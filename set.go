package sorm

import (
	"context"
	"iter"
)

// Set is typed access to one entity within a unit of work — the EF Core
// DbSet analog. Queries started from a set are tracked: materialized
// entities land in the session's identity map, plain Go mutations are
// picked up by SaveChanges. The generated Context wires a set per entity.
//
//	c := sormgen.NewContext(db)
//	users, err := c.Users.Where(u.Age.Gt(18)).All(ctx)
//	users[0].Name = "new"                 // tracked — no manual Track
//	c.Orders.Add(&models.Order{...})
//	err = c.SaveChanges(ctx)
type Set[E any] struct{ sess *Session }

// NewSet binds a set to a session. Normally called by the generated
// NewContext, not by hand.
func NewSet[E any](s *Session) Set[E] { return Set[E]{sess: s} }

// Query starts a tracked query (identity map, EF semantics: a reloaded
// row returns the already-tracked pointer, local changes win).
func (s Set[E]) Query() QueryBuilder[E] { return Track[E](s.sess) }

// NoTracking starts an untracked read-only query: no snapshots, no
// identity map — cheaper for large result sets you won't mutate.
func (s Set[E]) NoTracking() QueryBuilder[E] { return Query[E](s.sess.db) }

// Shortcuts to Query() so a set reads like a query root.
func (s Set[E]) Where(ps ...Pred[E]) QueryBuilder[E]          { return s.Query().Where(ps...) }
func (s Set[E]) OrderBy(os ...Order[E]) QueryBuilder[E]       { return s.Query().OrderBy(os...) }
func (s Set[E]) With(specs ...IncludeSpec[E]) QueryBuilder[E] { return s.Query().With(specs...) }
func (s Set[E]) Named(name string) QueryBuilder[E]            { return s.Query().Named(name) }
func (s Set[E]) Limit(n int) QueryBuilder[E]                  { return s.Query().Limit(n) }

func (s Set[E]) All(ctx context.Context) ([]*E, error)     { return s.Query().All(ctx) }
func (s Set[E]) Count(ctx context.Context) (int64, error)  { return s.Query().Count(ctx) }
func (s Set[E]) Iter(ctx context.Context) iter.Seq2[*E, error] { return s.Query().Iter(ctx) }

// Add registers new entities for INSERT on the session.
func (s Set[E]) Add(entities ...*E) { Add(s.sess, entities...) }

// Remove marks entities for DELETE on the session.
func (s Set[E]) Remove(entities ...*E) { Remove(s.sess, entities...) }

// Find returns the entity by primary key: first from the identity map
// (no SQL if already tracked — EF Find semantics), otherwise a tracked
// SELECT by PK. ErrNotFound if the row does not exist.
func (s Set[E]) Find(ctx context.Context, pk any) (*E, error) {
	st := storeOf[E](s.sess)
	if r, ok := st.byPK[pk]; ok {
		return r.e, nil
	}
	if r, ok := st.byPK[normalizePK(pk)]; ok {
		return r.e, nil
	}
	m := st.meta
	return s.Query().Where(pred[E](cmpNode{colRef{m.Table, m.PK}, "=", pk})).One(ctx)
}

// normalizePK widens integer PK values to int64 so Find(ctx, 5) hits the
// identity map keyed by an int64 PKValue.
func normalizePK(v any) any {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	}
	return v
}
