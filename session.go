package sorm

import (
	"context"
	"fmt"
	"reflect"
)

// Session — Unit of Work: identity map + snapshot change tracking.
// Entities loaded via Track are mutated with plain Go code;
// SaveChanges computes the minimal diff and applies it in batches
// within a single transaction.
//
// Not thread-safe (like DbContext in EF); lives for one unit of work.
type Session struct {
	db     DB
	stores map[reflect.Type]anyStore
}

func NewSession(db DB) *Session {
	// A unit of work lives on the primary: tracked snapshots from a
	// lagging replica would yield stale diffs (see WithReplicas).
	return &Session{db: Primary(db), stores: map[reflect.Type]anyStore{}}
}

// DB returns the connection or transaction the session runs on.
func (s *Session) DB() DB { return s.db }

// Track is the same query builder, but materialized entities go into
// the tracker. Reloading the same row returns the already-tracked
// pointer; database data does not overwrite local changes (EF semantics).
func Track[E any](s *Session) QueryBuilder[E] {
	q := Query[E](s.db)
	q.sess = s
	return q
}

// Add registers new entities for INSERT. FKs of a new graph are set via
// navigation (p.Author = u) — the runtime fills in the FK column value
// after the parent is inserted.
func Add[E any](s *Session, entities ...*E) {
	st := storeOf[E](s)
	for _, e := range entities {
		if _, dup := st.added[e]; dup {
			continue
		}
		st.added[e] = struct{}{}
		st.addedOrder = append(st.addedOrder, e)
	}
}

// Remove marks entities for DELETE. An entity must be tracked
// or have a populated PK (checked in SaveChanges).
func Remove[E any](s *Session, entities ...*E) {
	st := storeOf[E](s)
	for _, e := range entities {
		if _, dup := st.removed[e]; dup {
			continue
		}
		st.removed[e] = struct{}{}
		st.removedOrder = append(st.removedOrder, e)
	}
}

// SaveChanges opens a transaction, applies the diff, and commits.
// Order: DELETE (children before parents) → UPDATE (changed columns
// only) → INSERT by dependency level (RETURNING → FK fixup of children).
// DELETE+UPDATE go in one pgx.Batch (single roundtrip), each insert
// level in another.
func (s *Session) SaveChanges(ctx context.Context) error {
	// Session on top of an already-open transaction (RunInTx): flush in it,
	// commit is up to the transaction owner.
	if tx, ok := s.db.(Tx); ok {
		return s.SaveChangesTx(ctx, tx)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("sorm: begin: %w", err)
	}
	post, err := s.flush(ctx, tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("sorm: commit: %w", err)
	}
	post()
	return nil
}

// SaveChangesTx applies the diff inside an external transaction; commit is up to the caller.
// Note: tracker state is updated immediately after a successful flush.
func (s *Session) SaveChangesTx(ctx context.Context, tx Tx) error {
	post, err := s.flush(ctx, tx)
	if err != nil {
		return err
	}
	post()
	return nil
}

// --- per-type store ---

type anyStore interface {
	collectAdded(set map[any]bool)
	buildPlan(ctx context.Context, p *flushPlan) error
}

type rec[E any] struct {
	e    *E
	snap any
}

type tracker[E any] struct {
	meta         *Meta[E]
	byPK         map[any]*rec[E]
	trackOrder   []*rec[E]
	added        map[*E]struct{}
	addedOrder   []*E
	removed      map[*E]struct{}
	removedOrder []*E
}

func storeOf[E any](s *Session) *tracker[E] {
	t := reflect.TypeFor[E]()
	if st, ok := s.stores[t]; ok {
		return st.(*tracker[E])
	}
	tr := &tracker[E]{
		meta:    metaFor[E](),
		byPK:    map[any]*rec[E]{},
		added:   map[*E]struct{}{},
		removed: map[*E]struct{}{},
	}
	s.stores[t] = tr
	return tr
}

// trackScanned — identity map: same row → same pointer.
func (t *tracker[E]) trackScanned(e *E) *E {
	pk := t.meta.PKValue(e)
	if existing, ok := t.byPK[pk]; ok {
		return existing.e
	}
	r := &rec[E]{e: e, snap: t.meta.Snapshot(e)}
	t.byPK[pk] = r
	t.trackOrder = append(t.trackOrder, r)
	return e
}

func (t *tracker[E]) collectAdded(set map[any]bool) {
	for _, e := range t.addedOrder {
		// Membership lives in the map: a previous SaveChanges of the same
		// session removes flushed entities from it (addedOrder keeps history).
		if _, pending := t.added[e]; !pending {
			continue
		}
		if _, cancelled := t.removed[e]; !cancelled {
			set[any(e)] = true
		}
	}
}
