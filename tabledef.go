package sorm

import (
	"sort"
	"strings"
	"sync"
)

// TableDef — a dialect-neutral description of an entity's table. Generated
// by `sorm gen` and registered alongside the meta; used by migrations
// (sorm/migrate, `sorm schema`) as the source of the desired schema.
type TableDef struct {
	Name    string
	Columns []ColumnDef
	Indexes []IndexDef
}

// IndexDef — a table index. Simple (including composite) indexes are declared
// with `index:`/`uniqueIndex:` tags; custom ones via an optional model method:
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
// `sorm gen` merges the tags and the method into the TableDef.
type IndexDef struct {
	Name    string
	Columns []string    // simple ASC columns (equivalent to Parts without Desc/Expr)
	Parts   []IndexPart // extended form: sort order and expressions
	Unique  bool
	Type    string // index type: "gin"/"brin" (PG, USING), "fulltext" (MySQL)
	Where   string // partial index (PG, SQLite); raw SQL condition
}

// IndexPart — an index element: a column or an expression.
type IndexPart struct {
	Column string // column name (mutually exclusive with Expr)
	Expr   string // raw expression: to_tsvector(...), lower(email), ...
	Desc   bool
}

// parts normalizes Columns+Parts into a single list.
func (ix IndexDef) parts() []IndexPart {
	out := make([]IndexPart, 0, len(ix.Columns)+len(ix.Parts))
	for _, c := range ix.Columns {
		out = append(out, IndexPart{Column: c})
	}
	return append(out, ix.Parts...)
}

// IndexParts — the normalized list of index elements (for generators).
func (ix IndexDef) IndexParts() []IndexPart { return ix.parts() }

// ColumnDef — a column description.
type ColumnDef struct {
	Name     string
	GoKind   string // "bool","string","int*","uint*","float32/64","time","bytes"
	Nullable bool
	Unique   bool
	PK       bool
	Auto     bool
	SQLType  string // override from the type: tag
	RefTable string // FK: target table
	RefCol   string // FK: target column
}

var (
	tableDefsMu sync.Mutex
	tableDefs   []TableDef
)

// RegisterTable is called from the generated package's init().
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

// UnregisterTable removes a table definition (mainly for migration tests).
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

// Tables returns all registered definitions (deterministic order).
func Tables() []TableDef {
	tableDefsMu.Lock()
	defer tableDefsMu.Unlock()
	out := append([]TableDef(nil), tableDefs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SQLTypeFor — the column's SQL type for a dialect ("postgres","mysql","sqlite").
// Single mapping point for the DDL generator and migrations.
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
		// TEXT in MySQL cannot be indexed without a length — default is VARCHAR(255);
		// use the type: tag for longer values.
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
