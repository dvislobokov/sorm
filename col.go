package sorm

import "strings"

// Col — дескриптор колонки со сравнениями на равенство. Тип дескриптора
// (Col/OrdCol/StrCol/BytesCol) выбирает генератор по Go-типу поля.
// Для nullable-полей (*string и т.п.) дескриптор генерируется по base-типу
// плюс IsNull/IsNotNull — предикаты не требуют указателей.
//
// Eq(false) и Eq(0) — полноценные условия: предикаты не знают понятия zero-value.
type Col[E any, V comparable] struct{ name string }

func NewCol[E any, V comparable](name string) Col[E, V] { return Col[E, V]{name} }

// ColName — имя колонки в БД (для генератора и внутренних нужд).
func (c Col[E, V]) ColName() string { return c.name }

func (c Col[E, V]) Eq(v V) Pred[E]  { return Pred[E]{cmpNode{c.name, "=", v}} }
func (c Col[E, V]) Neq(v V) Pred[E] { return Pred[E]{cmpNode{c.name, "<>", v}} }

func (c Col[E, V]) In(vs ...V) Pred[E]    { return Pred[E]{inNode{c.name, box(vs), false}} }
func (c Col[E, V]) NotIn(vs ...V) Pred[E] { return Pred[E]{inNode{c.name, box(vs), true}} }

func (c Col[E, V]) IsNull() Pred[E]    { return Pred[E]{nullNode{c.name, false}} }
func (c Col[E, V]) IsNotNull() Pred[E] { return Pred[E]{nullNode{c.name, true}} }

// Set — типизированное присваивание для set-based Update.
func (c Col[E, V]) Set(v V) Assign[E] { return Assign[E]{col: c.name, val: v} }

func (c Col[E, V]) Asc() Order[E]  { return Order[E]{col: c.name} }
func (c Col[E, V]) Desc() Order[E] { return Order[E]{col: c.name, desc: true} }

// OrdCol добавляет упорядоченные сравнения. Ограничение — comparable, а не
// cmp.Ordered: time.Time упорядочен в SQL, но не Ordered в Go; корректность
// выбора гарантирует генератор (OrdCol только для чисел, строк и time.Time).
type OrdCol[E any, V comparable] struct{ Col[E, V] }

func NewOrdCol[E any, V comparable](name string) OrdCol[E, V] {
	return OrdCol[E, V]{NewCol[E, V](name)}
}

func (c OrdCol[E, V]) Gt(v V) Pred[E]  { return Pred[E]{cmpNode{c.name, ">", v}} }
func (c OrdCol[E, V]) Gte(v V) Pred[E] { return Pred[E]{cmpNode{c.name, ">=", v}} }
func (c OrdCol[E, V]) Lt(v V) Pred[E]  { return Pred[E]{cmpNode{c.name, "<", v}} }
func (c OrdCol[E, V]) Lte(v V) Pred[E] { return Pred[E]{cmpNode{c.name, "<=", v}} }

func (c OrdCol[E, V]) Between(lo, hi V) Pred[E] {
	return Pred[E]{betweenNode{c.name, lo, hi}}
}

// StrCol добавляет строковые предикаты. Аргументы HasPrefix/HasSuffix/Contains —
// литералы, спецсимволы LIKE экранируются; Like/ILike принимают готовый шаблон.
type StrCol[E any] struct{ OrdCol[E, string] }

func NewStrCol[E any](name string) StrCol[E] {
	return StrCol[E]{NewOrdCol[E, string](name)}
}

func (c StrCol[E]) Like(pattern string) Pred[E]  { return Pred[E]{cmpNode{c.name, "LIKE", pattern}} }
func (c StrCol[E]) ILike(pattern string) Pred[E] { return Pred[E]{cmpNode{c.name, "ILIKE", pattern}} }

func (c StrCol[E]) HasPrefix(s string) Pred[E] {
	return Pred[E]{cmpNode{c.name, "LIKE", escapeLike(s) + "%"}}
}

func (c StrCol[E]) HasSuffix(s string) Pred[E] {
	return Pred[E]{cmpNode{c.name, "LIKE", "%" + escapeLike(s)}}
}

func (c StrCol[E]) Contains(s string) Pred[E] {
	return Pred[E]{cmpNode{c.name, "LIKE", "%" + escapeLike(s) + "%"}}
}

// BytesCol — []byte не comparable, поэтому отдельный дескриптор без In
// (SQL `= $1` по bytea допустим, IN по семантике не нужен).
type BytesCol[E any] struct{ name string }

func NewBytesCol[E any](name string) BytesCol[E] { return BytesCol[E]{name} }

func (c BytesCol[E]) ColName() string { return c.name }

func (c BytesCol[E]) Eq(v []byte) Pred[E]  { return Pred[E]{cmpNode{c.name, "=", v}} }
func (c BytesCol[E]) Neq(v []byte) Pred[E] { return Pred[E]{cmpNode{c.name, "<>", v}} }
func (c BytesCol[E]) IsNull() Pred[E]      { return Pred[E]{nullNode{c.name, false}} }
func (c BytesCol[E]) IsNotNull() Pred[E]   { return Pred[E]{nullNode{c.name, true}} }

func (c BytesCol[E]) Set(v []byte) Assign[E] { return Assign[E]{col: c.name, val: v} }

// Order — элемент сортировки, замкнутый по сущности.
type Order[E any] struct {
	col  string
	desc bool
}

// Assign — типизированное присваивание колонки (Update.Set, будущий INSERT DSL).
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
