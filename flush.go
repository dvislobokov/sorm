package sorm

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/jackc/pgx/v5"

	"sorm/dialect"
)

// flushPlan — собранный план записи: statements и граф зависимостей вставок.
type flushPlan struct {
	d dialect.Dialect
	// addedSet — все Added-указатели всех типов (для распознавания рёбер).
	addedSet map[any]bool
	deletes  []planStmt
	updates  []planStmt
	inserts  map[any]*insertNode // ключ — указатель на сущность
	seq      int
	// tableRefs: таблица → таблицы, на которые она ссылается (порядок DELETE).
	tableRefs map[string][]string
	// post — бухгалтерия трекеров после успешного применения.
	post []func()
}

type planStmt struct {
	table string
	queue func(b *pgx.Batch)
	check func(res pgx.BatchResults) error
}

type insertNode struct {
	seq   int
	table string
	deps  []any // указатели на Added-родителей
	queue func(b *pgx.Batch) // вызывается после вставки родителей: FK-fixup внутри
	scan  func(res pgx.BatchResults) error
}

func (s *Session) flush(ctx context.Context, db DB) (post func(), err error) {
	p := &flushPlan{
		d:         defaultDialect,
		addedSet:  map[any]bool{},
		inserts:   map[any]*insertNode{},
		tableRefs: map[string][]string{},
	}
	// Проход 1: множество новых сущностей (нужно для распознавания рёбер).
	for _, st := range s.stores {
		st.collectAdded(p.addedSet)
	}
	// Проход 2: statements и валидация (до похода в БД).
	// Порядок обхода stores — по имени типа: детерминизм плана.
	for _, st := range storesSorted(s) {
		if err := st.buildPlan(p); err != nil {
			return nil, err
		}
	}

	// 1. DELETE (дети раньше родителей) + UPDATE — один батч, один roundtrip.
	first := append(orderDeletes(p.deletes, p.tableRefs), p.updates...)
	if err := sendBatch(ctx, db, first); err != nil {
		return nil, err
	}

	// 2. INSERT по уровням: уровень готов, когда все его Added-родители вставлены.
	remaining := p.inserts
	for len(remaining) > 0 {
		var level []*insertNode
		for _, n := range remaining {
			ready := true
			for _, d := range n.deps {
				if _, pending := remaining[d]; pending {
					ready = false
					break
				}
			}
			if ready {
				level = append(level, n)
			}
		}
		if len(level) == 0 {
			return nil, ErrCyclicGraph
		}
		sort.Slice(level, func(i, j int) bool { return level[i].seq < level[j].seq })

		b := &pgx.Batch{}
		for _, n := range level {
			n.queue(b) // queue-время: родители предыдущих уровней уже имеют PK
		}
		br := db.SendBatch(ctx, b)
		for _, n := range level {
			if err := n.scan(br); err != nil {
				br.Close()
				return nil, err
			}
		}
		if err := br.Close(); err != nil {
			return nil, err
		}
		for _, n := range level {
			for ptr, node := range remaining {
				if node == n {
					delete(remaining, ptr)
				}
			}
		}
	}

	return func() {
		for _, f := range p.post {
			f()
		}
	}, nil
}

func storesSorted(s *Session) []anyStore {
	type kv struct {
		name string
		st   anyStore
	}
	var all []kv
	for t, st := range s.stores {
		all = append(all, kv{t.String(), st})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	out := make([]anyStore, len(all))
	for i, x := range all {
		out[i] = x.st
	}
	return out
}

func sendBatch(ctx context.Context, db DB, stmts []planStmt) error {
	if len(stmts) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, st := range stmts {
		st.queue(b)
	}
	br := db.SendBatch(ctx, b)
	for _, st := range stmts {
		if err := st.check(br); err != nil {
			br.Close()
			return err
		}
	}
	return br.Close()
}

// orderDeletes сортирует DELETE-стейтменты так, чтобы таблицы-дети шли
// раньше таблиц, на которые они ссылаются. Self-reference игнорируется
// (лучшее, что можно сделать без deferred constraints).
func orderDeletes(stmts []planStmt, refs map[string][]string) []planStmt {
	if len(stmts) == 0 {
		return stmts
	}
	rank := map[string]int{}
	var depth func(table string, seen map[string]bool) int
	depth = func(table string, seen map[string]bool) int {
		if r, ok := rank[table]; ok {
			return r
		}
		if seen[table] {
			return 0 // цикл таблиц — не упорядочиваем
		}
		seen[table] = true
		max := 0
		for _, parent := range refs[table] {
			if parent == table {
				continue // self-reference
			}
			if d := depth(parent, seen) + 1; d > max {
				max = d
			}
		}
		rank[table] = max
		return max
	}
	for _, st := range stmts {
		depth(st.table, map[string]bool{})
	}
	// Большая глубина = дальше от корня = ребёнок → удаляется раньше.
	sort.SliceStable(stmts, func(i, j int) bool {
		return rank[stmts[i].table] > rank[stmts[j].table]
	})
	return stmts
}

// --- buildPlan для конкретного типа ---

func (t *tracker[E]) buildPlan(p *flushPlan) error {
	m := t.meta
	p.tableRefs[m.Table] = m.RefTables

	// DELETE: отслеживаемые или с заполненным PK; added+removed взаимно гасятся.
	for _, e := range t.removedOrder {
		if _, wasAdded := t.added[e]; wasAdded {
			continue
		}
		pk := m.PKValue(e)
		if _, tracked := t.byPK[pk]; !tracked && isZero(pk) {
			return fmt.Errorf("sorm: Remove(%s): entity is neither tracked nor has a primary key", m.Table)
		}
		t.planDelete(p, e, pk)
	}

	// UPDATE: дифф отслеживаемых.
	for _, r := range t.trackOrder {
		if _, gone := t.removed[r.e]; gone {
			continue
		}
		idxs := m.Diff(r.snap, r.e)
		if len(idxs) == 0 {
			continue
		}
		t.planUpdate(p, r, idxs)
	}

	// INSERT: added (кроме взаимно погашенных).
	for _, e := range t.addedOrder {
		if _, cancelled := t.removed[e]; cancelled {
			continue
		}
		if err := t.planInsert(p, e); err != nil {
			return err
		}
	}
	return nil
}

func (t *tracker[E]) planDelete(p *flushPlan, e *E, pk any) {
	m := t.meta
	versioned := m.VersionCol != ""
	sql := "DELETE FROM " + p.d.QuoteIdent(m.Table) + " WHERE " + p.d.QuoteIdent(m.PK) + " = " + p.d.Placeholder(1)
	args := []any{pk}
	if versioned {
		sql += " AND " + p.d.QuoteIdent(m.VersionCol) + " = " + p.d.Placeholder(2)
		args = append(args, m.GetVersion(e))
	}
	p.deletes = append(p.deletes, planStmt{
		table: m.Table,
		queue: func(b *pgx.Batch) { b.Queue(sql, args...) },
		check: expectOneRow(m.Table, pk),
	})
	p.post = append(p.post, func() {
		delete(t.byPK, pk)
		delete(t.removed, e)
	})
}

func (t *tracker[E]) planUpdate(p *flushPlan, r *rec[E], idxs []int) {
	m := t.meta
	versioned := m.VersionCol != ""
	vals := m.ValuesFor(r.e, idxs)

	sql := "UPDATE " + p.d.QuoteIdent(m.Table) + " SET "
	args := make([]any, 0, len(vals)+2)
	for i, idx := range idxs {
		if i > 0 {
			sql += ", "
		}
		args = append(args, vals[i])
		sql += p.d.QuoteIdent(m.SelectCols[idx]) + " = " + p.d.Placeholder(len(args))
	}
	if versioned {
		sql += ", " + p.d.QuoteIdent(m.VersionCol) + " = " + p.d.QuoteIdent(m.VersionCol) + " + 1"
	}
	pk := m.PKValue(r.e)
	args = append(args, pk)
	sql += " WHERE " + p.d.QuoteIdent(m.PK) + " = " + p.d.Placeholder(len(args))
	if versioned {
		args = append(args, m.GetVersion(r.e))
		sql += " AND " + p.d.QuoteIdent(m.VersionCol) + " = " + p.d.Placeholder(len(args))
	}

	p.updates = append(p.updates, planStmt{
		table: m.Table,
		queue: func(b *pgx.Batch) { b.Queue(sql, args...) },
		check: expectOneRow(m.Table, pk),
	})
	p.post = append(p.post, func() {
		if versioned {
			m.SetVersion(r.e, m.GetVersion(r.e)+1)
		}
		r.snap = m.Snapshot(r.e)
	})
}

func (t *tracker[E]) planInsert(p *flushPlan, e *E) error {
	m := t.meta

	// Валидация FK/навигаций — до похода в БД.
	var deps []any
	for _, ref := range m.Refs {
		nav := ref.Nav(e)
		switch {
		case nav != nil && p.addedSet[nav]:
			deps = append(deps, nav) // родитель тоже новый → ребро топосорта
		case nav != nil:
			if isZero(ref.NavPK(e)) {
				return fmt.Errorf("sorm: insert %s: navigation for %q points to an entity without a primary key (did you forget to Add it?)",
					m.Table, ref.FKCol)
			}
		case ref.NotNull && ref.FKIsZero(e):
			return fmt.Errorf("sorm: insert %s: %q is required — set the navigation field or the FK column",
				m.Table, ref.FKCol)
		}
	}

	if m.VersionCol != "" && m.GetVersion(e) == 0 {
		m.SetVersion(e, 1)
	}

	sql := t.insertSQL(p.d)
	refs := m.Refs
	p.seq++
	node := &insertNode{
		seq:   p.seq,
		table: m.Table,
		deps:  deps,
		queue: func(b *pgx.Batch) {
			// queue-время: родители вставлены → FK-fixup по навигациям.
			for _, ref := range refs {
				if nav := ref.Nav(e); nav != nil {
					ref.SetFK(e, ref.NavPK(e))
				}
			}
			b.Queue(sql, m.InsertValues(e)...)
		},
	}
	if m.Auto {
		node.scan = func(res pgx.BatchResults) error {
			var id int64
			if err := res.QueryRow().Scan(&id); err != nil {
				return fmt.Errorf("sorm: insert %s: %w", m.Table, err)
			}
			m.SetPK(e, id)
			return nil
		}
	} else {
		node.scan = func(res pgx.BatchResults) error {
			if _, err := res.Exec(); err != nil {
				return fmt.Errorf("sorm: insert %s: %w", m.Table, err)
			}
			return nil
		}
	}
	p.inserts[any(e)] = node

	p.post = append(p.post, func() {
		delete(t.added, e)
		pk := m.PKValue(e)
		r := &rec[E]{e: e, snap: m.Snapshot(e)}
		t.byPK[pk] = r
		t.trackOrder = append(t.trackOrder, r)
	})
	return nil
}

func (t *tracker[E]) insertSQL(d dialect.Dialect) string {
	m := t.meta
	sql := "INSERT INTO " + d.QuoteIdent(m.Table) + " ("
	for i, c := range m.InsertCols {
		if i > 0 {
			sql += ", "
		}
		sql += d.QuoteIdent(c)
	}
	sql += ") VALUES ("
	for i := range m.InsertCols {
		if i > 0 {
			sql += ", "
		}
		sql += d.Placeholder(i + 1)
	}
	sql += ")"
	if m.Auto {
		sql += " RETURNING " + d.QuoteIdent(m.PK)
	}
	return sql
}

func expectOneRow(table string, pk any) func(pgx.BatchResults) error {
	return func(res pgx.BatchResults) error {
		ct, err := res.Exec()
		if err != nil {
			return fmt.Errorf("sorm: write %s: %w", table, err)
		}
		if ct.RowsAffected() == 0 {
			return &ConflictError{Table: table, PK: pk}
		}
		return nil
	}
}

func isZero(v any) bool {
	if v == nil {
		return true
	}
	return reflect.ValueOf(v).IsZero()
}
