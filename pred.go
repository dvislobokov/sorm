package sorm

import (
	"strings"

	"sorm/dialect"
)

// Pred — иммутабельное условие, параметризованное сущностью: условие по одной
// сущности невозможно подать в запрос по другой (ошибка компиляции).
type Pred[E any] struct{ n node }

// And объединяет условия конъюнкцией. And() без аргументов — TRUE.
func And[E any](ps ...Pred[E]) Pred[E] { return Pred[E]{logicalNode{"AND", nodesOf(ps)}} }

// Or объединяет условия дизъюнкцией. Or() без аргументов — FALSE.
func Or[E any](ps ...Pred[E]) Pred[E] { return Pred[E]{logicalNode{"OR", nodesOf(ps)}} }

func Not[E any](p Pred[E]) Pred[E] { return Pred[E]{notNode{p.n}} }

func nodesOf[E any](ps []Pred[E]) []node {
	ns := make([]node, len(ps))
	for i, p := range ps {
		ns[i] = p.n
	}
	return ns
}

// --- AST ---

type node interface{ writeSQL(w *sqlWriter) }

type cmpNode struct {
	col, op string
	val     any
}

func (n cmpNode) writeSQL(w *sqlWriter) {
	w.ident(n.col)
	w.raw(" " + n.op + " ")
	w.arg(n.val)
}

type inNode struct {
	col  string
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
	w.ident(n.col)
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
	col string
	not bool
}

func (n nullNode) writeSQL(w *sqlWriter) {
	w.ident(n.col)
	if n.not {
		w.raw(" IS NOT NULL")
	} else {
		w.raw(" IS NULL")
	}
}

type betweenNode struct {
	col    string
	lo, hi any
}

func (n betweenNode) writeSQL(w *sqlWriter) {
	w.ident(n.col)
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

type notNode struct{ child node }

func (n notNode) writeSQL(w *sqlWriter) {
	w.raw("NOT (")
	n.child.writeSQL(w)
	w.raw(")")
}

// --- построение SQL ---

type sqlWriter struct {
	sb   strings.Builder
	d    dialect.Dialect
	args []any
}

func newSQLWriter(d dialect.Dialect) *sqlWriter { return &sqlWriter{d: d} }

func (w *sqlWriter) raw(s string)   { w.sb.WriteString(s) }
func (w *sqlWriter) ident(s string) { w.sb.WriteString(w.d.QuoteIdent(s)) }

func (w *sqlWriter) arg(v any) {
	w.args = append(w.args, v)
	w.sb.WriteString(w.d.Placeholder(len(w.args)))
}
