// Package parse разбирает пакет моделей: находит структуры-сущности
// (маркер — ровно одно поле с тегом `sorm:"pk..."`), классифицирует поля
// по Go-типам и валидирует схему до генерации.
package parse

import (
	"fmt"
	"go/types"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type Kind int

const (
	KindEq    Kind = iota // Col: bool и прочие только-равенство
	KindOrd               // OrdCol: числа, time.Time, именованные упорядоченные
	KindStr               // StrCol: string
	KindBytes             // BytesCol: []byte
)

type Schema struct {
	PkgPath  string // import path пакета моделей
	PkgName  string
	Entities []Entity // отсортированы по имени — детерминизм генерации
}

type Entity struct {
	Name         string // имя Go-типа
	Table        string
	Fields       []Field // только колонки, в порядке объявления
	Relations    []Relation
	Indexes      []Index // из тегов index:/uniqueIndex: (композитные — по общему имени)
	// HasIndexesMethod: у типа есть метод Indexes() []sorm.IndexDef —
	// кастомные индексы (DESC, выражения, полнотекст, частичные);
	// сгенерированный код объединит его с тегами.
	HasIndexesMethod bool
	PKIndex          int // индекс в Fields
	VersionIndex     int // -1 если нет
}

// Index — индекс таблицы; колонки в порядке объявления полей.
type Index struct {
	Name   string
	Cols   []string
	Unique bool
}

func (e Entity) PK() Field { return e.Fields[e.PKIndex] }

type Field struct {
	GoName   string
	Col      string
	Kind     Kind
	TypeExpr string // тип для дескриптора/снапшота: "int64", "time.Time", "models.Status"
	Nullable bool   // поле — указатель; TypeExpr уже разыменован
	IsTime   bool
	IsBytes  bool
	PK       bool
	Auto     bool
	Unique   bool
	Version  bool
	FK       string // "User.ID" из тега fk:, пусто если нет
	SQLType  string // переопределение типа колонки из тега type:
	// BasicKind — underlying-тип для маппинга в SQL: "string","int64",...,
	// "time","bytes" (независим от именованных типов).
	BasicKind string
}

type Relation struct {
	GoName    string
	Kind      string // "hasMany" | "belongsTo" | "hasOne" | "many2many"
	Target    string // имя сущности
	FKField   string // Go-имя FK-поля (не для many2many)
	JoinTable string // только many2many
	Slice     bool
}

// Load разбирает пакет моделей в dir.
func Load(dir string) (*Schema, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  dir,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", dir, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("expected exactly one package in %s, got %d", dir, len(pkgs))
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		return nil, fmt.Errorf("package %s does not compile: %v", pkg.PkgPath, pkg.Errors[0])
	}

	s := &Schema{PkgPath: pkg.PkgPath, PkgName: pkg.Name}

	scope := pkg.Types.Scope()
	names := scope.Names()
	sort.Strings(names)

	// Первый проход: имена сущностей (нужны для валидации навигаций).
	entityNames := map[string]bool{}
	for _, name := range names {
		if st := structOf(scope.Lookup(name)); st != nil && hasPKTag(st) {
			entityNames[name] = true
		}
	}
	if len(entityNames) == 0 {
		return nil, fmt.Errorf("no entities found in %s (an entity is a struct with a `sorm:\"pk\"` field)", pkg.PkgPath)
	}

	for _, name := range names {
		obj := scope.Lookup(name)
		st := structOf(obj)
		if st == nil || !hasPKTag(st) {
			continue
		}
		ent, err := parseEntity(name, st, pkg.Types, entityNames)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", pkg.Name, name, err)
		}
		ent.HasIndexesMethod = hasIndexesMethod(obj)
		s.Entities = append(s.Entities, *ent)
	}

	if err := validateRelations(s); err != nil {
		return nil, err
	}
	return s, nil
}

func structOf(obj types.Object) *types.Struct {
	tn, ok := obj.(*types.TypeName)
	if !ok {
		return nil
	}
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return nil
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	return st
}

// hasIndexesMethod — есть ли у типа метод Indexes() []sorm.IndexDef.
func hasIndexesMethod(obj types.Object) bool {
	named, ok := obj.(*types.TypeName).Type().(*types.Named)
	if !ok {
		return false
	}
	for i := 0; i < named.NumMethods(); i++ {
		m := named.Method(i)
		if m.Name() != "Indexes" {
			continue
		}
		sig, ok := m.Type().(*types.Signature)
		if !ok || sig.Params().Len() != 0 || sig.Results().Len() != 1 {
			return false
		}
		return strings.HasSuffix(sig.Results().At(0).Type().String(), "sorm.IndexDef")
	}
	return false
}

func hasPKTag(st *types.Struct) bool {
	for i := 0; i < st.NumFields(); i++ {
		if opts := tagOptions(st.Tag(i)); opts.has("pk") {
			return true
		}
	}
	return false
}

func parseEntity(name string, st *types.Struct, modelsPkg *types.Package, entityNames map[string]bool) (*Entity, error) {
	ent := &Entity{Name: name, Table: pluralSnake(name), PKIndex: -1, VersionIndex: -1}

	qual := func(p *types.Package) string {
		if p == modelsPkg {
			return "models" // алиас импорта в сгенерированном пакете
		}
		return p.Name()
	}

	for i := 0; i < st.NumFields(); i++ {
		fv := st.Field(i)
		if !fv.Exported() {
			continue // неэкспортируемое поле не может быть колонкой
		}
		opts := tagOptions(st.Tag(i))
		if opts.has("-") {
			continue
		}
		if tbl, ok := opts.value("table"); ok {
			ent.Table = tbl
		}

		// Навигация?
		if rel, ok := parseRelation(fv, opts, modelsPkg, entityNames); ok {
			if rel == nil {
				return nil, fmt.Errorf(
					"field %s: struct-typed field needs a relation tag (sorm:\"hasMany:FK\" / \"belongsTo:FK\" / \"hasOne:FK\") or sorm:\"-\"",
					fv.Name())
			}
			ent.Relations = append(ent.Relations, *rel)
			continue
		}

		f, err := parseColumn(fv, opts, qual)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", fv.Name(), err)
		}
		if f.PK {
			if ent.PKIndex >= 0 {
				return nil, fmt.Errorf("field %s: multiple pk fields (composite PK is out of MVP scope)", fv.Name())
			}
			ent.PKIndex = len(ent.Fields)
		}
		if f.Version {
			if ent.VersionIndex >= 0 {
				return nil, fmt.Errorf("field %s: multiple version fields", fv.Name())
			}
			if f.TypeExpr != "int64" || f.Nullable {
				return nil, fmt.Errorf("field %s: version field must be plain int64", fv.Name())
			}
			ent.VersionIndex = len(ent.Fields)
		}
		ent.Fields = append(ent.Fields, *f)
	}

	if ent.PKIndex < 0 {
		return nil, fmt.Errorf("no pk field")
	}
	if err := collectIndexes(ent, st); err != nil {
		return nil, err
	}
	if pk := ent.PK(); pk.Auto && !isIntExpr(pk.TypeExpr) {
		return nil, fmt.Errorf("field %s: auto pk must be an integer type", pk.GoName)
	}
	return ent, nil
}

// parseRelation возвращает (rel, true) если поле — навигация по своему типу.
// rel == nil при отсутствии обязательного тега.
func parseRelation(fv *types.Var, opts tagOpts, modelsPkg *types.Package, entityNames map[string]bool) (*Relation, bool) {
	target, slice := navigationTarget(fv.Type(), modelsPkg)
	if target == "" || !entityNames[target] {
		return nil, false
	}
	for _, kind := range []string{"hasMany", "belongsTo", "hasOne"} {
		if fk, ok := opts.value(kind); ok {
			return &Relation{GoName: fv.Name(), Kind: kind, Target: target, FKField: fk, Slice: slice}, true
		}
	}
	if jt, ok := opts.value("many2many"); ok {
		return &Relation{GoName: fv.Name(), Kind: "many2many", Target: target, JoinTable: jt, Slice: slice}, true
	}
	return nil, true // тип навигационный, тега нет — ошибка выше
}

// navigationTarget: []*T / []T / *T, где T — именованная структура пакета моделей.
func navigationTarget(t types.Type, modelsPkg *types.Package) (name string, slice bool) {
	if sl, ok := t.(*types.Slice); ok {
		n := namedStructIn(sl.Elem(), modelsPkg)
		return n, true
	}
	return namedStructIn(t, modelsPkg), false
}

func namedStructIn(t types.Type, pkg *types.Package) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() != pkg {
		return ""
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return ""
	}
	return named.Obj().Name()
}

func parseColumn(fv *types.Var, opts tagOpts, qual types.Qualifier) (*Field, error) {
	f := &Field{
		GoName:  fv.Name(),
		Col:     snake(fv.Name()),
		PK:      opts.has("pk"),
		Auto:    opts.has("auto"),
		Unique:  opts.has("unique"),
		Version: opts.has("version"),
	}
	if col, ok := opts.value("col"); ok {
		f.Col = col
	}
	if fk, ok := opts.value("fk"); ok {
		f.FK = fk
	}
	if st, ok := opts.value("type"); ok {
		f.SQLType = st
	}

	t := fv.Type()
	if p, ok := t.(*types.Pointer); ok {
		f.Nullable = true
		t = p.Elem()
	}

	switch {
	case isByteSlice(t):
		if f.Nullable {
			return nil, fmt.Errorf("*[]byte is not supported; []byte is already nullable")
		}
		f.Kind, f.IsBytes, f.TypeExpr, f.BasicKind = KindBytes, true, "[]byte", "bytes"
	case isTime(t):
		f.Kind, f.IsTime, f.TypeExpr, f.BasicKind = KindOrd, true, "time.Time", "time"
	default:
		basic, ok := t.Underlying().(*types.Basic)
		if !ok {
			return nil, fmt.Errorf("unsupported column type %s (sql.Null* is not supported — use a pointer)", t)
		}
		f.TypeExpr = types.TypeString(t, qual)
		f.BasicKind = basic.Name()
		info := basic.Info()
		switch {
		case info&types.IsBoolean != 0:
			f.Kind = KindEq
		case info&types.IsString != 0:
			if f.TypeExpr == "string" {
				f.Kind = KindStr
			} else {
				f.Kind = KindOrd // именованный строковый тип: без Like-предикатов, но типобезопасно
			}
		case info&types.IsNumeric != 0:
			f.Kind = KindOrd
		default:
			return nil, fmt.Errorf("unsupported basic type %s", basic)
		}
	}
	return f, nil
}

func validateRelations(s *Schema) error {
	byName := map[string]*Entity{}
	for i := range s.Entities {
		byName[s.Entities[i].Name] = &s.Entities[i]
	}
	for _, e := range s.Entities {
		for _, r := range e.Relations {
			if r.Kind == "many2many" {
				if r.JoinTable == "" {
					return fmt.Errorf("%s.%s: many2many требует имя join-таблицы", e.Name, r.GoName)
				}
				if !r.Slice {
					return fmt.Errorf("%s.%s: many2many должно быть слайсом", e.Name, r.GoName)
				}
				continue
			}
			// FK-поле живёт на стороне «многих»: у target для hasMany, у себя для belongsTo/hasOne-владельца.
			owner := byName[r.Target]
			if r.Kind == "belongsTo" {
				owner = byName[e.Name]
			}
			if owner == nil {
				return fmt.Errorf("%s.%s: relation %s targets unknown entity %q",
					e.Name, r.GoName, r.Kind, r.Target)
			}
			if !hasField(owner, r.FKField) {
				return fmt.Errorf("%s.%s: relation %s references unknown FK field %q on %s",
					e.Name, r.GoName, r.Kind, r.FKField, owner.Name)
			}
		}
	}
	return nil
}

// collectIndexes собирает индексы из тегов `index[:name]` и
// `uniqueIndex[:name]`: поля с одним именем образуют композитный индекс
// в порядке объявления; без имени — одноколоночный idx_<table>_<col>.
func collectIndexes(ent *Entity, st *types.Struct) error {
	byName := map[string]*Index{}
	fieldPos := 0
	for i := 0; i < st.NumFields(); i++ {
		fv := st.Field(i)
		if !fv.Exported() {
			continue
		}
		opts := tagOptions(st.Tag(i))
		if opts.has("-") {
			continue
		}
		// колонка ли это поле (навигации пропускаем): сверяем по позиции в Fields
		if fieldPos >= len(ent.Fields) || ent.Fields[fieldPos].GoName != fv.Name() {
			continue
		}
		col := ent.Fields[fieldPos].Col
		fieldPos++

		for _, kind := range []struct {
			opt    string
			unique bool
		}{{"index", false}, {"uniqueIndex", true}} {
			name, hasVal := opts.value(kind.opt)
			if !hasVal && !opts.has(kind.opt) {
				continue
			}
			if name == "" {
				name = "idx_" + ent.Table + "_" + col
			}
			if ix, ok := byName[name]; ok {
				if ix.Unique != kind.unique {
					return fmt.Errorf("index %q: смешаны index и uniqueIndex", name)
				}
				ix.Cols = append(ix.Cols, col)
				continue
			}
			ix := &Index{Name: name, Cols: []string{col}, Unique: kind.unique}
			byName[name] = ix
			ent.Indexes = append(ent.Indexes, Index{Name: name}) // позиция; содержимое ниже
		}
	}
	// композитные индексы дособрались в byName — материализуем в порядке появления
	for i := range ent.Indexes {
		ent.Indexes[i] = *byName[ent.Indexes[i].Name]
	}
	return nil
}

// ResolveFK: "User.ID" → ("users", "id").
func (s *Schema) ResolveFK(fk string) (table, col string, err error) {
	entName, fieldName, ok := strings.Cut(fk, ".")
	if !ok {
		return "", "", fmt.Errorf("bad fk tag %q (want Entity.Field)", fk)
	}
	for _, e := range s.Entities {
		if e.Name != entName {
			continue
		}
		for _, f := range e.Fields {
			if f.GoName == fieldName {
				return e.Table, f.Col, nil
			}
		}
	}
	return "", "", fmt.Errorf("fk tag %q: target not found", fk)
}

func hasField(e *Entity, goName string) bool {
	for _, f := range e.Fields {
		if f.GoName == goName {
			return true
		}
	}
	return false
}

func isByteSlice(t types.Type) bool {
	sl, ok := t.(*types.Slice)
	if !ok {
		return false
	}
	b, ok := sl.Elem().(*types.Basic)
	return ok && b.Kind() == types.Byte
}

func isTime(t types.Type) bool {
	named, ok := t.(*types.Named)
	return ok && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "time" && named.Obj().Name() == "Time"
}

func isIntExpr(expr string) bool {
	switch expr {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return true
	}
	return false
}

// --- теги ---

type tagOpts []string

func tagOptions(structTag string) tagOpts {
	v, ok := reflect.StructTag(structTag).Lookup("sorm")
	if !ok {
		return nil
	}
	return strings.Split(v, ",")
}

func (o tagOpts) has(name string) bool {
	for _, s := range o {
		if s == name {
			return true
		}
	}
	return false
}

func (o tagOpts) value(name string) (string, bool) {
	for _, s := range o {
		if v, ok := strings.CutPrefix(s, name+":"); ok {
			return v, true
		}
	}
	return "", false
}

// --- имена ---

// Snake — экспорт snake_case для генераторов (имена колонок join-таблиц).
func Snake(s string) string { return snake(s) }

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			// граница слова: не первый символ и (предыдущий строчный ИЛИ следующий строчный — конец аббревиатуры)
			if i > 0 && (isLower(rune(s[i-1])) || (i+1 < len(s) && isLower(rune(s[i+1])))) {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isLower(r rune) bool { return r >= 'a' && r <= 'z' }

func pluralSnake(name string) string {
	s := snake(name)
	switch {
	case strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(rune(s[len(s)-2])):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s"), strings.HasSuffix(s, "x"), strings.HasSuffix(s, "z"),
		strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func isVowel(r rune) bool { return strings.ContainsRune("aeiou", r) }
