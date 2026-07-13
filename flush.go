package sorm

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/dvislobokov/sorm/dialect"
)

// flushPlan — the assembled write plan: statements and the insert dependency graph.
type flushPlan struct {
	d dialect.Dialect
	// schema — non-empty for InSchema connections: qualifies table names.
	schema string
	// now — a single timestamp for the whole flush: every autoCreate/autoUpdate
	// field stamped in this SaveChanges gets the same value.
	now time.Time
	// addedSet — all Added pointers of all types (for edge detection).
	addedSet map[any]bool
	deletes  []planStmt
	updates  []planStmt
	inserts  map[any]*insertNode // keyed by entity pointer
	seq      int
	// tableRefs: table → tables it references (DELETE ordering).
	tableRefs map[string][]string
	// post — tracker bookkeeping after a successful apply.
	post []func()
}

type planStmt struct {
	table string
	item  BatchItem
}

type insertNode struct {
	seq        int
	table      string
	deps       []any // pointers to Added parents
	auto       bool
	pkCol      string
	insertCols []string
	// values is called after the parents are inserted: FK fixup happens inside.
	values func() []any
	setPK  func(int64)
}

func (s *Session) flush(ctx context.Context, db DB) (post func(), err error) {
	p := &flushPlan{
		d:         db.Dialect(),
		schema:    schemaOf(db),
		now:       time.Now(),
		addedSet:  map[any]bool{},
		inserts:   map[any]*insertNode{},
		tableRefs: map[string][]string{},
	}
	// Pass 1: the set of new entities (needed for edge detection).
	for _, st := range s.stores {
		st.collectAdded(p.addedSet)
	}
	// Pass 2: statements and validation (before hitting the DB).
	// Stores are traversed by type name: deterministic plan.
	for _, st := range storesSorted(s) {
		if err := st.buildPlan(p); err != nil {
			return nil, err
		}
	}

	// 1. DELETE (children before parents) + UPDATE — one batch.
	first := append(orderDeletes(p.deletes, p.tableRefs), p.updates...)
	if len(first) > 0 {
		items := make([]BatchItem, len(first))
		for i, st := range first {
			items[i] = st.item
		}
		if err := db.ExecBatch(ctx, items); err != nil {
			return nil, err
		}
	}

	// 2. INSERT by level: a level is ready once all of its Added parents are inserted.
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

		// values time: parents from previous levels already have PKs (fixup inside).
		items := groupInserts(p.d, p.schema, level)
		if err := db.ExecBatch(ctx, items); err != nil {
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

// maxInsertRows / maxInsertArgs — multi-row INSERT limits: rows per
// statement and placeholders (PG is capped at 65535 parameters).
const (
	maxInsertRows = 500
	maxInsertArgs = 30000
)

// groupInserts merges same-level inserts into multi-row INSERTs:
// consecutive nodes of the same table → INSERT ... VALUES (...),(...),...
func groupInserts(d dialect.Dialect, schema string, level []*insertNode) []BatchItem {
	var items []BatchItem
	for start := 0; start < len(level); {
		n := level[start]
		rowCap := maxInsertRows
		if len(n.insertCols) > 0 {
			if byArgs := maxInsertArgs / len(n.insertCols); byArgs < rowCap {
				rowCap = byArgs
			}
		}
		end := start + 1
		for end < len(level) && end-start < rowCap && level[end].table == n.table {
			end++
		}
		group := level[start:end]
		items = append(items, buildInsertItem(d, schema, group))
		start = end
	}
	return items
}

func buildInsertItem(d dialect.Dialect, schema string, group []*insertNode) BatchItem {
	first := group[0]
	args := make([]any, 0, len(group)*len(first.insertCols))
	for _, n := range group {
		args = append(args, n.values()...)
	}
	item := BatchItem{
		SQL:  multiInsertSQL(d, schema, first, len(group)),
		Args: args,
	}
	if first.auto {
		item.IDCount = len(group)
		item.OnIDs = func(ids []int64) {
			for i, n := range group {
				n.setPK(ids[i])
			}
		}
	}
	return item
}

func multiInsertSQL(d dialect.Dialect, schema string, n *insertNode, rows int) string {
	var b strings.Builder
	b.Grow(64 + rows*len(n.insertCols)*5)
	b.WriteString("INSERT INTO ")
	b.WriteString(qualifiedTable(d, schema, n.table))
	b.WriteString(" (")
	for i, c := range n.insertCols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.QuoteIdent(c))
	}
	b.WriteString(") VALUES ")
	arg := 0
	for r := 0; r < rows; r++ {
		if r > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for i := range n.insertCols {
			if i > 0 {
				b.WriteString(", ")
			}
			arg++
			b.WriteString(d.Placeholder(arg))
		}
		b.WriteByte(')')
	}
	if n.auto && d.ReturningSupported() {
		b.WriteString(" RETURNING ")
		b.WriteString(d.QuoteIdent(n.pkCol))
	}
	return b.String()
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

// orderDeletes sorts DELETE statements so that child tables come
// before the tables they reference. Self-references are ignored
// (the best we can do without deferred constraints).
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
			return 0 // table cycle — leave unordered
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
	// Greater depth = further from the root = child → deleted first.
	sort.SliceStable(stmts, func(i, j int) bool {
		return rank[stmts[i].table] > rank[stmts[j].table]
	})
	return stmts
}

// --- buildPlan for a specific type ---

func (t *tracker[E]) buildPlan(p *flushPlan) error {
	m := t.meta
	p.tableRefs[m.Table] = m.RefTables

	// DELETE: tracked or with a populated PK; added+removed cancel each other out.
	// Membership lives in the maps: a previous SaveChanges of the same session
	// removes flushed entities from them (the order slices keep history).
	for _, e := range t.removedOrder {
		if _, pending := t.removed[e]; !pending {
			continue
		}
		if _, wasAdded := t.added[e]; wasAdded {
			continue
		}
		pk := m.PKValue(e)
		if _, tracked := t.byPK[pk]; !tracked && isZero(pk) {
			return fmt.Errorf("sorm: Remove(%s): entity is neither tracked nor has a primary key", m.Table)
		}
		t.planDelete(p, e, pk)
	}

	// UPDATE: diff of tracked entities.
	for _, r := range t.trackOrder {
		if _, gone := t.removed[r.e]; gone {
			continue
		}
		// Stale rec: the row was deleted by a previous flush of this session
		// (byPK holds the canonical rec; trackOrder keeps history).
		if t.byPK[m.PKValue(r.e)] != r {
			continue
		}
		idxs := m.Diff(r.snap, r.e)
		if len(idxs) == 0 {
			continue
		}
		// autoUpdate stamps only effective updates: the diff is re-taken so the
		// timestamp column joins the changed set.
		if m.TouchUpdate != nil {
			m.TouchUpdate(r.e, p.now)
			idxs = m.Diff(r.snap, r.e)
		}
		t.planUpdate(p, r, idxs)
	}

	// INSERT: added (except mutually cancelled ones).
	for _, e := range t.addedOrder {
		if _, pending := t.added[e]; !pending {
			continue
		}
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

	var sql string
	var args []any
	if sd := m.SoftDeleteCol; sd != "" {
		// Soft delete: an UPDATE stamping the column, with the same
		// optimistic-concurrency predicate a hard delete would carry.
		sql = "UPDATE " + qualifiedTable(p.d, p.schema, m.Table) + " SET " +
			p.d.QuoteIdent(sd) + " = " + p.d.Placeholder(1)
		args = []any{p.now}
		if versioned {
			sql += ", " + p.d.QuoteIdent(m.VersionCol) + " = " + p.d.QuoteIdent(m.VersionCol) + " + 1"
		}
		args = append(args, pk)
		sql += " WHERE " + p.d.QuoteIdent(m.PK) + " = " + p.d.Placeholder(len(args))
		if versioned {
			args = append(args, m.GetVersion(e))
			sql += " AND " + p.d.QuoteIdent(m.VersionCol) + " = " + p.d.Placeholder(len(args))
		}
		// Only alive rows: soft-deleting twice must not re-stamp.
		sql += " AND " + p.d.QuoteIdent(sd) + " IS NULL"
	} else {
		sql = "DELETE FROM " + qualifiedTable(p.d, p.schema, m.Table) + " WHERE " + p.d.QuoteIdent(m.PK) + " = " + p.d.Placeholder(1)
		args = []any{pk}
		if versioned {
			sql += " AND " + p.d.QuoteIdent(m.VersionCol) + " = " + p.d.Placeholder(2)
			args = append(args, m.GetVersion(e))
		}
	}
	p.deletes = append(p.deletes, planStmt{
		table: m.Table,
		item:  BatchItem{SQL: sql, Args: args, Check: expectOneRow(m.Table, pk)},
	})
	p.post = append(p.post, func() {
		if m.SetDeleted != nil {
			m.SetDeleted(e, p.now)
			if versioned {
				m.SetVersion(e, m.GetVersion(e)+1)
			}
		}
		delete(t.byPK, pk)
		delete(t.removed, e)
	})
}

func (t *tracker[E]) planUpdate(p *flushPlan, r *rec[E], idxs []int) {
	m := t.meta
	versioned := m.VersionCol != ""
	vals := m.ValuesFor(r.e, idxs)

	sql := "UPDATE " + qualifiedTable(p.d, p.schema, m.Table) + " SET "
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
		item:  BatchItem{SQL: sql, Args: args, Check: expectOneRow(m.Table, pk)},
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

	// FK/navigation validation — before hitting the DB.
	var deps []any
	for _, ref := range m.Refs {
		nav := ref.Nav(e)
		switch {
		case nav != nil && p.addedSet[nav]:
			deps = append(deps, nav) // parent is new too → toposort edge
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
	// Auto-timestamps: created (unless set manually) and updated stamp on insert.
	if m.TouchCreate != nil {
		m.TouchCreate(e, p.now)
	}
	if m.TouchUpdate != nil {
		m.TouchUpdate(e, p.now)
	}

	refs := m.Refs
	p.seq++
	p.inserts[any(e)] = &insertNode{
		seq:        p.seq,
		table:      m.Table,
		deps:       deps,
		auto:       m.Auto,
		pkCol:      m.PK,
		insertCols: m.InsertCols,
		values: func() []any {
			// values time: parents are inserted → FK fixup via navigations.
			for _, ref := range refs {
				if nav := ref.Nav(e); nav != nil {
					ref.SetFK(e, ref.NavPK(e))
				}
			}
			return m.InsertValues(e)
		},
		setPK: func(id int64) { m.SetPK(e, id) },
	}

	p.post = append(p.post, func() {
		delete(t.added, e)
		pk := m.PKValue(e)
		r := &rec[E]{e: e, snap: m.Snapshot(e)}
		t.byPK[pk] = r
		t.trackOrder = append(t.trackOrder, r)
	})
	return nil
}

func expectOneRow(table string, pk any) func(int64) error {
	return func(rowsAffected int64) error {
		if rowsAffected == 0 {
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
