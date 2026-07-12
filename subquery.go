package sorm

// Typed subqueries. SubQ[V] is a subquery producing values of type V —
// the compiler will not let an int64 subquery meet a string column.
// Arguments of the outer query and the subquery share one placeholder
// sequence: everything renders into a single writer.
//
//	// users who authored a flagged post: id IN (SELECT author_id FROM posts WHERE ...)
//	flagged := sorm.Pick(p.AuthorID, sorm.Query[models.Post](nil).Where(p.Flagged.Eq(true)))
//	users, err := sorm.Query[models.User](db).Where(sorm.InQuery(u.ID, flagged)).All(ctx)
//
//	// balance above the average: balance > (SELECT avg(balance) FROM users)
//	avg := sorm.PickScalar(sorm.Avg[models.User](u.Balance), sorm.Query[models.User](nil))
//	rich, err := sorm.Query[models.User](db).Where(sorm.GtQ(u.Balance, avg)).All(ctx)
//
// The inner builder is usually created with a nil db — rendering is
// dialect-driven by the OUTER query, so the same subquery value works on
// any connection. Correlated subqueries (referencing the outer row) are
// covered by relation predicates (Any/None/Is); Pick/PickScalar are for
// самостоятельные подзапросы.
type SubQ[V comparable] struct {
	write func(w *sqlWriter)
}

// Pick selects a single typed column from a query — the IN-subquery shape.
// ORDER BY/LIMIT/OFFSET of the builder are preserved if set.
func Pick[E any, V comparable](c ColOfV[E, V], q QueryBuilder[E]) SubQ[V] {
	col := c.ColName()
	return SubQ[V]{write: func(w *sqlWriter) {
		q.writeSelect(w, w.d.QuoteIdent(col))
	}}
}

// PickScalar selects a single aggregate value — the scalar-subquery
// shape for comparisons: avg, max, count(*), ...
func PickScalar[E any, V comparable](a AggExpr[E, V], q QueryBuilder[E]) SubQ[V] {
	return SubQ[V]{write: func(w *sqlWriter) {
		// Render the aggregate expression with the outer writer first —
		// its SQL becomes the select list of the embedded query.
		inner := &sqlWriter{d: w.d, qualify: w.qualify, schema: w.schema}
		inner.args = w.args // continue the shared numbering
		a.n.writeSQL(inner)
		if inner.err != nil {
			w.fail(inner.err.Error())
			return
		}
		w.args = inner.args
		q.writeSelect(w, inner.sb.String())
	}}
}

// InQuery — col IN (SELECT ...). Value types must match.
func InQuery[E any, V comparable](c ColOfV[E, V], sub SubQ[V]) Pred[E] {
	return pred[E](subCmpNode{ref: colRef{c.colTable(), c.ColName()}, op: "IN", sub: sub.write})
}

// NotInQuery — col NOT IN (SELECT ...). Beware NULLs in the subquery:
// SQL's NOT IN over a set containing NULL matches nothing.
func NotInQuery[E any, V comparable](c ColOfV[E, V], sub SubQ[V]) Pred[E] {
	return pred[E](subCmpNode{ref: colRef{c.colTable(), c.ColName()}, op: "NOT IN", sub: sub.write})
}

// Scalar comparisons: col <op> (SELECT ...). The subquery must yield a
// single row (usually an aggregate) — more rows is a database error.
func EqQ[E any, V comparable](c ColOfV[E, V], sub SubQ[V]) Pred[E]  { return subCmp(c, "=", sub) }
func NeqQ[E any, V comparable](c ColOfV[E, V], sub SubQ[V]) Pred[E] { return subCmp(c, "<>", sub) }
func GtQ[E any, V comparable](c ColOfV[E, V], sub SubQ[V]) Pred[E]  { return subCmp(c, ">", sub) }
func GteQ[E any, V comparable](c ColOfV[E, V], sub SubQ[V]) Pred[E] { return subCmp(c, ">=", sub) }
func LtQ[E any, V comparable](c ColOfV[E, V], sub SubQ[V]) Pred[E]  { return subCmp(c, "<", sub) }
func LteQ[E any, V comparable](c ColOfV[E, V], sub SubQ[V]) Pred[E] { return subCmp(c, "<=", sub) }

func subCmp[E any, V comparable](c ColOfV[E, V], op string, sub SubQ[V]) Pred[E] {
	return pred[E](subCmpNode{ref: colRef{c.colTable(), c.ColName()}, op: op, sub: sub.write})
}

// subCmpNode renders `col op (SELECT ...)`.
type subCmpNode struct {
	ref colRef
	op  string
	sub func(w *sqlWriter)
}

func (n subCmpNode) writeSQL(w *sqlWriter) {
	w.col(n.ref)
	w.raw(" " + n.op + " (")
	n.sub(w)
	w.raw(")")
}
