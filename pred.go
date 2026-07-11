package sorm

import (
	"strings"

	"sorm/dialect"
)

// Pred — иммутабельное условие, параметризованное сущностью: условие по одной
// сущности невозможно подать в запрос по другой (ошибка компиляции).
// agg=true — условие с агрегатом, допустимо только в Having.
type Pred[E any] struct {
	n   node
	agg bool
}

func pred[E any](n node) Pred[E] { return Pred[E]{n: n} }

// And объединяет условия конъюнкцией. And() без аргументов — TRUE.
func And[E any](ps ...Pred[E]) Pred[E] {
	return Pred[E]{n: logicalNode{"AND", nodesOf(ps)}, agg: anyAgg(ps)}
}

// Or объединяет условия дизъюнкцией. Or() без аргументов — FALSE.
func Or[E any](ps ...Pred[E]) Pred[E] {
	return Pred[E]{n: logicalNode{"OR", nodesOf(ps)}, agg: anyAgg(ps)}
}

func Not[E any](p Pred[E]) Pred[E] { return Pred[E]{n: notNode{p.n}, agg: p.agg} }

func nodesOf[E any](ps []Pred[E]) []node {
	ns := make([]node, len(ps))
	for i, p := range ps {
		ns[i] = p.n
	}
	return ns
}

func anyAgg[E any](ps []Pred[E]) bool {
	for _, p := range ps {
		if p.agg {
			return true
		}
	}
	return false
}

// --- AST ---

type node interface{ writeSQL(w *sqlWriter) }

type cmpNode struct {
	ref colRef
	op  string
	val any
}

func (n cmpNode) writeSQL(w *sqlWriter) {
	w.col(n.ref)
	w.raw(" " + n.op + " ")
	w.arg(n.val)
}

type inNode struct {
	ref  colRef
	vals []any
	not  bool
}

func (n inNode) writeSQL(w *sqlWriter) {
	// Пустой IN — не ошибка рантайма, а предсказуемая константа:
	// In() ни с чем не совпадает, NotIn() совпадает со всем.
	if len(n.vals) == 0 {
		if n.not {
			w.raw("TRUE")
		} else {
			w.raw("FALSE")
		}
		return
	}
	w.col(n.ref)
	if n.not {
		w.raw(" NOT")
	}
	w.raw(" IN (")
	for i, v := range n.vals {
		if i > 0 {
			w.raw(", ")
		}
		w.arg(v)
	}
	w.raw(")")
}

type nullNode struct {
	ref colRef
	not bool
}

func (n nullNode) writeSQL(w *sqlWriter) {
	w.col(n.ref)
	if n.not {
		w.raw(" IS NOT NULL")
	} else {
		w.raw(" IS NULL")
	}
}

type betweenNode struct {
	ref    colRef
	lo, hi any
}

func (n betweenNode) writeSQL(w *sqlWriter) {
	w.col(n.ref)
	w.raw(" BETWEEN ")
	w.arg(n.lo)
	w.raw(" AND ")
	w.arg(n.hi)
}

type logicalNode struct {
	op       string // "AND" / "OR"
	children []node
}

func (n logicalNode) writeSQL(w *sqlWriter) {
	if len(n.children) == 0 {
		if n.op == "AND" {
			w.raw("TRUE")
		} else {
			w.raw("FALSE")
		}
		return
	}
	if len(n.children) == 1 {
		n.children[0].writeSQL(w)
		return
	}
	w.raw("(")
	for i, c := range n.children {
		if i > 0 {
			w.raw(" " + n.op + " ")
		}
		c.writeSQL(w)
	}
	w.raw(")")
}

// existsNode — коррелированный подзапрос по связи: внутри него неквалифицированные
// имена колонок разрешаются в дочернюю таблицу (внутренняя область видимости),
// ссылка на родителя квалифицируется явно.
type existsNode struct {
	childTable  string
	fkCol       string
	parentTable string
	parentPK    string
	preds       []node
	not         bool
}

func (n existsNode) writeSQL(w *sqlWriter) {
	if n.not {
		w.raw("NOT ")
	}
	w.raw("EXISTS (SELECT 1 FROM ")
	w.ident(n.childTable)
	w.raw(" WHERE ")
	w.col(colRef{n.childTable, n.fkCol})
	w.raw(" = ")
	w.ident(n.parentTable)
	w.raw(".")
	w.ident(n.parentPK)
	for _, p := range n.preds {
		w.raw(" AND ")
		p.writeSQL(w)
	}
	w.raw(")")
}

type notNode struct{ child node }

func (n notNode) writeSQL(w *sqlWriter) {
	w.raw("NOT (")
	n.child.writeSQL(w)
	w.raw(")")
}

// aggNode — агрегатное выражение: count(*), sum("t"."c") и т.п.
type aggNode struct {
	fn   string
	ref  colRef
	star bool
}

func (n aggNode) writeSQL(w *sqlWriter) {
	w.raw(n.fn + "(")
	if n.star {
		w.raw("*")
	} else {
		w.col(n.ref)
	}
	w.raw(")")
}

// exprCmpNode — сравнение произвольного выражения (агрегата) со значением.
type exprCmpNode struct {
	left node
	op   string
	val  any
}

func (n exprCmpNode) writeSQL(w *sqlWriter) {
	n.left.writeSQL(w)
	w.raw(" " + n.op + " ")
	w.arg(n.val)
}

// --- построение SQL ---

type sqlWriter struct {
	sb      strings.Builder
	d       dialect.Dialect
	args    []any
	qualify bool // проекционный слой с JOIN: имена колонок с таблицей
}

func newSQLWriter(d dialect.Dialect) *sqlWriter { return &sqlWriter{d: d} }

func (w *sqlWriter) raw(s string)   { w.sb.WriteString(s) }
func (w *sqlWriter) ident(s string) { w.sb.WriteString(w.d.QuoteIdent(s)) }

func (w *sqlWriter) col(r colRef) {
	if w.qualify && r.table != "" {
		w.ident(r.table)
		w.raw(".")
	}
	w.ident(r.name)
}

func (w *sqlWriter) arg(v any) {
	w.args = append(w.args, v)
	w.sb.WriteString(w.d.Placeholder(len(w.args)))
}
