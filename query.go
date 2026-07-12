package sorm

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strconv"

	"github.com/dvislobokov/sorm/dialect"
	"github.com/dvislobokov/sorm/dialect/pg"
)

// defaultDialect is a fallback for inspecting SQL without a connection (Query[E](nil).ToSQL()).
var defaultDialect dialect.Dialect = pg.Dialect{}

func dialectOf(db DB) dialect.Dialect {
	if db == nil {
		return defaultDialect
	}
	return db.Dialect()
}

// Query starts a typed query over entity E.
// The builder is immutable: every method returns a copy, so reusing a base
// builder is safe (no state leaks between queries).
func Query[E any](db DB) QueryBuilder[E] {
	return QueryBuilder[E]{db: db, meta: metaFor[E](), d: dialectOf(db)}
}

type QueryBuilder[E any] struct {
	db       DB
	meta     *Meta[E]
	d        dialect.Dialect
	preds    []Pred[E]
	orders   []Order[E]
	includes []IncludeSpec[E]
	sess     *Session
	limit    *int
	offset   *int
}

// Where adds conditions; multiple Where calls and multiple arguments are ANDed.
func (q QueryBuilder[E]) Where(ps ...Pred[E]) QueryBuilder[E] {
	q.preds = append(slices.Clip(q.preds), ps...)
	return q
}

func (q QueryBuilder[E]) OrderBy(os ...Order[E]) QueryBuilder[E] {
	q.orders = append(slices.Clip(q.orders), os...)
	return q
}

// With adds eager loading of relations (specs are created by the Include
// method on relation descriptors: gen.User.Posts.Include(...)).
func (q QueryBuilder[E]) With(specs ...IncludeSpec[E]) QueryBuilder[E] {
	q.includes = append(slices.Clip(q.includes), specs...)
	return q
}

func (q QueryBuilder[E]) Limit(n int) QueryBuilder[E] {
	q.limit = &n
	return q
}

func (q QueryBuilder[E]) Offset(n int) QueryBuilder[E] {
	q.offset = &n
	return q
}

// ToSQL returns the final SQL and arguments — inspection instead of magic.
func (q QueryBuilder[E]) ToSQL() (string, []any) {
	return q.buildSelect(selectColumns(q.d, q.meta.SelectCols))
}

// All runs the query without tracking. An empty result is an empty slice, nil error.
func (q QueryBuilder[E]) All(ctx context.Context) ([]*E, error) {
	sqlStr, args := q.ToSQL()
	rows, err := q.db.Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("sorm: select %s: %w", q.meta.Table, err)
	}

	out := []*E{}
	for rows.Next() {
		e := new(E)
		if err := rows.Scan(q.meta.Scan(e)...); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sorm: scan %s: %w", q.meta.Table, err)
		}
		out = append(out, e)
	}
	// Close BEFORE loading relations: on a single-connection DB (pgx.Tx)
	// a second query cannot be opened over an active one.
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sorm: select %s: %w", q.meta.Table, err)
	}

	// Identity map: before loading relations, so children attach to canonical
	// pointers. An already-tracked row maps to the existing object; local
	// changes are not overwritten.
	if q.sess != nil {
		st := storeOf[E](q.sess)
		for i, e := range out {
			out[i] = st.trackScanned(e)
		}
	}

	for _, spec := range q.includes {
		if err := spec.load(ctx, q.db, q.sess, out); err != nil {
			return nil, fmt.Errorf("sorm: select %s: %w", q.meta.Table, err)
		}
	}
	return out, nil
}

// Iter streams the result: rows are yielded as they are read, without loading
// the whole result set into memory. Incompatible with With (eager loading
// requires the full set of parents) — in that case the iterator yields a single error.
//
//	for u, err := range sorm.Query[models.User](db).Iter(ctx) {
//	    if err != nil { return err }
//	    ...
//	}
func (q QueryBuilder[E]) Iter(ctx context.Context) iter.Seq2[*E, error] {
	return func(yield func(*E, error) bool) {
		if len(q.includes) > 0 {
			yield(nil, fmt.Errorf("sorm: Iter is incompatible with With/Include — use All"))
			return
		}
		sqlStr, args := q.ToSQL()
		rows, err := q.db.Query(ctx, sqlStr, args...)
		if err != nil {
			yield(nil, fmt.Errorf("sorm: select %s: %w", q.meta.Table, err))
			return
		}
		defer rows.Close()

		for rows.Next() {
			e := new(E)
			if err := rows.Scan(q.meta.Scan(e)...); err != nil {
				yield(nil, fmt.Errorf("sorm: scan %s: %w", q.meta.Table, err))
				return
			}
			if q.sess != nil {
				e = storeOf[E](q.sess).trackScanned(e)
			}
			if !yield(e, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, fmt.Errorf("sorm: select %s: %w", q.meta.Table, err))
		}
	}
}

// One returns the first row or ErrNotFound.
func (q QueryBuilder[E]) One(ctx context.Context) (*E, error) {
	all, err := q.Limit(1).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNotFound
	}
	return all[0], nil
}

func (q QueryBuilder[E]) Count(ctx context.Context) (int64, error) {
	// count(*) ignores ORDER BY/LIMIT/OFFSET of the original builder.
	base := q
	base.orders = nil
	base.limit = nil
	base.offset = nil
	sqlStr, args := base.buildSelect("count(*)")

	rows, err := q.db.Query(ctx, sqlStr, args...)
	if err != nil {
		return 0, fmt.Errorf("sorm: count %s: %w", q.meta.Table, err)
	}
	defer rows.Close()

	var n int64
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, fmt.Errorf("sorm: count %s: %w", q.meta.Table, err)
		}
	}
	return n, rows.Err()
}

func (q QueryBuilder[E]) buildSelect(selectList string) (string, []any) {
	w := newSQLWriter(q.d)
	w.raw("SELECT " + selectList + " FROM ")
	w.ident(q.meta.Table)

	if len(q.preds) > 0 {
		w.raw(" WHERE ")
		logicalNode{"AND", nodesOf(q.preds)}.writeSQL(w)
	}
	if len(q.orders) > 0 {
		w.raw(" ORDER BY ")
		for i, o := range q.orders {
			if i > 0 {
				w.raw(", ")
			}
			w.col(o.ref)
			if o.desc {
				w.raw(" DESC")
			}
		}
	}
	if q.limit != nil {
		w.raw(" LIMIT " + strconv.Itoa(*q.limit))
	}
	if q.offset != nil {
		w.raw(" OFFSET " + strconv.Itoa(*q.offset))
	}
	return w.sb.String(), w.args
}

func selectColumns(d dialect.Dialect, cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += d.QuoteIdent(c)
	}
	return out
}
