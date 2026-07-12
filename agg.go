package sorm

import "strings"

// Extension API for aggregate expressions. The portable aggregates
// (Count/Sum/Avg/Min/Max) live in this package; dialect-specific ones —
// string_agg, percentile_cont, GROUP_CONCAT, JSON_ARRAYAGG, … — live in
// the companion packages pgagg (PostgreSQL) and myagg (MySQL), built on
// the primitives below. Third-party packages can do the same.

// AggPart is one fragment of a custom aggregate expression.
type AggPart struct {
	kind    byte // 'r' raw, 'c' col, 'a' arg, 'l' literal, 'd' dialect guard
	raw     string
	col     colRef
	arg     any
	dialect string
}

// AggRaw emits a raw SQL fragment verbatim.
func AggRaw(sql string) AggPart { return AggPart{kind: 'r', raw: sql} }

// AggCol emits a column reference (qualified inside projections).
func AggCol(c AnyCol) AggPart { return AggPart{kind: 'c', col: colRef{c.colTable(), c.ColName()}} }

// AggArg emits a bind-parameter placeholder for v.
func AggArg(v any) AggPart { return AggPart{kind: 'a', arg: v} }

// AggLit emits a safely quoted string literal — for spots where the SQL
// grammar forbids placeholders (e.g. MySQL's GROUP_CONCAT ... SEPARATOR).
func AggLit(s string) AggPart {
	return AggPart{kind: 'l', raw: "'" + strings.ReplaceAll(s, "'", "''") + "'"}
}

// AggDialect guards the expression: rendering on any other dialect is a
// build error returned when the query executes.
func AggDialect(name string) AggPart { return AggPart{kind: 'd', dialect: name} }

// NewAgg assembles a custom aggregate expression from parts. E is the
// root entity of the projection; V is the result type used by Having
// comparisons.
//
//	// string_agg(name, ', ')  — how pgagg.StringAgg is built:
//	sorm.NewAgg[E, string](
//	    sorm.AggDialect("postgres"),
//	    sorm.AggRaw("string_agg("), sorm.AggCol(col), sorm.AggRaw(", "),
//	    sorm.AggArg(", "), sorm.AggRaw(")"),
//	)
func NewAgg[E any, V comparable](parts ...AggPart) AggExpr[E, V] {
	return AggExpr[E, V]{n: rawAggNode{parts}}
}

type rawAggNode struct{ parts []AggPart }

func (n rawAggNode) writeSQL(w *sqlWriter) {
	for _, p := range n.parts {
		switch p.kind {
		case 'r', 'l':
			w.raw(p.raw)
		case 'c':
			w.col(p.col)
		case 'a':
			w.arg(p.arg)
		case 'd':
			if w.d.Name() != p.dialect {
				w.fail("sorm: this aggregate is only supported on " + p.dialect +
					" (current dialect: " + w.d.Name() + ")")
				return
			}
		}
	}
}

// CountDistinct — count(DISTINCT col); portable across all dialects.
func CountDistinct[E any](c AnyCol) AggExpr[E, int64] {
	return NewAgg[E, int64](
		AggRaw("count(DISTINCT "), AggCol(c), AggRaw(")"),
	)
}
