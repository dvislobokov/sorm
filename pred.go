package sorm

import (
	"errors"
	"strings"

	"github.com/dvislobokov/sorm/dialect"
)

// Pred is an immutable condition parameterized by the entity: a condition on
// one entity cannot be passed into a query on another (compile-time error).
// agg=true means the condition contains an aggregate and is only valid in Having.
type Pred[E any] struct {
	n   node
	agg bool
}

func pred[E any](n node) Pred[E] { return Pred[E]{n: n} }

// And combines conditions with a conjunction. And() with no arguments is TRUE.
func And[E any](ps ...Pred[E]) Pred[E] {
	return Pred[E]{n: logicalNode{"AND", nodesOf(ps)}, agg: anyAgg(ps)}
}

// Or combines conditions with a disjunction. Or() with no arguments is FALSE.
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
	// Empty IN is not a runtime error but a predictable constant:
	// In() matches nothing, NotIn() matches everything.
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

// existsNode is a correlated subquery over a relation: inside it, unqualified
// column names resolve to the child table (inner scope), while the reference
// to the parent is qualified explicitly.
type existsNode struct {
	childTable  string
	fkCol       string
	parentTable string
	parentPK    string
	// deletedCol — soft-delete column of the INNER table; the subquery
	// sees alive rows only ("" — no filtering).
	deletedCol string
	preds      []node
	not        bool
}

func (n existsNode) writeSQL(w *sqlWriter) {
	if n.not {
		w.raw("NOT ")
	}
	w.raw("EXISTS (SELECT 1 FROM ")
	w.table(n.childTable)
	w.raw(" WHERE ")
	w.col(colRef{n.childTable, n.fkCol})
	w.raw(" = ")
	w.table(n.parentTable)
	w.raw(".")
	w.ident(n.parentPK)
	if n.deletedCol != "" {
		w.raw(" AND ")
		w.col(colRef{n.childTable, n.deletedCol})
		w.raw(" IS NULL")
	}
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

// aggNode is an aggregate expression: count(*), sum("t"."c"), etc.
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

// exprCmpNode compares an arbitrary expression (an aggregate) with a value.
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

// --- SQL generation ---

type sqlWriter struct {
	sb      strings.Builder
	d       dialect.Dialect
	args    []any
	qualify bool   // projection layer with JOINs: column names prefixed with the table
	schema  string // non-empty: table names render schema-qualified (InSchema)
	err     error
}

func newSQLWriter(d dialect.Dialect) *sqlWriter { return &sqlWriter{d: d} }

func newSchemaSQLWriter(d dialect.Dialect, schema string) *sqlWriter {
	return &sqlWriter{d: d, schema: schema}
}

// table renders a table name, schema-qualified when the connection is
// wrapped with InSchema: "billing"."orders".
func (w *sqlWriter) table(name string) {
	if w.schema != "" {
		w.ident(w.schema)
		w.raw(".")
	}
	w.ident(name)
}

// fail records a build error (e.g. a predicate unsupported on this dialect).
// Executing methods surface it instead of sending broken SQL to the database.
func (w *sqlWriter) fail(msg string) {
	if w.err == nil {
		w.err = errors.New(msg)
	}
}

func (w *sqlWriter) raw(s string)   { w.sb.WriteString(s) }
func (w *sqlWriter) ident(s string) { w.sb.WriteString(w.d.QuoteIdent(s)) }

func (w *sqlWriter) col(r colRef) {
	if w.qualify && r.table != "" {
		w.table(r.table)
		w.raw(".")
	}
	w.ident(r.name)
}

func (w *sqlWriter) arg(v any) {
	w.args = append(w.args, v)
	w.sb.WriteString(w.d.Placeholder(len(w.args)))
}

// qualifiedTable renders a table name with an optional schema prefix —
// for SQL built outside a sqlWriter.
func qualifiedTable(d dialect.Dialect, schema, table string) string {
	if schema == "" {
		return d.QuoteIdent(table)
	}
	return d.QuoteIdent(schema) + "." + d.QuoteIdent(table)
}