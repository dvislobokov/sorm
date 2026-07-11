package sorm

import "strings"

// colRef — ссылка на колонку; таблица используется только в квалифицированном
// режиме (проекционный слой с JOIN), энтити-запросы пишут голое имя.
type colRef struct{ table, name string }

// Col — дескриптор колонки со сравнениями на равенство. Тип дескриптора
// (Col/OrdCol/StrCol/BytesCol) выбирает генератор по Go-типу поля.
// Для nullable-полей (*string и т.п.) дескриптор генерируется по base-типу
// плюс IsNull/IsNotNull — предикаты не требуют указателей.
//
// Eq(false) и Eq(0) — полноценные условия: предикаты не знают понятия zero-value.
type Col[E any, V comparable] struct{ ref colRef }

func NewCol[E any, V comparable](table, name string) Col[E, V] {
	return Col[E, V]{colRef{table, name}}
}

// ColName — имя колонки в БД.
func (c Col[E, V]) ColName() string { return c.ref.name }

// colTable + entityMark + valueMark — реализация ColOf[E]/ColOfV[E, V].
func (c Col[E, V]) colTable() string { return c.ref.table }
func (c Col[E, V]) entityMark(*E)    {}
func (c Col[E, V]) valueMark(V)      {}

func (c Col[E, V]) Eq(v V) Pred[E]  { return pred[E](cmpNode{c.ref, "=", v}) }
func (c Col[E, V]) Neq(v V) Pred[E] { return pred[E](cmpNode{c.ref, "<>", v}) }

func (c Col[E, V]) In(vs ...V) Pred[E]    { return pred[E](inNode{c.ref, box(vs), false}) }
func (c Col[E, V]) NotIn(vs ...V) Pred[E] { return pred[E](inNode{c.ref, box(vs), true}) }

func (c Col[E, V]) IsNull() Pred[E]    { return pred[E](nullNode{c.ref, false}) }
func (c Col[E, V]) IsNotNull() Pred[E] { return pred[E](nullNode{c.ref, true}) }

// Set — типизированное присваивание для set-based Update.
func (c Col[E, V]) Set(v V) Assign[E] { return Assign[E]{col: c.ref.name, val: v} }

func (c Col[E, V]) Asc() Order[E]  { return Order[E]{ref: c.ref} }
func (c Col[E, V]) Desc() Order[E] { return Order[E]{ref: c.ref, desc: true} }

// AnyCol — колонка любой сущности. Используется в проекциях после JOIN
// («ослабленный режим»): принадлежность таблицы FROM/JOIN-набору
// валидируется при построении запроса.
type AnyCol interface {
	ColName() string
	colTable() string
}

// ColV — колонка любой сущности с известным типом значения
// (агрегаты и ColEq сохраняют типизацию по V).
type ColV[V comparable] interface {
	AnyCol
	valueMark(V)
}

// ColOf — дескриптор колонки именно сущности E (для Field/GroupBy корневой
// таблицы — с выводом типов).
type ColOf[E any] interface {
	AnyCol
	entityMark(*E)
}

// ColOfV — дескриптор колонки E с типом значения (для типобезопасного ColEq).
type ColOfV[E any, V comparable] interface {
	ColOf[E]
	valueMark(V)
}

// OrdCol добавляет упорядоченные сравнения. Ограничение — comparable, а не
// cmp.Ordered: time.Time упорядочен в SQL, но не Ordered в Go; корректность
// выбора гарантирует генератор (OrdCol только для чисел, строк и time.Time).
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

// StrCol добавляет строковые предикаты. Аргументы HasPrefix/HasSuffix/Contains —
// литералы, спецсимволы LIKE экранируются; Like/ILike принимают готовый шаблон.
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

// BytesCol — []byte не comparable, поэтому отдельный дескриптор без In
// (SQL `= $1` по bytea допустим, IN по семантике не нужен).
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

// Order — элемент сортировки, замкнутый по сущности.
type Order[E any] struct {
	ref  colRef
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
