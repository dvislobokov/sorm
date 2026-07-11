package sorm

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"sorm/dialect"
	"sorm/dialect/pg"
)

// defaultDialect — PG-only в MVP; при мультидиалектности диалект приедет
// из драйверного адаптера DB.
var defaultDialect dialect.Dialect = pg.Dialect{}

// Query начинает типизированный запрос по сущности E.
// Билдер иммутабелен: каждый метод возвращает копию, переиспользование
// базового билдера безопасно (никакого state-leak между запросами).
func Query[E any](db DB) QueryBuilder[E] {
	return QueryBuilder[E]{db: db, meta: metaFor[E](), d: defaultDialect}
}

type QueryBuilder[E any] struct {
	db       DB
	meta     *Meta[E]
	d        dialect.Dialect
	preds    []Pred[E]
	orders   []Order[E]
	includes []IncludeSpec[E]
	limit    *int
	offset   *int
}

// Where добавляет условия; несколько Where и несколько аргументов — AND.
func (q QueryBuilder[E]) Where(ps ...Pred[E]) QueryBuilder[E] {
	q.preds = append(slices.Clip(q.preds), ps...)
	return q
}

func (q QueryBuilder[E]) OrderBy(os ...Order[E]) QueryBuilder[E] {
	q.orders = append(slices.Clip(q.orders), os...)
	return q
}

// With добавляет eager loading связей (спецификации создаются методом
// Include на дескрипторах связей: gen.User.Posts.Include(...)).
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

// ToSQL возвращает итоговый SQL и аргументы — инспекция вместо магии.
func (q QueryBuilder[E]) ToSQL() (string, []any) {
	return q.buildSelect(selectColumns(q.d, q.meta.SelectCols))
}

// All выполняет запрос без трекинга. Пустой результат — пустой слайс, nil error.
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
	// Закрываем ДО загрузки связей: на однососединительном DB (pgx.Tx)
	// нельзя открыть второй запрос поверх активного.
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sorm: select %s: %w", q.meta.Table, err)
	}

	for _, spec := range q.includes {
		if err := spec.load(ctx, q.db, out); err != nil {
			return nil, fmt.Errorf("sorm: select %s: %w", q.meta.Table, err)
		}
	}
	return out, nil
}

// One возвращает первую строку или ErrNotFound.
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
	// count(*) игнорирует ORDER BY/LIMIT/OFFSET исходного билдера.
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
			w.ident(o.col)
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
