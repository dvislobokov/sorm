package sorm

import (
	"context"
	"sync/atomic"

	"github.com/dvislobokov/sorm/dialect"
)

// WithReplicas splits reads and writes: untracked SELECTs go to the
// replicas round-robin, everything else stays on the primary.
//
//	db := sorm.WithReplicas(pgxd.Wrap(primary),
//	    pgxd.Wrap(replica1), pgxd.Wrap(replica2))
//
// Routing rules:
//   - Query (untracked reads)            → next replica
//   - Exec / ExecBatch / Begin / RunInTx → primary
//   - Sessions and generated Contexts    → primary entirely (read-your-
//     writes: a tracked snapshot from a lagging replica would produce
//     stale diffs and false version conflicts)
//   - ForUpdate / ForUpdateSkipLocked    → primary (locks on a replica
//     are meaningless)
//
// Explicit overrides: sorm.Primary(db) pins the primary for a single
// query, sorm.Replica(db) pins a replica (both are no-ops for plain
// connections). Health checking and failover are the pool's job — the
// resolver only routes.
//
// Composes with other wrappers; recommended order is instrumentation
// outside, InSchema in the middle, the resolver inside.
func WithReplicas(primary DB, replicas ...DB) DB {
	if len(replicas) == 0 {
		return primary
	}
	return &resolver{primary: primary, replicas: replicas}
}

// primaryProvider is the unwrap capability: NewSession, locked queries
// and sorm.Primary use it to reach the write connection through any
// stack of wrappers.
type primaryProvider interface{ Primary() DB }

// replicaProvider — the read counterpart, used by sorm.Replica.
type replicaProvider interface{ Replica() DB }

// Primary returns the write connection behind db: unwraps WithReplicas
// through any wrapper stack; plain connections come back unchanged.
func Primary(db DB) DB {
	if p, ok := db.(primaryProvider); ok {
		return p.Primary()
	}
	return db
}

// Replica returns a read replica behind db (round-robin); plain
// connections come back unchanged.
func Replica(db DB) DB {
	if r, ok := db.(replicaProvider); ok {
		return r.Replica()
	}
	return db
}

type resolver struct {
	primary  DB
	replicas []DB
	next     atomic.Uint64
}

func (r *resolver) pick() DB {
	n := r.next.Add(1)
	return r.replicas[(n-1)%uint64(len(r.replicas))]
}

func (r *resolver) Primary() DB { return r.primary }
func (r *resolver) Replica() DB { return r.pick() }

func (r *resolver) Dialect() dialect.Dialect { return r.primary.Dialect() }

func (r *resolver) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return r.pick().Query(ctx, sql, args...)
}

func (r *resolver) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	return r.primary.Exec(ctx, sql, args...)
}

func (r *resolver) ExecBatch(ctx context.Context, items []BatchItem) error {
	return r.primary.ExecBatch(ctx, items)
}

func (r *resolver) Begin(ctx context.Context) (Tx, error) {
	return r.primary.Begin(ctx)
}

// RetryableError / EmitOp delegate to the primary so RunInTx retries and
// instrumentation keep working through the resolver.
func (r *resolver) RetryableError(err error) bool {
	if rc, ok := r.primary.(retryClassifier); ok {
		return rc.RetryableError(err)
	}
	return false
}

func (r *resolver) EmitOp(ctx context.Context, op Op) {
	if em, ok := r.primary.(interface {
		EmitOp(context.Context, Op)
	}); ok {
		em.EmitOp(ctx, op)
	}
}
