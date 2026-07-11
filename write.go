package sorm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"sorm/dialect"
)

// Update — set-based UPDATE без сессии (аналог ExecuteUpdate в EF Core).
// Типобезопасность — на дескрипторах: Set принимает Assign[E] от Col.Set(v).
// Set(false) и Set(0) — полноценные присваивания.
//
// UPDATE без Where — ошибка, если явно не разрешён AllRows() (анти-footgun).
// У версионируемых сущностей автоматически инкрементируется version —
// открытые сессии честно поймают конфликт.
func Update[E any](db DB) UpdateBuilder[E] {
	return UpdateBuilder[E]{db: db, meta: metaFor[E](), d: dialectOf(db)}
}

type UpdateBuilder[E any] struct {
	db      DB
	meta    *Meta[E]
	d       dialect.Dialect
	assigns []Assign[E]
	preds   []Pred[E]
	allRows bool
}

func (q UpdateBuilder[E]) Set(as ...Assign[E]) UpdateBuilder[E] {
	q.assigns = append(slices.Clip(q.assigns), as...)
	return q
}

func (q UpdateBuilder[E]) Where(ps ...Pred[E]) UpdateBuilder[E] {
	q.preds = append(slices.Clip(q.preds), ps...)
	return q
}

// AllRows — явное разрешение выполнить UPDATE по всей таблице.
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
	w := newSQLWriter(q.d)
	w.raw("UPDATE ")
	w.ident(q.meta.Table)
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
	return w.sb.String(), w.args, nil
}

func (q UpdateBuilder[E]) Exec(ctx context.Context) (int64, error) {
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

// Delete — set-based DELETE. Те же правила: без Where нужен AllRows().
func Delete[E any](db DB) DeleteBuilder[E] {
	return DeleteBuilder[E]{db: db, meta: metaFor[E](), d: dialectOf(db)}
}

type DeleteBuilder[E any] struct {
	db      DB
	meta    *Meta[E]
	d       dialect.Dialect
	preds   []Pred[E]
	allRows bool
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
	w := newSQLWriter(q.d)
	w.raw("DELETE FROM ")
	w.ident(q.meta.Table)
	if len(q.preds) > 0 {
		w.raw(" WHERE ")
		logicalNode{"AND", nodesOf(q.preds)}.writeSQL(w)
	}
	return w.sb.String(), w.args, nil
}

func (q DeleteBuilder[E]) Exec(ctx context.Context) (int64, error) {
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
