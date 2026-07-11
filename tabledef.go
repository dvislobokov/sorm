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
}

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
