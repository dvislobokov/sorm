package sorm

import (
	"context"
	"fmt"
)

// ManyToMany — связь «многие ко многим» через неявную join-таблицу
// (тег `many2many:join_table`). Чтение — Include/Any; запись — явные
// Link/Unlink: sorm не угадывает диффы коллекций, связывание — операция.
type ManyToMany[E, C any] struct {
	joinTable   string
	parentCol   string // колонка join-таблицы со стороны E
	childCol    string // колонка join-таблицы со стороны C
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

// Link связывает parent с children (INSERT в join-таблицу). Обе стороны
// должны быть persisted. Повторное связывание — ConstraintError (композитный
// PK join-таблицы).
func (r ManyToMany[E, C]) Link(ctx context.Context, db DB, parent *E, children ...*C) error {
	if len(children) == 0 {
		return nil
	}
	pm, cm := metaFor[E](), metaFor[C]()
	d := dialectOf(db)

	sql := "INSERT INTO " + d.QuoteIdent(r.joinTable) + " (" +
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

// Unlink разрывает связи parent с children (DELETE из join-таблицы).
func (r ManyToMany[E, C]) Unlink(ctx context.Context, db DB, parent *E, children ...*C) error {
	if len(children) == 0 {
		return nil
	}
	pm, cm := metaFor[E](), metaFor[C]()
	w := newSQLWriter(dialectOf(db))
	w.raw("DELETE FROM ")
	w.ident(r.joinTable)
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

// Any — фильтр E по связанным C: EXISTS по join-таблице; preds сужают C
// коррелированным подзапросом.
func (r ManyToMany[E, C]) Any(preds ...Pred[C]) Pred[E] {
	pm, cm := metaFor[E](), metaFor[C]()
	return pred[E](m2mExistsNode{
		joinTable: r.joinTable, parentCol: r.parentCol, childCol: r.childCol,
		parentTable: pm.Table, parentPK: pm.PK,
		childTable: cm.Table, childPK: cm.PK,
		preds: nodesOf(preds),
	})
}

// Include — eager loading связанных C: пары из join-таблицы + загрузка
// детей одним IN-запросом (с чанкованием), раскладка по родителям.
// Order[C]-опции задают порядок детей у каждого родителя.
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

		// 1. пары (parent, child) из join-таблицы
		type pair struct{ p, c any }
		var pairs []pair
		childSeen := map[any]bool{}
		var childKeys []any
		for _, chunk := range chunked(keys) {
			w := newSQLWriter(dialectOf(db))
			w.raw("SELECT ")
			w.ident(r.parentCol)
			w.raw(", ")
			w.ident(r.childCol)
			w.raw(" FROM ")
			w.ident(r.joinTable)
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

		// 2. дети одним IN-запросом (порядок — из cfg.orders)
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

		// 3. раскладка: обходим детей в порядке запроса — Order[C] соблюдён
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

// normalizeKey приводит значения ключей к сравнимому виду:
// database/sql-драйверы (MySQL) возвращают строки как []byte.
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
	w.ident(n.joinTable)
	w.raw(" WHERE ")
	w.col(colRef{n.joinTable, n.parentCol})
	w.raw(" = ")
	w.ident(n.parentTable)
	w.raw(".")
	w.ident(n.parentPK)
	if len(n.preds) > 0 {
		w.raw(" AND ")
		w.col(colRef{n.joinTable, n.childCol})
		w.raw(" IN (SELECT ")
		w.ident(n.childPK)
		w.raw(" FROM ")
		w.ident(n.childTable)
		w.raw(" WHERE ")
		logicalNode{"AND", n.preds}.writeSQL(w)
		w.raw(")")
	}
	w.raw(")")
}
