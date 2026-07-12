package sorm

import "strings"

// colRef is a column reference; the table is used only in qualified mode
// (the projection layer with JOINs), entity queries emit the bare name.
type colRef struct{ table, name string }

// Col is a column descriptor with equality comparisons. The descriptor type
// (Col/OrdCol/StrCol/BytesCol) is chosen by the generator based on the field's Go type.
// For nullable fields (*string etc.) the descriptor is generated for the base type
// plus IsNull/IsNotNull — predicates do not require pointers.
//
// Eq(false) and Eq(0) are full-fledged conditions: predicates have no notion of a zero value.
type Col[E any, V comparable] struct{ ref colRef }

func NewCol[E any, V comparable](table, name string) Col[E, V] {
	return Col[E, V]{colRef{table, name}}
}

// ColName is the column name in the database.
func (c Col[E, V]) ColName() string { return c.ref.name }

// colTable + entityMark + valueMark implement ColOf[E]/ColOfV[E, V].
func (c Col[E, V]) colTable() string { return c.ref.table }
func (c Col[E, V]) entityMark(*E)    {}
func (c Col[E, V]) valueMark(V)      {}

func (c Col[E, V]) Eq(v V) Pred[E]  { return pred[E](cmpNode{c.ref, "=", v}) }
func (c Col[E, V]) Neq(v V) Pred[E] { return pred[E](cmpNode{c.ref, "<>", v}) }

func (c Col[E, V]) In(vs ...V) Pred[E]    { return pred[E](inNode{c.ref, box(vs), false}) }
func (c Col[E, V]) NotIn(vs ...V) Pred[E] { return pred[E](inNode{c.ref, box(vs), true}) }

func (c Col[E, V]) IsNull() Pred[E]    { return pred[E](nullNode{c.ref, false}) }
func (c Col[E, V]) IsNotNull() Pred[E] { return pred[E](nullNode{c.ref, true}) }

// Set is a typed assignment for set-based Update.
func (c Col[E, V]) Set(v V) Assign[E] { return Assign[E]{col: c.ref.name, val: v} }

// SetNull assigns SQL NULL (for nullable columns; a NOT NULL column will
// come back as a typed ConstraintError from the database).
func (c Col[E, V]) SetNull() Assign[E] { return Assign[E]{col: c.ref.name, val: nil} }

func (c Col[E, V]) Asc() Order[E]  { return Order[E]{ref: c.ref} }
func (c Col[E, V]) Desc() Order[E] { return Order[E]{ref: c.ref, desc: true} }

// AnyCol is a column of any entity. Used in projections after a JOIN
// ("relaxed mode"): table membership in the FROM/JOIN set is validated
// when the query is built.
type AnyCol interface {
	ColName() string
	colTable() string
}

// ColV is a column of any entity with a known value type
// (aggregates and ColEq stay typed over V).
type ColV[V comparable] interface {
	AnyCol
	valueMark(V)
}

// ColOf is a column descriptor of exactly entity E (for Field/GroupBy on the
// root table — with type inference).
type ColOf[E any] interface {
	AnyCol
	entityMark(*E)
}

// ColOfV is a column descriptor of E with a value type (for type-safe ColEq).
type ColOfV[E any, V comparable] interface {
	ColOf[E]
	valueMark(V)
}

// OrdCol adds ordered comparisons. The constraint is comparable, not
// cmp.Ordered: time.Time is ordered in SQL but not Ordered in Go; the generator
// guarantees correct selection (OrdCol only for numbers, strings, and time.Time).
type OrdCol[E any, V comparable] struct{ Col[E, V] }

func NewOrdCol[E any, V comparable](table, name string) OrdCol[E, V] {
	return OrdCol[E, V]{NewCol[E, V](table, name)}
}

func (c OrdCol[E, V]) Gt(v V) Pred[E]  { return pred[E](cmpNode{c.ref, ">", v}) }
func (c OrdCol[E, V]) Gte(v V) Pred[E] { return pred[E](cmpNode{c.ref, ">=", v}) }
func (c OrdCol[E, V]) Lt(v V) Pred[E]  { return pred[E](cmpNode{c.ref, "<", v}) }
func (c OrdCol[E, V]) Lte(v V) Pred[E] { return pred[E](cmpNode{c.ref, "<=", v}) }

func (c OrdCol[E, V]) Between(lo, hi V) Pred[E] {
	return pred[E](betweenNode{c.ref, lo, hi})
}

// StrCol adds string predicates. HasPrefix/HasSuffix/Contains arguments are
// literals, LIKE special characters are escaped; Like/ILike take a ready-made pattern.
type StrCol[E any] struct{ OrdCol[E, string] }

func NewStrCol[E any](table, name string) StrCol[E] {
	return StrCol[E]{NewOrdCol[E, string](table, name)}
}

func (c StrCol[E]) Like(pattern string) Pred[E]  { return pred[E](cmpNode{c.ref, "LIKE", pattern}) }
func (c StrCol[E]) ILike(pattern string) Pred[E] { return pred[E](cmpNode{c.ref, "ILIKE", pattern}) }

func (c StrCol[E]) HasPrefix(s string) Pred[E] {
	return pred[E](cmpNode{c.ref, "LIKE", escapeLike(s) + "%"})
}

func (c StrCol[E]) HasSuffix(s string) Pred[E] {
	return pred[E](cmpNode{c.ref, "LIKE", "%" + escapeLike(s)})
}

func (c StrCol[E]) Contains(s string) Pred[E] {
	return pred[E](cmpNode{c.ref, "LIKE", "%" + escapeLike(s) + "%"})
}

// BytesCol: []byte is not comparable, hence a separate descriptor without In
// (SQL `= $1` on bytea is valid; IN is semantically unnecessary).
type BytesCol[E any] struct{ ref colRef }

func NewBytesCol[E any](table, name string) BytesCol[E] {
	return BytesCol[E]{colRef{table, name}}
}

func (c BytesCol[E]) ColName() string  { return c.ref.name }
func (c BytesCol[E]) colTable() string { return c.ref.table }
func (c BytesCol[E]) entityMark(*E)    {}

func (c BytesCol[E]) Eq(v []byte) Pred[E]  { return pred[E](cmpNode{c.ref, "=", v}) }
func (c BytesCol[E]) Neq(v []byte) Pred[E] { return pred[E](cmpNode{c.ref, "<>", v}) }
func (c BytesCol[E]) IsNull() Pred[E]      { return pred[E](nullNode{c.ref, false}) }
func (c BytesCol[E]) IsNotNull() Pred[E]   { return pred[E](nullNode{c.ref, true}) }

func (c BytesCol[E]) Set(v []byte) Assign[E] { return Assign[E]{col: c.ref.name, val: v} }

// SetNull assigns SQL NULL.
func (c BytesCol[E]) SetNull() Assign[E] { return Assign[E]{col: c.ref.name, val: nil} }

// Order is a sort element, closed over the entity.
type Order[E any] struct {
	ref  colRef
	desc bool
}

// Assign is a typed column assignment (Update.Set, future INSERT DSL).
type Assign[E any] struct {
	col string
	val any
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLike(s string) string { return likeEscaper.Replace(s) }

func box[V any](vs []V) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}
