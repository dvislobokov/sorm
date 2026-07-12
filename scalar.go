package sorm

import "database/sql/driver"

// ScalarSnapshot normalizes a custom scalar for snapshot diffing: the
// driver.Value is comparable after []byte → string (driver.Value is one
// of nil, bool, int64, float64, string, time.Time, []byte). Used by
// generated snapshot/diff code — the Go type itself does not have to be
// comparable (decimal.Decimal is not).
func ScalarSnapshot(v driver.Valuer) any {
	val, err := v.Value()
	if err != nil {
		// A value that cannot serialize compares unequal to everything —
		// the flush surfaces the same error when the INSERT/UPDATE runs.
		return "sorm: scalar snapshot error: " + err.Error()
	}
	if b, ok := val.([]byte); ok {
		return string(b)
	}
	return val
}

// ScalarCol is the descriptor of a custom-scalar column: a named Go type
// implementing driver.Valuer + sql.Scanner (decimal.Decimal, money types,
// encrypted strings). V is intentionally unconstrained — the type does not
// have to be Go-comparable; every comparison happens in SQL, where the
// column's real type (numeric, ...) is ordered.
type ScalarCol[E any, V any] struct{ ref colRef }

func NewScalarCol[E any, V any](table, name string) ScalarCol[E, V] {
	return ScalarCol[E, V]{colRef{table, name}}
}

func (c ScalarCol[E, V]) ColName() string  { return c.ref.name }
func (c ScalarCol[E, V]) colTable() string { return c.ref.table }
func (c ScalarCol[E, V]) entityMark(*E)    {}

func (c ScalarCol[E, V]) Eq(v V) Pred[E]  { return pred[E](cmpNode{c.ref, "=", v}) }
func (c ScalarCol[E, V]) Neq(v V) Pred[E] { return pred[E](cmpNode{c.ref, "<>", v}) }
func (c ScalarCol[E, V]) Gt(v V) Pred[E]  { return pred[E](cmpNode{c.ref, ">", v}) }
func (c ScalarCol[E, V]) Gte(v V) Pred[E] { return pred[E](cmpNode{c.ref, ">=", v}) }
func (c ScalarCol[E, V]) Lt(v V) Pred[E]  { return pred[E](cmpNode{c.ref, "<", v}) }
func (c ScalarCol[E, V]) Lte(v V) Pred[E] { return pred[E](cmpNode{c.ref, "<=", v}) }

func (c ScalarCol[E, V]) In(vs ...V) Pred[E]    { return pred[E](inNode{c.ref, box(vs), false}) }
func (c ScalarCol[E, V]) NotIn(vs ...V) Pred[E] { return pred[E](inNode{c.ref, box(vs), true}) }

func (c ScalarCol[E, V]) IsNull() Pred[E]    { return pred[E](nullNode{c.ref, false}) }
func (c ScalarCol[E, V]) IsNotNull() Pred[E] { return pred[E](nullNode{c.ref, true}) }

func (c ScalarCol[E, V]) Set(v V) Assign[E]  { return Assign[E]{col: c.ref.name, val: v} }
func (c ScalarCol[E, V]) SetNull() Assign[E] { return Assign[E]{col: c.ref.name, val: nil} }

func (c ScalarCol[E, V]) Asc() Order[E]  { return Order[E]{ref: c.ref} }
func (c ScalarCol[E, V]) Desc() Order[E] { return Order[E]{ref: c.ref, desc: true} }

// ArrayCol is the descriptor of a native PostgreSQL array column
// (`sorm:"array"` on []string, []int64, ...). Array predicates render
// PG operators and are guarded: building them on another dialect is a
// build error returned when the query executes. A nil slice maps to NULL.
type ArrayCol[E any, V comparable] struct{ ref colRef }

func NewArrayCol[E any, V comparable](table, name string) ArrayCol[E, V] {
	return ArrayCol[E, V]{colRef{table, name}}
}

func (c ArrayCol[E, V]) ColName() string  { return c.ref.name }
func (c ArrayCol[E, V]) colTable() string { return c.ref.table }
func (c ArrayCol[E, V]) entityMark(*E)    {}

// Contains — col @> ARRAY[vs]: the column contains every listed element.
func (c ArrayCol[E, V]) Contains(vs ...V) Pred[E] {
	return pred[E](arrayCmpNode{c.ref, "@>", box(vs)})
}

// Overlaps — col && ARRAY[vs]: the column shares at least one element.
func (c ArrayCol[E, V]) Overlaps(vs ...V) Pred[E] {
	return pred[E](arrayCmpNode{c.ref, "&&", box(vs)})
}

// Has — a single-element Contains, reads better for the common case.
func (c ArrayCol[E, V]) Has(v V) Pred[E] {
	return pred[E](arrayCmpNode{c.ref, "@>", []any{v}})
}

func (c ArrayCol[E, V]) IsNull() Pred[E]    { return pred[E](nullNode{c.ref, false}) }
func (c ArrayCol[E, V]) IsNotNull() Pred[E] { return pred[E](nullNode{c.ref, true}) }

func (c ArrayCol[E, V]) Set(vs []V) Assign[E] { return Assign[E]{col: c.ref.name, val: vs} }
func (c ArrayCol[E, V]) SetNull() Assign[E]   { return Assign[E]{col: c.ref.name, val: nil} }

// arrayCmpNode renders `col op ARRAY[$1, $2, ...]` — PostgreSQL only.
type arrayCmpNode struct {
	ref colRef
	op  string
	vs  []any
}

func (n arrayCmpNode) writeSQL(w *sqlWriter) {
	if w.d.Name() != "postgres" {
		w.fail("sorm: array predicates are only supported on postgres (current dialect: " + w.d.Name() + ")")
		return
	}
	w.col(n.ref)
	w.raw(" " + n.op + " ARRAY[")
	for i, v := range n.vs {
		if i > 0 {
			w.raw(", ")
		}
		w.arg(v)
	}
	w.raw("]")
}
