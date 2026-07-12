// Package pgagg provides PostgreSQL-specific aggregate functions for sorm
// projections. Using them on another dialect is a build error returned
// when the query executes — never silently wrong SQL.
//
//	rows, err := sorm.Project[stat](
//	    sorm.From[models.User](db).GroupBy(u.Country),
//	    sorm.Field(u.Country),
//	    sorm.As(pgagg.StringAgg[models.User](u.Name, ", "), "names"),
//	    sorm.As(pgagg.PercentileCont[models.User](0.5, u.Age), "median_age"),
//	).All(ctx)
package pgagg

import (
	"github.com/dvislobokov/sorm"
)

const dialect = "postgres"

// StringAgg — string_agg(col, sep): concatenates values with a separator.
func StringAgg[E any](c sorm.AnyCol, sep string) sorm.AggExpr[E, string] {
	return sorm.NewAgg[E, string](
		sorm.AggDialect(dialect),
		sorm.AggRaw("string_agg("), sorm.AggCol(c), sorm.AggRaw("::text, "),
		sorm.AggArg(sep), sorm.AggRaw(")"),
	)
}

// ArrayAgg — array_agg(col).
func ArrayAgg[E any](c sorm.AnyCol) sorm.AggExpr[E, string] {
	return agg1[E, string]("array_agg", c)
}

// JSONBAgg — jsonb_agg(col): the group as a JSONB array.
func JSONBAgg[E any](c sorm.AnyCol) sorm.AggExpr[E, string] {
	return agg1[E, string]("jsonb_agg", c)
}

// JSONBObjectAgg — jsonb_object_agg(k, v).
func JSONBObjectAgg[E any](k, v sorm.AnyCol) sorm.AggExpr[E, string] {
	return sorm.NewAgg[E, string](
		sorm.AggDialect(dialect),
		sorm.AggRaw("jsonb_object_agg("), sorm.AggCol(k), sorm.AggRaw(", "),
		sorm.AggCol(v), sorm.AggRaw(")"),
	)
}

// BoolAnd — bool_and(col): true when every value is true.
func BoolAnd[E any](c sorm.AnyCol) sorm.AggExpr[E, bool] {
	return agg1[E, bool]("bool_and", c)
}

// BoolOr — bool_or(col): true when at least one value is true.
func BoolOr[E any](c sorm.AnyCol) sorm.AggExpr[E, bool] {
	return agg1[E, bool]("bool_or", c)
}

// BitAnd / BitOr — bitwise aggregate over integer values.
func BitAnd[E any](c sorm.AnyCol) sorm.AggExpr[E, int64] { return agg1[E, int64]("bit_and", c) }
func BitOr[E any](c sorm.AnyCol) sorm.AggExpr[E, int64]  { return agg1[E, int64]("bit_or", c) }

// Statistical aggregates.
func StdDev[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]     { return agg1[E, float64]("stddev", c) }
func StdDevPop[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]  { return agg1[E, float64]("stddev_pop", c) }
func StdDevSamp[E any](c sorm.AnyCol) sorm.AggExpr[E, float64] { return agg1[E, float64]("stddev_samp", c) }
func Variance[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]   { return agg1[E, float64]("variance", c) }
func VarPop[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]     { return agg1[E, float64]("var_pop", c) }
func VarSamp[E any](c sorm.AnyCol) sorm.AggExpr[E, float64]    { return agg1[E, float64]("var_samp", c) }

// Corr — corr(y, x): the correlation coefficient.
func Corr[E any](y, x sorm.AnyCol) sorm.AggExpr[E, float64] { return agg2[E]("corr", y, x) }

// CovarPop / CovarSamp — covariance.
func CovarPop[E any](y, x sorm.AnyCol) sorm.AggExpr[E, float64]  { return agg2[E]("covar_pop", y, x) }
func CovarSamp[E any](y, x sorm.AnyCol) sorm.AggExpr[E, float64] { return agg2[E]("covar_samp", y, x) }

// PercentileCont — percentile_cont(fraction) WITHIN GROUP (ORDER BY col):
// continuous percentile (0.5 = median, interpolated).
func PercentileCont[E any](fraction float64, orderBy sorm.AnyCol) sorm.AggExpr[E, float64] {
	return withinGroup[E]("percentile_cont", fraction, orderBy)
}

// PercentileDisc — percentile_disc(fraction) WITHIN GROUP (ORDER BY col):
// discrete percentile (an actual value from the set).
func PercentileDisc[E any](fraction float64, orderBy sorm.AnyCol) sorm.AggExpr[E, float64] {
	return withinGroup[E]("percentile_disc", fraction, orderBy)
}

// Mode — mode() WITHIN GROUP (ORDER BY col): the most frequent value.
func Mode[E any](orderBy sorm.AnyCol) sorm.AggExpr[E, string] {
	return sorm.NewAgg[E, string](
		sorm.AggDialect(dialect),
		sorm.AggRaw("mode() WITHIN GROUP (ORDER BY "), sorm.AggCol(orderBy), sorm.AggRaw(")"),
	)
}

func agg1[E any, V comparable](fn string, c sorm.AnyCol) sorm.AggExpr[E, V] {
	return sorm.NewAgg[E, V](
		sorm.AggDialect(dialect),
		sorm.AggRaw(fn+"("), sorm.AggCol(c), sorm.AggRaw(")"),
	)
}

func agg2[E any](fn string, a, b sorm.AnyCol) sorm.AggExpr[E, float64] {
	return sorm.NewAgg[E, float64](
		sorm.AggDialect(dialect),
		sorm.AggRaw(fn+"("), sorm.AggCol(a), sorm.AggRaw(", "), sorm.AggCol(b), sorm.AggRaw(")"),
	)
}

func withinGroup[E any](fn string, fraction float64, orderBy sorm.AnyCol) sorm.AggExpr[E, float64] {
	return sorm.NewAgg[E, float64](
		sorm.AggDialect(dialect),
		sorm.AggRaw(fn+"("), sorm.AggArg(fraction),
		sorm.AggRaw(") WITHIN GROUP (ORDER BY "), sorm.AggCol(orderBy), sorm.AggRaw(")"),
	)
}
