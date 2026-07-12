package sorm

import (
	"context"
	"fmt"
)

// ManyToMany — many-to-many relation through an implicit join table
// (`many2many:join_table` tag). Reads via Include/Any; writes via explicit
// Link/Unlink: sorm does not guess collection diffs, linking is an operation.
type ManyToMany[E, C any] struct {
	joinTable   string
	parentCol   string // join-table column on the E side
	childCol    string // join-table column on the C side
	initSlice   func(*E)
	appendChild func(*E, *C)
}

func NewManyToMany[E, C any](
	joinTable, parentCol, childCol string,
	initSlice func(*E),
	appendChild func(*E, *C),
) ManyToMany[E, C] {
	return ManyToMany[E, C]{joinTable, parentCol, childCol, initSlice, appendChild}
}

// Link associates parent with children (INSERT into the join table). Both
// sides must be persisted. Linking twice yields a ConstraintError (the join
// table's composite PK).
func (r ManyToMany[E, C]) Link(ctx context.Context, db DB, parent *E, children ...*C) error {
	if len(children) == 0 {
		return nil
	}
	pm, cm := metaFor[E](), metaFor[C]()
	d := dialectOf(db)

	sql := "INSERT INTO " + qualifiedTable(d, schemaOf(db), r.joinTable) + " (" +
		d.QuoteIdent(r.parentCol) + ", " + d.QuoteIdent(r.childCol) + ") VALUES "
	args := make([]any, 0, len(children)*2)
	for i, c := range children {
		if i > 0 {
			sql += ", "
		}
		args = append(args, pm.PKValue(parent), cm.PKValue(c))
		sql += "(" + d.Placeholder(len(args)-1) + ", " + d.Placeholder(len(args)) + ")"
	}
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("sorm: link %s: %w", r.joinTable, err)
	}
	return nil
}

// Unlink breaks the links between parent and children (DELETE from the join table).
func (r ManyToMany[E, C]) Unlink(ctx context.Context, db DB, parent *E, children ...*C) error {
	if len(children) == 0 {
		return nil
	}
	pm, cm := metaFor[E](), metaFor[C]()
	w := newSchemaSQLWriter(dialectOf(db), schemaOf(db))
	w.raw("DELETE FROM ")
	w.table(r.joinTable)
	w.raw(" WHERE ")
	w.ident(r.parentCol)
	w.raw(" = ")
	w.arg(pm.PKValue(parent))
	w.raw(" AND ")
	w.ident(r.childCol)
	w.raw(" IN (")
	for i, c := range children {
		if i > 0 {
			w.raw(", ")
		}
		w.arg(cm.PKValue(c))
	}
	w.raw(")")
	if _, err := db.Exec(ctx, w.sb.String(), w.args...); err != nil {
		return fmt.Errorf("sorm: unlink %s: %w", r.joinTable, err)
	}
	return nil
}

// Any filters E by related C: EXISTS over the join table; preds narrow C
// with a correlated subquery.
func (r ManyToMany[E, C]) Any(preds ...Pred[C]) Pred[E] {
	pm, cm := metaFor[E](), metaFor[C]()
	return pred[E](m2mExistsNode{
		joinTable: r.joinTable, parentCol: r.parentCol, childCol: r.childCol,
		parentTable: pm.Table, parentPK: pm.PK,
		childTable: cm.Table, childPK: cm.PK,
		preds: nodesOf(preds),
	})
}

// Include — eager loading of related C: pairs from the join table + loading
// children with a single IN query (chunked), then distribution to parents.
// Order[C] options set the order of children within each parent.
func (r ManyToMany[E, C]) Include(opts ...ChildOpt[C]) IncludeSpec[E] {
	cfg := childConfig(opts)
	return IncludeSpec[E]{load: func(ctx context.Context, db DB, sess *Session, parents []*E) error {
		if len(parents) == 0 {
			return nil
		}
		pm, cm := metaFor[E](), metaFor[C]()

		byKey := make(map[any]*E, len(parents))
		keys := make([]any, 0, len(parents))
		for _, p := range parents {
			k := normalizeKey(pm.PKValue(p))
			if _, dup := byKey[k]; !dup {
				keys = append(keys, k)
			}
			byKey[k] = p
			r.initSlice(p)
		}

		// 1. (parent, child) pairs from the join table
		type pair struct{ p, c any }
		var pairs []pair
		childSeen := map[any]bool{}
		var childKeys []any
		for _, chunk := range chunked(keys) {
			w := newSchemaSQLWriter(dialectOf(db), schemaOf(db))
			w.raw("SELECT ")
			w.ident(r.parentCol)
			w.raw(", ")
			w.ident(r.childCol)
			w.raw(" FROM ")
			w.table(r.joinTable)
			w.raw(" WHERE ")
			w.ident(r.parentCol)
			w.raw(" IN (")
			for i, k := range chunk {
				if i > 0 {
					w.raw(", ")
				}
				w.arg(k)
			}
			w.raw(")")

			rows, err := db.Query(ctx, w.sb.String(), w.args...)
			if err != nil {
				return fmt.Errorf("include %s: %w", r.joinTable, err)
			}
			for rows.Next() {
				var pv, cv any
				if err := rows.Scan(&pv, &cv); err != nil {
					rows.Close()
					return fmt.Errorf("include %s: %w", r.joinTable, err)
				}
				pv, cv = normalizeKey(pv), normalizeKey(cv)
				pairs = append(pairs, pair{pv, cv})
				if !childSeen[cv] {
					childSeen[cv] = true
					childKeys = append(childKeys, cv)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return fmt.Errorf("include %s: %w", r.joinTable, err)
			}
		}
		if len(childKeys) == 0 {
			return nil
		}

		// 2. children in a single IN query (order comes from cfg.orders)
		var childList []*C
		for _, chunk := range chunked(childKeys) {
			cq := Query[C](db).
				Where(pred[C](inNode{colRef{name: cm.PK}, chunk, false})).
				Where(cfg.preds...).
				OrderBy(cfg.orders...)
			cq.sess = sess
			children, err := cq.All(ctx)
			if err != nil {
				return fmt.Errorf("include %s: %w", r.joinTable, err)
			}
			childList = append(childList, children...)
		}

		// 3. distribution: walk children in query order — Order[C] is preserved
		parentsByChild := map[any][]any{}
		for _, pr := range pairs {
			parentsByChild[pr.c] = append(parentsByChild[pr.c], pr.p)
		}
		for _, c := range childList {
			for _, pk := range parentsByChild[normalizeKey(cm.PKValue(c))] {
				r.appendChild(byKey[pk], c)
			}
		}
		return runChildSpecs(ctx, db, sess, cfg.specs, childList)
	}}
}

// normalizeKey brings key values to a comparable form:
// database/sql drivers (MySQL) return strings as []byte.
func normalizeKey(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// m2mExistsNode: EXISTS (SELECT 1 FROM jt WHERE jt.pcol = parent.pk
// [AND jt.ccol IN (SELECT pk FROM children WHERE preds)]).
type m2mExistsNode struct {
	joinTable, parentCol, childCol string
	parentTable, parentPK          string
	childTable, childPK            string
	preds                          []node
}

func (n m2mExistsNode) writeSQL(w *sqlWriter) {
	w.raw("EXISTS (SELECT 1 FROM ")
	w.table(n.joinTable)
	w.raw(" WHERE ")
	w.col(colRef{n.joinTable, n.parentCol})
	w.raw(" = ")
	w.table(n.parentTable)
	w.raw(".")
	w.ident(n.parentPK)
	if len(n.preds) > 0 {
		w.raw(" AND ")
		w.col(colRef{n.joinTable, n.childCol})
		w.raw(" IN (SELECT ")
		w.ident(n.childPK)
		w.raw(" FROM ")
		w.table(n.childTable)
		w.raw(" WHERE ")
		logicalNode{"AND", n.preds}.writeSQL(w)
		w.raw(")")
	}
	w.raw(")")
}
