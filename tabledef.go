package sorm

import (
	"sort"
	"strings"
	"sync"
)

// TableDef — диалект-нейтральное описание таблицы сущности. Генерируется
// `sorm gen` и регистрируется вместе с метой; используется миграциями
// (sorm/migrate, `sorm schema`) как источник желаемой схемы.
type TableDef struct {
	Name    string
	Columns []ColumnDef
	Indexes []IndexDef
}

// IndexDef — индекс таблицы. Простые (в т.ч. композитные) индексы задаются
// тегами `index:`/`uniqueIndex:`; кастомные — опциональным методом модели:
//
//	func (Post) Indexes() []sorm.IndexDef {
//	    return []sorm.IndexDef{
//	        {Name: "idx_posts_fts", Type: "gin",
//	         Parts: []sorm.IndexPart{{Expr: "to_tsvector('russian', title)"}}},
//	        {Name: "idx_posts_recent",
//	         Parts: []sorm.IndexPart{{Column: "created_at", Desc: true}},
//	         Where: "views > 0"},
//	    }
//	}
//
// `sorm gen` объединяет теги и метод в TableDef.
type IndexDef struct {
	Name    string
	Columns []string    // простые ASC-колонки (эквивалент Parts без Desc/Expr)
	Parts   []IndexPart // расширенный вариант: порядок сортировки и выражения
	Unique  bool
	Type    string // тип индекса: "gin"/"brin" (PG, USING), "fulltext" (MySQL)
	Where   string // частичный индекс (PG, SQLite); сырое SQL-условие
}

// IndexPart — элемент индекса: колонка или выражение.
type IndexPart struct {
	Column string // имя колонки (взаимоисключимо с Expr)
	Expr   string // сырое выражение: to_tsvector(...), lower(email), ...
	Desc   bool
}

// parts нормализует Columns+Parts в единый список.
func (ix IndexDef) parts() []IndexPart {
	out := make([]IndexPart, 0, len(ix.Columns)+len(ix.Parts))
	for _, c := range ix.Columns {
		out = append(out, IndexPart{Column: c})
	}
	return append(out, ix.Parts...)
}

// IndexParts — нормализованный список элементов индекса (для генераторов).
func (ix IndexDef) IndexParts() []IndexPart { return ix.parts() }

// ColumnDef — описание колонки.
type ColumnDef struct {
	Name     string
	GoKind   string // "bool","string","int*","uint*","float32/64","time","bytes"
	Nullable bool
	Unique   bool
	PK       bool
	Auto     bool
	SQLType  string // переопределение из тега type:
	RefTable string // FK: целевая таблица
	RefCol   string // FK: целевая колонка
}

var (
	tableDefsMu sync.Mutex
	tableDefs   []TableDef
)

// RegisterTable вызывается из init() сгенерированного пакета.
func RegisterTable(def TableDef) {
	tableDefsMu.Lock()
	defer tableDefsMu.Unlock()
	for i, d := range tableDefs {
		if d.Name == def.Name {
			tableDefs[i] = def
			return
		}
	}
	tableDefs = append(tableDefs, def)
}

// UnregisterTable удаляет описание таблицы (в основном для тестов миграций).
func UnregisterTable(name string) {
	tableDefsMu.Lock()
	defer tableDefsMu.Unlock()
	for i, d := range tableDefs {
		if d.Name == name {
			tableDefs = append(tableDefs[:i], tableDefs[i+1:]...)
			return
		}
	}
}

// Tables возвращает все зарегистрированные описания (детерминированный порядок).
func Tables() []TableDef {
	tableDefsMu.Lock()
	defer tableDefsMu.Unlock()
	out := append([]TableDef(nil), tableDefs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SQLTypeFor — тип колонки в SQL для диалекта ("postgres","mysql","sqlite").
// Единая точка маппинга для DDL-генератора и миграций.
func SQLTypeFor(dialect string, c ColumnDef) string {
	if c.SQLType != "" {
		return strings.ToUpper(c.SQLType)
	}
	switch dialect {
	case "mysql":
		return myTypeOf(c)
	case "sqlite":
		return liteTypeOf(c)
	default:
		return pgTypeOf(c)
	}
}

func pgTypeOf(c ColumnDef) string {
	switch c.GoKind {
	case "bytes":
		return "BYTEA"
	case "time":
		return "TIMESTAMPTZ"
	case "bool":
		return "BOOLEAN"
	case "string":
		return "TEXT"
	case "int8", "int16", "uint8":
		return "SMALLINT"
	case "int32", "int", "uint16":
		return "INTEGER"
	case "int64", "uint", "uint32", "uint64":
		return "BIGINT"
	case "float32":
		return "REAL"
	case "float64":
		return "DOUBLE PRECISION"
	default:
		return "TEXT"
	}
}

func myTypeOf(c ColumnDef) string {
	switch c.GoKind {
	case "bytes":
		return "BLOB"
	case "time":
		return "DATETIME(6)"
	case "bool":
		return "BOOLEAN"
	case "string":
		// TEXT в MySQL не индексируется без длины — дефолт VARCHAR(255),
		// длиннее — через тег type:.
		return "VARCHAR(255)"
	case "int8", "int16", "uint8":
		return "SMALLINT"
	case "int32", "int", "uint16":
		return "INT"
	case "int64", "uint", "uint32", "uint64":
		return "BIGINT"
	case "float32":
		return "FLOAT"
	case "float64":
		return "DOUBLE"
	default:
		return "VARCHAR(255)"
	}
}

func liteTypeOf(c ColumnDef) string {
	switch c.GoKind {
	case "bytes":
		return "BLOB"
	case "time":
		return "DATETIME"
	case "bool":
		return "BOOLEAN"
	case "string":
		return "TEXT"
	case "float32", "float64":
		return "REAL"
	default:
		return "INTEGER"
	}
}
