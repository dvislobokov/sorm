// Package parse parses the models package: it finds entity structs
// (marked by exactly one field with a `sorm:"pk..."` tag), classifies
// fields by Go type, and validates the schema before generation.
package parse

import (
	"fmt"
	"go/types"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type Kind int

const (
	KindEq     Kind = iota // Col: bool and other equality-only types
	KindOrd                // OrdCol: numbers, time.Time, named ordered types
	KindStr                // StrCol: string
	KindBytes              // BytesCol: []byte
	KindJSON               // JSONCol: any marshalable type stored as JSON
	KindScalar             // ScalarCol: named type implementing driver.Valuer + sql.Scanner
	KindArray              // ArrayCol: []T with sorm:"array" — native PostgreSQL array
)

type Schema struct {
	PkgPath  string // import path of the models package
	PkgName  string
	Naming   string // identifier naming strategy (NamingSnake/Camel/Pascal)
	Entities []Entity // sorted by name — deterministic generation
}

type Entity struct {
	Name         string // Go type name
	Table        string
	Fields       []Field // columns only, in declaration order
	Relations    []Relation
	Indexes      []Index // from index:/uniqueIndex: tags (composite — via a shared name)
	// HasIndexesMethod: the type has an Indexes() []sorm.IndexDef method —
	// custom indexes (DESC, expressions, full-text, partial);
	// the generated code merges it with the tag-defined ones.
	HasIndexesMethod bool
	PKIndex          int // index into Fields
	VersionIndex     int // -1 if absent
}

// Index is a table index; columns follow field declaration order.
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
	TypeExpr string // type for the descriptor/snapshot: "int64", "time.Time", "models.Status"
	Nullable bool   // field is a pointer; TypeExpr is already dereferenced
	IsTime   bool
	IsBytes  bool
	PK       bool
	Auto     bool
	Unique   bool
	Version  bool
	// AutoCreate/AutoUpdate — auto-timestamps (plain time.Time only):
	// autoCreate stamps on INSERT when zero, autoUpdate on INSERT and
	// every effective UPDATE.
	AutoCreate bool
	AutoUpdate bool
	// SoftDelete — sorm:"softDelete" on a *time.Time: queries filter the
	// column IS NULL, deletes become UPDATEs stamping it.
	SoftDelete bool
	FK       string // "User.ID" from the fk: tag, empty if absent
	SQLType  string // column type override from the type: tag
	// BasicKind — underlying type for the SQL mapping: "string","int64",...,
	// "time","bytes" (independent of named types).
	BasicKind string
	// JSONDoc — the field tree of a struct-typed json column (nil for
	// maps/slices/unknown shapes); drives typed accessor generation.
	JSONDoc []JSONField
	// ImportPath — package of a KindScalar type living outside the models
	// package (e.g. github.com/shopspring/decimal); "" otherwise.
	ImportPath string
	// ElemExpr — element type of a KindArray column ("string", "int64", ...).
	ElemExpr string
}

// JSONField — one field of a JSON document (for typed accessors).
type JSONField struct {
	GoName string      // Theme
	Key    string      // json key ("theme"); from the json tag or the field name
	Kind   string      // "string" | "int" | "float" | "bool" | "array" | "object"
	Fields []JSONField // populated for Kind == "object"
}

type Relation struct {
	GoName    string
	Kind      string // "hasMany" | "belongsTo" | "hasOne" | "many2many"
	Target    string // entity name
	FKField   string // Go name of the FK field (not for many2many)
	JoinTable string // many2many only
	Slice     bool
}

// Naming strategies for derived table and column identifiers.
// Explicit overrides (col:, table:) always win regardless of strategy.
const (
	NamingSnake  = "snake"  // CreatedAt → created_at, ApiKey → api_keys (default)
	NamingCamel  = "camel"  // CreatedAt → createdAt, ApiKey → apiKeys
	NamingPascal = "pascal" // CreatedAt → CreatedAt, ApiKey → ApiKeys
)

// Option configures Load.
type Option func(*config)

type config struct{ naming string }

// WithNaming sets the identifier naming strategy (NamingSnake/Camel/Pascal).
func WithNaming(naming string) Option { return func(c *config) { c.naming = naming } }

// Load parses the models package in dir.
func Load(dir string, opts ...Option) (*Schema, error) {
	c := config{naming: NamingSnake}
	for _, o := range opts {
		o(&c)
	}
	switch c.naming {
	case NamingSnake, NamingCamel, NamingPascal:
	default:
		return nil, fmt.Errorf("unknown naming strategy %q (snake|camel|pascal)", c.naming)
	}
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

	s := &Schema{PkgPath: pkg.PkgPath, PkgName: pkg.Name, Naming: c.naming}

	scope := pkg.Types.Scope()
	names := scope.Names()
	sort.Strings(names)

	// First pass: entity names (needed to validate navigations).
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
		ent, err := parseEntity(name, st, pkg.Types, entityNames, c.naming)
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

// hasIndexesMethod reports whether the type has an Indexes() []sorm.IndexDef method.
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

func parseEntity(name string, st *types.Struct, modelsPkg *types.Package, entityNames map[string]bool, naming string) (*Entity, error) {
	ent := &Entity{Name: name, Table: RenamePlural(naming, name), PKIndex: -1, VersionIndex: -1}

	qual := func(p *types.Package) string {
		if p == modelsPkg {
			return "models" // import alias in the generated package
		}
		return p.Name()
	}

	for i := 0; i < st.NumFields(); i++ {
		fv := st.Field(i)
		if !fv.Exported() {
			continue // an unexported field cannot be a column
		}
		opts := tagOptions(st.Tag(i))
		if opts.has("-") {
			continue
		}
		if tbl, ok := opts.value("table"); ok {
			ent.Table = tbl
		}

		// A JSON column? Checked before navigation detection: a struct-typed
		// field tagged sorm:"json" is a column, not a relation.
		if opts.has("json") {
			f, err := parseJSONColumn(fv, opts, qual, naming)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", fv.Name(), err)
			}
			ent.Fields = append(ent.Fields, *f)
			continue
		}

		// A navigation?
		if rel, ok := parseRelation(fv, opts, modelsPkg, entityNames); ok {
			if rel == nil {
				return nil, fmt.Errorf(
					"field %s: struct-typed field needs a relation tag (sorm:\"hasMany:FK\" / \"belongsTo:FK\" / \"hasOne:FK\") or sorm:\"-\"",
					fv.Name())
			}
			ent.Relations = append(ent.Relations, *rel)
			continue
		}

		f, err := parseColumn(fv, opts, qual, naming)
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
	softDeletes := 0
	for _, f := range ent.Fields {
		if f.SoftDelete {
			softDeletes++
		}
	}
	if softDeletes > 1 {
		return nil, fmt.Errorf("multiple softDelete fields")
	}
	if err := collectIndexes(ent, st); err != nil {
		return nil, err
	}
	if pk := ent.PK(); pk.Auto && !isIntExpr(pk.TypeExpr) {
		return nil, fmt.Errorf("field %s: auto pk must be an integer type", pk.GoName)
	}
	return ent, nil
}

// parseRelation returns (rel, true) if the field is a navigation by its type.
// rel == nil when the required tag is missing.
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
	return nil, true // navigation type without a tag — error raised by the caller
}

// navigationTarget: []*T / []T / *T, where T is a named struct from the models package.
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

// parseJSONColumn handles `sorm:"json"` fields: any marshalable Go type
// (struct, map, slice), nullable via a pointer. []byte is rejected —
// ambiguous with the bytes column kind.
func parseJSONColumn(fv *types.Var, opts tagOpts, qual types.Qualifier, naming string) (*Field, error) {
	f := &Field{
		GoName:    fv.Name(),
		Col:       Rename(naming, fv.Name()),
		Kind:      KindJSON,
		BasicKind: "json",
		Unique:    opts.has("unique"),
	}
	if opts.has("pk") || opts.has("version") {
		return nil, fmt.Errorf("json column cannot be a pk or version field")
	}
	if col, ok := opts.value("col"); ok {
		f.Col = col
	}
	if st, ok := opts.value("type"); ok {
		f.SQLType = st
	}

	t := fv.Type()
	if p, ok := t.(*types.Pointer); ok {
		f.Nullable = true
		t = p.Elem()
	}
	if isByteSlice(t) {
		return nil, fmt.Errorf("[]byte cannot be a json column (store it as bytes, or use json.RawMessage semantics via a named type)")
	}
	// nil is the natural zero of maps and slices — such columns are nullable
	// (nil ⇒ SQL NULL), like []byte. Non-pointer structs stay NOT NULL:
	// their zero value marshals to a valid document.
	switch t.Underlying().(type) {
	case *types.Map, *types.Slice:
		f.Nullable = true
	}
	f.TypeExpr = types.TypeString(t, qual)
	f.JSONDoc = jsonDocOf(t, 0)
	return f, nil
}

const jsonDocMaxDepth = 3

// jsonDocOf walks a struct type stored as JSON and builds the field tree
// for typed accessors. Non-struct shapes (maps, slices) return nil —
// Path(...) remains the way to query them.
func jsonDocOf(t types.Type, depth int) []JSONField {
	st, ok := t.Underlying().(*types.Struct)
	if !ok || isTime(t) || isUUID(t) {
		return nil
	}
	var out []JSONField
	for i := 0; i < st.NumFields(); i++ {
		fv := st.Field(i)
		if !fv.Exported() {
			continue
		}
		key := fv.Name()
		if tag, ok := reflect.StructTag(st.Tag(i)).Lookup("json"); ok {
			name, _, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if name != "" {
				key = name
			}
		}
		if !jsonKeyRe.MatchString(key) {
			continue // exotic keys stay reachable via Path
		}

		ft := fv.Type()
		if p, ok := ft.(*types.Pointer); ok {
			ft = p.Elem()
		}
		jf := JSONField{GoName: fv.Name(), Key: key}
		switch {
		case isTime(ft) || isUUID(ft):
			jf.Kind = "string" // serialized as JSON strings
		case isByteSlice(ft):
			continue // base64 blob — not queryable in a typed way
		default:
			switch u := ft.Underlying().(type) {
			case *types.Basic:
				info := u.Info()
				switch {
				case info&types.IsBoolean != 0:
					jf.Kind = "bool"
				case info&types.IsString != 0:
					jf.Kind = "string"
				case info&types.IsInteger != 0:
					jf.Kind = "int"
				case info&types.IsFloat != 0:
					jf.Kind = "float"
				default:
					continue
				}
			case *types.Slice, *types.Array:
				jf.Kind = "array"
			case *types.Struct:
				if depth+1 >= jsonDocMaxDepth {
					continue
				}
				jf.Kind = "object"
				jf.Fields = jsonDocOf(ft, depth+1)
				if len(jf.Fields) == 0 {
					continue
				}
			default:
				continue // maps, interfaces, channels — Path fallback
			}
		}
		out = append(out, jf)
	}
	return out
}

var jsonKeyRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func parseColumn(fv *types.Var, opts tagOpts, qual types.Qualifier, naming string) (*Field, error) {
	f := &Field{
		GoName:     fv.Name(),
		Col:        Rename(naming, fv.Name()),
		PK:         opts.has("pk"),
		Auto:       opts.has("auto"),
		Unique:     opts.has("unique"),
		Version:    opts.has("version"),
		AutoCreate: opts.has("autoCreate"),
		AutoUpdate: opts.has("autoUpdate"),
		SoftDelete: opts.has("softDelete"),
	}
	if f.AutoCreate && f.AutoUpdate {
		return nil, fmt.Errorf("autoCreate and autoUpdate on one field are redundant — autoUpdate already stamps on insert")
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

	if opts.has("array") {
		return parseArrayColumn(f, t, qual)
	}

	switch {
	case isByteSlice(t):
		if f.Nullable {
			return nil, fmt.Errorf("*[]byte is not supported; []byte is already nullable")
		}
		f.Kind, f.IsBytes, f.TypeExpr, f.BasicKind = KindBytes, true, "[]byte", "bytes"
	case isTime(t):
		f.Kind, f.IsTime, f.TypeExpr, f.BasicKind = KindOrd, true, "time.Time", "time"
	case isUUID(t):
		// uuid.UUID is a comparable [16]byte: equality predicates, plain
		// snapshot comparison and map keys all work by value. Generation is
		// client-side (uuid.New() before Add); `auto` is not applicable.
		f.Kind, f.TypeExpr, f.BasicKind = KindEq, "uuid.UUID", "uuid"
	default:
		basic, ok := t.Underlying().(*types.Basic)
		if !ok {
			if scalarColumn(t) {
				return parseScalarColumn(f, t, qual)
			}
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
				f.Kind = KindOrd // named string type: no Like predicates, but type-safe
			}
		case info&types.IsNumeric != 0:
			f.Kind = KindOrd
		default:
			return nil, fmt.Errorf("unsupported basic type %s", basic)
		}
	}
	if (f.AutoCreate || f.AutoUpdate) && (!f.IsTime || f.Nullable) {
		return nil, fmt.Errorf("autoCreate/autoUpdate requires a plain time.Time field")
	}
	if f.SoftDelete && (!f.IsTime || !f.Nullable) {
		return nil, fmt.Errorf("softDelete requires a *time.Time field (NULL = alive)")
	}
	if f.SoftDelete && (f.PK || f.Version || f.AutoCreate || f.AutoUpdate) {
		return nil, fmt.Errorf("softDelete cannot be combined with pk/version/auto-timestamps")
	}
	return f, nil
}

// parseArrayColumn handles `sorm:"array"`: []T of a basic element, stored
// as a native PostgreSQL array (text[], bigint[], ...). Other dialects
// reject the column at DDL/migration time. A nil slice maps to SQL NULL.
func parseArrayColumn(f *Field, t types.Type, qual types.Qualifier) (*Field, error) {
	if f.Nullable {
		return nil, fmt.Errorf("*[]T is not supported for array columns; a nil slice is already NULL")
	}
	if f.PK || f.Version || f.AutoCreate || f.AutoUpdate {
		return nil, fmt.Errorf("array column cannot be pk/version/auto-timestamp")
	}
	sl, ok := t.Underlying().(*types.Slice)
	if !ok {
		return nil, fmt.Errorf("sorm:\"array\" requires a slice type, got %s", t)
	}
	elemBasic, ok := sl.Elem().Underlying().(*types.Basic)
	if !ok {
		return nil, fmt.Errorf("array element must be a basic type, got %s", sl.Elem())
	}
	switch elemBasic.Name() {
	case "string", "int64", "int32", "int", "float64", "bool":
	default:
		return nil, fmt.Errorf("unsupported array element type %s (string|int64|int32|int|float64|bool)", elemBasic.Name())
	}
	f.Kind = KindArray
	f.Nullable = true // nil slice ⇒ NULL
	f.TypeExpr = types.TypeString(t, qual)
	f.ElemExpr = types.TypeString(sl.Elem(), qual)
	f.BasicKind = "array:" + elemBasic.Name()
	return f, nil
}

// parseScalarColumn accepts a custom scalar: a named type implementing
// driver.Valuer + sql.Scanner (decimal.Decimal, money types, encrypted
// strings). The SQL type is not statically derivable — the type: tag is
// required. NULL is the type's own job (e.g. decimal.NullDecimal):
// pointer scalars are rejected because **T cannot be a Scan target.
func parseScalarColumn(f *Field, t types.Type, qual types.Qualifier) (*Field, error) {
	if f.Nullable {
		return nil, fmt.Errorf("pointer to a Valuer/Scanner type is not supported — handle NULL inside the type (e.g. decimal.NullDecimal)")
	}
	if f.PK || f.Version || f.FK != "" {
		return nil, fmt.Errorf("a Valuer/Scanner scalar cannot be a pk, version or fk column")
	}
	if f.SQLType == "" {
		return nil, fmt.Errorf("a Valuer/Scanner scalar requires an explicit SQL type: add sorm:\"type:numeric(20,8)\" (or similar)")
	}
	f.Kind = KindScalar
	f.BasicKind = "scalar"
	f.TypeExpr = types.TypeString(t, qual)
	if pkg := t.(*types.Named).Obj().Pkg(); pkg != nil && qual(pkg) != "models" {
		f.ImportPath = pkg.Path()
	}
	return f, nil
}

// scalarColumn reports whether the named type implements driver.Valuer
// (value receiver) and sql.Scanner (pointer receiver) — matched
// structurally: Value() with 0 params / 2 results, Scan with 1/1.
func scalarColumn(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	var hasValue, hasScan bool
	// Methods of *T cover both receiver kinds.
	ms := types.NewMethodSet(types.NewPointer(named))
	for i := 0; i < ms.Len(); i++ {
		m := ms.At(i).Obj().(*types.Func)
		sig := m.Type().(*types.Signature)
		switch {
		case m.Name() == "Value" && sig.Params().Len() == 0 && sig.Results().Len() == 2:
			hasValue = true
		case m.Name() == "Scan" && sig.Params().Len() == 1 && sig.Results().Len() == 1:
			hasScan = true
		}
	}
	return hasValue && hasScan
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
					return fmt.Errorf("%s.%s: many2many requires a join table name", e.Name, r.GoName)
				}
				if !r.Slice {
					return fmt.Errorf("%s.%s: many2many must be a slice", e.Name, r.GoName)
				}
				continue
			}
			// The FK field lives on the "many" side: on target for hasMany, on self for belongsTo/hasOne owner.
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

// collectIndexes gathers indexes from `index[:name]` and
// `uniqueIndex[:name]` tags: fields sharing a name form a composite index
// in declaration order; without a name — a single-column idx_<table>_<col>.
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
		// is this field a column (navigations are skipped): match by position in Fields
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
					return fmt.Errorf("index %q: mixes index and uniqueIndex", name)
				}
				ix.Cols = append(ix.Cols, col)
				continue
			}
			ix := &Index{Name: name, Cols: []string{col}, Unique: kind.unique}
			byName[name] = ix
			ent.Indexes = append(ent.Indexes, Index{Name: name}) // placeholder; filled in below
		}
	}
	// composite indexes are fully assembled in byName — materialize in order of appearance
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

func isUUID(t types.Type) bool {
	named, ok := t.(*types.Named)
	return ok && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "github.com/google/uuid" && named.Obj().Name() == "UUID"
}

func isIntExpr(expr string) bool {
	switch expr {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return true
	}
	return false
}

// --- tags ---

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

// --- names ---

// Snake exports snake_case for generators (join-table column names).
func Snake(s string) string { return snake(s) }

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			// word boundary: not the first char and (previous is lowercase OR next is lowercase — end of an acronym)
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

// Rename derives an identifier from a Go name per the naming strategy:
// UserID → user_id | userId | UserId.
func Rename(naming, goName string) string {
	return joinWords(naming, strings.Split(snake(goName), "_"))
}

// RenamePlural derives a table name: the last word is pluralized.
// ApiKey → api_keys | apiKeys | ApiKeys.
func RenamePlural(naming, goName string) string {
	ws := strings.Split(snake(goName), "_")
	ws[len(ws)-1] = plural(ws[len(ws)-1])
	return joinWords(naming, ws)
}

func joinWords(naming string, ws []string) string {
	switch naming {
	case NamingCamel:
		out := ws[0]
		for _, w := range ws[1:] {
			out += title(w)
		}
		return out
	case NamingPascal:
		out := ""
		for _, w := range ws {
			out += title(w)
		}
		return out
	default: // NamingSnake
		return strings.Join(ws, "_")
	}
}

func title(w string) string {
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

func plural(s string) string {
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
