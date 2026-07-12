package sorm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/dvislobokov/sorm/dialect"
)

// Projection layer: GROUP BY, HAVING, aggregations, arbitrary JOINs.
// The result is not an entity but a named struct R (Go has no anonymous
// types); the column-to-field mapping is the same as RawAs.
// SQL here is always qualified ("users"."id") — after a JOIN column names
// are ambiguous.

// From starts a projection query from the table of entity E.
func From[E any](db DB) FromBuilder[E] {
	m := metaFor[E]()
	return FromBuilder[E]{db: db, d: dialectOf(db), table: m.Table}
}

type FromBuilder[E any] struct {
	db     DB
	d      dialect.Dialect
	table  string
	name   string
	joins  []joinClause
	preds  []Pred[E]
	groups []colRef
	having []Pred[E]
	orders []Order[E]
	limit  *int
	offset *int
	err    error
}

// Named labels the projection for instrumentation (sorm.query.name).
func (q FromBuilder[E]) Named(name string) FromBuilder[E] {
	q.name = name
	return q
}

func (q FromBuilder[E]) Where(ps ...Pred[E]) FromBuilder[E] {
	for _, p := range ps {
		if p.agg {
			q.err = errors.New("sorm: aggregate predicate in Where — use Having")
			return q
		}
	}
	q.preds = append(slices.Clip(q.preds), ps...)
	return q
}

func (q FromBuilder[E]) GroupBy(cols ...ColOf[E]) FromBuilder[E] {
	gs := slices.Clip(q.groups)
	for _, c := range cols {
		gs = append(gs, colRef{c.colTable(), c.ColName()})
	}
	q.groups = gs
	return q
}

func (q FromBuilder[E]) Having(ps ...Pred[E]) FromBuilder[E] {
	q.having = append(slices.Clip(q.having), ps...)
	return q
}

func (q FromBuilder[E]) OrderBy(os ...Order[E]) FromBuilder[E] {
	q.orders = append(slices.Clip(q.orders), os...)
	return q
}

func (q FromBuilder[E]) Limit(n int) FromBuilder[E] {
	q.limit = &n
	return q
}

func (q FromBuilder[E]) Offset(n int) FromBuilder[E] {
	q.offset = &n
	return q
}

// Join adds JOIN specifications (created by relation methods or by the
// free functions LeftJoinOn/InnerJoinOn/CrossJoin).
func (q FromBuilder[E]) Join(specs ...JoinSpec[E]) FromBuilder[E] {
	js := slices.Clip(q.joins)
	for _, s := range specs {
		js = append(js, s.clause)
	}
	q.joins = js
	return q
}

type joinClause struct {
	kind  string // "LEFT JOIN" / "INNER JOIN" / "CROSS JOIN"
	table string
	on    []node // empty for CROSS
}

type JoinSpec[E any] struct{ clause joinClause }

// --- JOIN over a relation: both types are known to the descriptor ---

// LeftJoin is a LEFT JOIN of the child table over the relation's FK; preds are added to ON.
func (r HasMany[E, C]) LeftJoin(preds ...Pred[C]) JoinSpec[E] {
	return r.join("LEFT JOIN", preds)
}

// InnerJoin is an INNER JOIN of the child table over the relation's FK.
func (r HasMany[E, C]) InnerJoin(preds ...Pred[C]) JoinSpec[E] {
	return r.join("INNER JOIN", preds)
}

func (r HasMany[E, C]) join(kind string, preds []Pred[C]) JoinSpec[E] {
	pm, cm := metaFor[E](), metaFor[C]()
	on := []node{joinEqNode{
		left:  colRef{cm.Table, r.fkCol},
		right: colRef{pm.Table, pm.PK},
	}}
	return JoinSpec[E]{joinClause{kind, cm.Table, append(on, nodesOf(preds)...)}}
}

// --- arbitrary JOIN ---

// JoinOn is the join condition between C (being joined) and E (already in the query).
// The column value types must match — enforced by the compiler.
type JoinOn[C, E any] struct{ left, right colRef }

func ColEq[C, E any, V comparable](joined ColOfV[C, V], existing ColOfV[E, V]) JoinOn[C, E] {
	return JoinOn[C, E]{
		left:  colRef{joined.colTable(), joined.ColName()},
		right: colRef{existing.colTable(), existing.ColName()},
	}
}

// LeftJoinOn / InnerJoinOn is an arbitrary JOIN of entity C's table;
// preds on C are added to ON.
func LeftJoinOn[C, E any](on JoinOn[C, E], preds ...Pred[C]) JoinSpec[E] {
	return joinOn("LEFT JOIN", on, preds)
}

func InnerJoinOn[C, E any](on JoinOn[C, E], preds ...Pred[C]) JoinSpec[E] {
	return joinOn("INNER JOIN", on, preds)
}

// CrossJoin is a Cartesian product with entity C's table.
func CrossJoin[C, E any]() JoinSpec[E] {
	return JoinSpec[E]{joinClause{kind: "CROSS JOIN", table: metaFor[C]().Table}}
}

func joinOn[C, E any](kind string, on JoinOn[C, E], preds []Pred[C]) JoinSpec[E] {
	nodes := []node{joinEqNode{on.left, on.right}}
	return JoinSpec[E]{joinClause{kind, metaFor[C]().Table, append(nodes, nodesOf(preds)...)}}
}

type joinEqNode struct{ left, right colRef }

func (n joinEqNode) writeSQL(w *sqlWriter) {
	w.col(n.left)
	w.raw(" = ")
	w.col(n.right)
}

// --- aggregates ---

// AggExpr is an aggregate expression with value type V: comparisons yield
// Pred[E] for Having, As(...) yields a result column.
type AggExpr[E any, V comparable] struct{ n node }

// Aggregates accept a column of ANY entity (E is given explicitly — it is the
// query root): count over a child column after a JOIN is the typical case.
// Table membership is checked when the query is built.

func CountAll[E any]() AggExpr[E, int64] {
	return AggExpr[E, int64]{aggNode{fn: "count", star: true}}
}

func Count[E any](c AnyCol) AggExpr[E, int64] {
	return AggExpr[E, int64]{aggNode{fn: "count", ref: refOf(c)}}
}

func Sum[E any, V comparable](c ColV[V]) AggExpr[E, V] {
	return AggExpr[E, V]{aggNode{fn: "sum", ref: refOf(c)}}
}

func Avg[E any](c AnyCol) AggExpr[E, float64] {
	return AggExpr[E, float64]{aggNode{fn: "avg", ref: refOf(c)}}
}

func Min[E any, V comparable](c ColV[V]) AggExpr[E, V] {
	return AggExpr[E, V]{aggNode{fn: "min", ref: refOf(c)}}
}

func Max[E any, V comparable](c ColV[V]) AggExpr[E, V] {
	return AggExpr[E, V]{aggNode{fn: "max", ref: refOf(c)}}
}

func refOf(c AnyCol) colRef { return colRef{c.colTable(), c.ColName()} }

func (a AggExpr[E, V]) Eq(v V) Pred[E]  { return aggPred[E](exprCmpNode{a.n, "=", v}) }
func (a AggExpr[E, V]) Gt(v V) Pred[E]  { return aggPred[E](exprCmpNode{a.n, ">", v}) }
func (a AggExpr[E, V]) Gte(v V) Pred[E] { return aggPred[E](exprCmpNode{a.n, ">=", v}) }
func (a AggExpr[E, V]) Lt(v V) Pred[E]  { return aggPred[E](exprCmpNode{a.n, "<", v}) }
func (a AggExpr[E, V]) Lte(v V) Pred[E] { return aggPred[E](exprCmpNode{a.n, "<=", v}) }

func aggPred[E any](n node) Pred[E] { return Pred[E]{n: n, agg: true} }

// --- SELECT list ---

// SelectExpr is a column of the projection result.
type SelectExpr[E any] struct {
	n     node
	alias string // result column name (for mapping into R)
}

type colNode struct{ ref colRef }

func (n colNode) writeSQL(w *sqlWriter) { w.col(n.ref) }

// Field is a column of the root entity (E is inferred); the result name = the column name.
func Field[E any](c ColOf[E]) SelectExpr[E] {
	return SelectExpr[E]{n: colNode{refOf(c)}, alias: c.ColName()}
}

// FieldAs is a column with an alias (e.g. on a name collision after a JOIN).
func FieldAs[E any](c ColOf[E], alias string) SelectExpr[E] {
	return SelectExpr[E]{n: colNode{refOf(c)}, alias: alias}
}

// FieldOf is a column of a joined entity ("relaxed mode", E is explicit;
// table membership is validated at build time).
func FieldOf[E any](c AnyCol) SelectExpr[E] {
	return SelectExpr[E]{n: colNode{refOf(c)}, alias: c.ColName()}
}

// FieldOfAs is the same with an alias.
func FieldOfAs[E any](c AnyCol, alias string) SelectExpr[E] {
	return SelectExpr[E]{n: colNode{refOf(c)}, alias: alias}
}

// As is an aggregate with an alias.
func As[E any, V comparable](a AggExpr[E, V], alias string) SelectExpr[E] {
	return SelectExpr[E]{n: a.n, alias: alias}
}

// --- execution ---

// Project performs a projection into struct R. The match between expression
// aliases and R's fields (`sorm:` tag or snake_case) is checked strictly before the query.
func Project[R any, E any](q FromBuilder[E], exprs ...SelectExpr[E]) ProjQuery[R] {
	if q.err != nil {
		return ProjQuery[R]{err: q.err}
	}
	if len(exprs) == 0 {
		return ProjQuery[R]{err: errors.New("sorm: Project without select expressions")}
	}
	plan, err := structPlanFor(reflect.TypeFor[R]())
	if err != nil {
		return ProjQuery[R]{err: err}
	}

	// Membership validation: expression and GROUP BY columns must come from
	// the FROM table or joined tables — a clear error instead of a server-side SQL error.
	allowed := map[string]bool{q.table: true}
	for _, j := range q.joins {
		allowed[j.table] = true
	}
	checkRef := func(r colRef) error {
		if r.table != "" && !allowed[r.table] {
			return fmt.Errorf("sorm: column %s.%s does not belong to FROM/JOIN tables of %s", r.table, r.name, q.table)
		}
		return nil
	}
	for _, ex := range exprs {
		switch n := ex.n.(type) {
		case colNode:
			if err := checkRef(n.ref); err != nil {
				return ProjQuery[R]{err: err}
			}
		case aggNode:
			if !n.star {
				if err := checkRef(n.ref); err != nil {
					return ProjQuery[R]{err: err}
				}
			}
		}
	}
	for _, g := range q.groups {
		if err := checkRef(g); err != nil {
			return ProjQuery[R]{err: err}
		}
	}

	// Strict mapping: every expr goes to a field, every field comes from an expr.
	fieldIdx := make([]int, len(exprs))
	used := map[string]bool{}
	var missing []string
	for i, ex := range exprs {
		idx, ok := plan.byName[ex.alias]
		if !ok {
			missing = append(missing, ex.alias)
			continue
		}
		fieldIdx[i] = plan.fields[idx]
		used[ex.alias] = true
	}
	var extra []string
	for _, name := range plan.names {
		if !used[name] {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		return ProjQuery[R]{err: &ScanError{Missing: missing, Extra: extra}}
	}

	sqlStr, args, err := buildProjection(q, exprs)
	if err != nil {
		return ProjQuery[R]{err: err}
	}
	return ProjQuery[R]{db: q.db, sql: sqlStr, args: args, fieldIdx: fieldIdx, name: q.name}
}

func buildProjection[E any](q FromBuilder[E], exprs []SelectExpr[E]) (string, []any, error) {
	w := newSQLWriter(q.d)
	w.qualify = true

	w.raw("SELECT ")
	for i, ex := range exprs {
		if i > 0 {
			w.raw(", ")
		}
		ex.n.writeSQL(w)
		w.raw(" AS ")
		w.ident(ex.alias)
	}
	w.raw(" FROM ")
	w.ident(q.table)

	for _, j := range q.joins {
		w.raw(" " + j.kind + " ")
		w.ident(j.table)
		if len(j.on) > 0 {
			w.raw(" ON ")
			for i, n := range j.on {
				if i > 0 {
					w.raw(" AND ")
				}
				n.writeSQL(w)
			}
		}
	}
	if len(q.preds) > 0 {
		w.raw(" WHERE ")
		logicalNode{"AND", nodesOf(q.preds)}.writeSQL(w)
	}
	if len(q.groups) > 0 {
		w.raw(" GROUP BY ")
		for i, g := range q.groups {
			if i > 0 {
				w.raw(", ")
			}
			w.col(g)
		}
	}
	if len(q.having) > 0 {
		w.raw(" HAVING ")
		logicalNode{"AND", nodesOf(q.having)}.writeSQL(w)
	}
	if len(q.orders) > 0 {
		w.raw(" ORDER BY ")
		for i, o := range q.orders {
			if i > 0 {
				w.raw(", ")
			}
			w.col(o.ref)
			if o.desc {
				w.raw(" DESC")
			}
		}
	}
	if q.limit != nil {
		w.raw(" LIMIT " + strconv.Itoa(*q.limit))
	}
	if q.offset != nil {
		w.raw(" OFFSET " + strconv.Itoa(*q.offset))
	}
	return w.sb.String(), w.args, w.err
}

type ProjQuery[R any] struct {
	db       DB
	sql      string
	args     []any
	fieldIdx []int
	name     string
	err      error
}

func (q ProjQuery[R]) ToSQL() (string, []any, error) { return q.sql, q.args, q.err }

func (q ProjQuery[R]) All(ctx context.Context) ([]*R, error) {
	if q.err != nil {
		return nil, q.err
	}
	ctx = named(ctx, q.name)
	rows, err := q.db.Query(ctx, q.sql, q.args...)
	if err != nil {
		return nil, fmt.Errorf("sorm: project: %w", err)
	}
	defer rows.Close()

	out := []*R{}
	for rows.Next() {
		r := new(R)
		v := reflect.ValueOf(r).Elem()
		targets := make([]any, len(q.fieldIdx))
		for i, fi := range q.fieldIdx {
			targets[i] = v.Field(fi).Addr().Interface()
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("sorm: project scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q ProjQuery[R]) One(ctx context.Context) (*R, error) {
	all, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNotFound
	}
	return all[0], nil
}
