// Package myagg provides MySQL-specific aggregate functions for sorm
// projections. Using them on another dialect is a build error returned
// when the query executes — never silently wrong SQL.
//
//	rows, err := sorm.Project[stat](
//	    sorm.From[models.User](db).GroupBy(u.Country),
//	    sorm.Field(u.Country),
//	    sorm.As(myagg.GroupConcatSep[models.User](u.Name, ", "), "names"),
//	).All(ctx)
package myagg

import (
	"github.com/dvislobokov/sorm"
)

const dialect = "mysql"

// GroupConcat — GROUP_CONCAT(col) with the default separator (",").
func GroupConcat[E any](c sorm.AnyCol) sorm.AggExpr[E, string] {
	return agg1[E, string]("GROUP_CONCAT", c)
}

// GroupConcatSep — GROUP_CONCAT(col SEPARATOR 'sep'). The separator must be
// a literal in MySQL's grammar, so it is embedded quote-escaped.
func GroupConcatSep[E any](c sorm.AnyCol, sep string) sorm.AggExpr[E, string] {
	return sorm.NewAgg[E, string](
		sorm.AggDialect(dialect),
		sorm.AggRaw("GROUP_CONCAT("), sorm.AggCol(c),
		sorm.AggRaw(" SEPARATOR "), sorm.AggLit(sep), sorm.AggRaw(")"),
	)
}

// GroupConcatDistinct — GROUP_CONCAT(DISTINCT col SEPARATOR 'sep').
func GroupConcatDistinct[E any](c sorm.AnyCol, sep string) sorm.AggExpr[E, string] {
	return sorm.NewAgg[E, string](
		sorm.AggDialect(dialect),
		sorm.AggRaw("GROUP_CONCAT(DISTINCT "), sorm.AggCol(c),
		sorm.AggRaw(" SEPARATOR "), sorm.AggLit(sep), sorm.AggRaw(")"),
	)
}

// JSONArrayAgg — JSON_ARRAYAGG(col): the group as a JSON array.
func JSONArrayAgg[E any](c sorm.AnyCol) sorm.AggExpr[E, string] {
	return agg1[E, string]("JSON_ARRAYAGG", c)
}

// JSONObjectAgg — JSON_OBJECTAGG(k, v).
func JSONObjectAgg[E any](k, v sorm.AnyCol) sorm.AggExpr[E, string] {
	return sorm.NewAgg[E, string](
		sorm.AggDialect(dialect),
		sorm.AggRaw("JSON_OBJECTAGG("), sorm.AggCol(k), sorm.AggRaw(", "),
		sorm.AggCol(v), sorm.AggRaw(")"),
	)
}

// AnyValue — ANY_VALUE(col): an arbitrary value from the group (lets a
// non-grouped column appear in the select list under ONLY_FULL_GROUP_BY).
func AnyValue[E any, V comparable](c sorm.ColV[V]) sorm.AggExpr[E, V] {
	return agg1[E, V]("ANY_VALUE", c)
}

// Statistical aggregates.
func StdDev[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]     { return agg1[E, float64]("STDDEV", c) }
func StdDevPop[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]  { return agg1[E, float64]("STDDEV_POP", c) }
func StdDevSamp[E any](c sorm.AnyCol) sorm.AggExpr[E, float64] { return agg1[E, float64]("STDDEV_SAMP", c) }
func VarPop[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]     { return agg1[E, float64]("VAR_POP", c) }
func VarSamp[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]    { return agg1[E, float64]("VAR_SAMP", c) }

// Bitwise aggregates.
func BitAnd[E any](c sorm.AnyCol) sorm.AggExpr[E, int64] { return agg1[E, int64]("BIT_AND", c) }
func BitOr[E any](c sorm.AnyCol) sorm.AggExpr[E, int64]  { return agg1[E, int64]("BIT_OR", c) }
func BitXor[E any](c sorm.AnyCol) sorm.AggExpr[E, int64] { return agg1[E, int64]("BIT_XOR", c) }

func agg1[E any, V comparable](fn string, c sorm.AnyCol) sorm.AggExpr[E, V] {
	return sorm.NewAgg[E, V](
		sorm.AggDialect(dialect),
		sorm.AggRaw(fn+"("), sorm.AggCol(c), sorm.AggRaw(")"),
	)
}
