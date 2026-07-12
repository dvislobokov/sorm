package sorm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/dvislobokov/sorm/dialect"
)

// Update is a set-based UPDATE without a session (analogous to ExecuteUpdate in EF Core).
// Type safety comes from descriptors: Set takes an Assign[E] from Col.Set(v).
// Set(false) and Set(0) are full-fledged assignments.
//
// UPDATE without Where is an error unless explicitly allowed via AllRows() (anti-footgun).
// For versioned entities the version column is incremented automatically —
// open sessions will properly catch the conflict.
func Update[E any](db DB) UpdateBuilder[E] {
	return UpdateBuilder[E]{db: db, meta: metaFor[E](), d: dialectOf(db), schema: schemaOf(db)}
}

type UpdateBuilder[E any] struct {
	db      DB
	meta    *Meta[E]
	d       dialect.Dialect
	schema  string
	assigns []Assign[E]
	preds   []Pred[E]
	name    string
	allRows bool
}

// Named labels the statement for instrumentation (sorm.query.name).
func (q UpdateBuilder[E]) Named(name string) UpdateBuilder[E] {
	q.name = name
	return q
}

func (q UpdateBuilder[E]) Set(as ...Assign[E]) UpdateBuilder[E] {
	q.assigns = append(slices.Clip(q.assigns), as...)
	return q
}

func (q UpdateBuilder[E]) Where(ps ...Pred[E]) UpdateBuilder[E] {
	q.preds = append(slices.Clip(q.preds), ps...)
	return q
}

// AllRows explicitly allows an UPDATE over the whole table.
func (q UpdateBuilder[E]) AllRows() UpdateBuilder[E] {
	q.allRows = true
	return q
}

func (q UpdateBuilder[E]) ToSQL() (string, []any, error) {
	if len(q.assigns) == 0 {
		return "", nil, errors.New("sorm: update without Set")
	}
	if len(q.preds) == 0 && !q.allRows {
		return "", nil, errors.New("sorm: update without Where — use AllRows() to update the whole table")
	}
	w := newSchemaSQLWriter(q.d, q.schema)
	w.raw("UPDATE ")
	w.table(q.meta.Table)
	w.raw(" SET ")
	for i, a := range q.assigns {
		if i > 0 {
			w.raw(", ")
		}
		w.ident(a.col)
		w.raw(" = ")
		w.arg(a.val)
	}
	if q.meta.VersionCol != "" {
		w.raw(", ")
		w.ident(q.meta.VersionCol)
		w.raw(" = ")
		w.ident(q.meta.VersionCol)
		w.raw(" + 1")
	}
	if len(q.preds) > 0 {
		w.raw(" WHERE ")
		logicalNode{"AND", nodesOf(q.preds)}.writeSQL(w)
	}
	return w.sb.String(), w.args, w.err
}

func (q UpdateBuilder[E]) Exec(ctx context.Context) (int64, error) {
	ctx = named(ctx, q.name)
	sqlStr, args, err := q.ToSQL()
	if err != nil {
		return 0, err
	}
	n, err := q.db.Exec(ctx, sqlStr, args...)
	if err != nil {
		return 0, fmt.Errorf("sorm: update %s: %w", q.meta.Table, err)
	}
	return n, nil
}

// Delete is a set-based DELETE. Same rules: without Where, AllRows() is required.
func Delete[E any](db DB) DeleteBuilder[E] {
	return DeleteBuilder[E]{db: db, meta: metaFor[E](), d: dialectOf(db), schema: schemaOf(db)}
}

type DeleteBuilder[E any] struct {
	db      DB
	meta    *Meta[E]
	d       dialect.Dialect
	schema  string
	preds   []Pred[E]
	name    string
	allRows bool
}

// Named labels the statement for instrumentation (sorm.query.name).
func (q DeleteBuilder[E]) Named(name string) DeleteBuilder[E] {
	q.name = name
	return q
}

func (q DeleteBuilder[E]) Where(ps ...Pred[E]) DeleteBuilder[E] {
	q.preds = append(slices.Clip(q.preds), ps...)
	return q
}

func (q DeleteBuilder[E]) AllRows() DeleteBuilder[E] {
	q.allRows = true
	return q
}

func (q DeleteBuilder[E]) ToSQL() (string, []any, error) {
	if len(q.preds) == 0 && !q.allRows {
		return "", nil, errors.New("sorm: delete without Where — use AllRows() to delete the whole table")
	}
	w := newSchemaSQLWriter(q.d, q.schema)
	w.raw("DELETE FROM ")
	w.table(q.meta.Table)
	if len(q.preds) > 0 {
		w.raw(" WHERE ")
		logicalNode{"AND", nodesOf(q.preds)}.writeSQL(w)
	}
	return w.sb.String(), w.args, w.err
}

func (q DeleteBuilder[E]) Exec(ctx context.Context) (int64, error) {
	ctx = named(ctx, q.name)
	sqlStr, args, err := q.ToSQL()
	if err != nil {
		return 0, err
	}
	n, err := q.db.Exec(ctx, sqlStr, args...)
	if err != nil {
		return 0, fmt.Errorf("sorm: delete %s: %w", q.meta.Table, err)
	}
	return n, nil
}
