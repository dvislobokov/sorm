package otelsorm

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect"
)

// observer is a thin outer wrapper around the instrumented DB. It covers
// the two signals the per-operation middleware cannot see on its own:
//
//   - sorm.db.rows.returned — rows are fetched AFTER Query returns, so the
//     result cursor is wrapped and counted until Close;
//   - sorm.tx.duration — begin and commit are separate operations, so the
//     transaction object itself carries its start time.
type observer struct {
	inner  sorm.DB
	m      *metrics
	system string
}

func (o observer) Dialect() dialect.Dialect { return o.inner.Dialect() }

func (o observer) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	return o.inner.Exec(ctx, sql, args...)
}

func (o observer) ExecBatch(ctx context.Context, items []sorm.BatchItem) error {
	return o.inner.ExecBatch(ctx, items)
}

func (o observer) Query(ctx context.Context, sql string, args ...any) (sorm.Rows, error) {
	rows, err := o.inner.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	attrs := []attribute.KeyValue{attribute.String("db.system", o.system)}
	if name := sorm.QueryNameFromContext(ctx); name != "" {
		attrs = append(attrs, attribute.String("sorm.query.name", name))
	}
	return &countingRows{Rows: rows, ctx: ctx, m: o.m, attrs: attrs}, nil
}

func (o observer) Begin(ctx context.Context) (sorm.Tx, error) {
	tx, err := o.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &observedTx{Tx: tx, o: observer{inner: tx, m: o.m, system: o.system}, start: time.Now()}, nil
}

// EmitOp / RetryableError delegate to the instrumented inner DB so that
// RunInTx keeps working through this wrapper.
func (o observer) EmitOp(ctx context.Context, op sorm.Op) {
	if em, ok := o.inner.(interface {
		EmitOp(context.Context, sorm.Op)
	}); ok {
		em.EmitOp(ctx, op)
	}
}

func (o observer) RetryableError(err error) bool {
	if rc, ok := o.inner.(interface{ RetryableError(error) bool }); ok {
		return rc.RetryableError(err)
	}
	return false
}

// --- rows counting ---

type countingRows struct {
	sorm.Rows
	ctx   context.Context
	m     *metrics
	attrs []attribute.KeyValue
	n     int64
	once  sync.Once
}

func (r *countingRows) Next() bool {
	ok := r.Rows.Next()
	if ok {
		r.n++
	}
	return ok
}

func (r *countingRows) Close() {
	r.Rows.Close()
	r.once.Do(func() {
		r.m.rows.Record(r.ctx, r.n, metric.WithAttributes(r.attrs...))
	})
}

// --- transaction timing ---

type observedTx struct {
	sorm.Tx
	o     observer // ops inside the tx keep rows counting and nested timing
	start time.Time
	once  sync.Once
}

func (t *observedTx) Query(ctx context.Context, sql string, args ...any) (sorm.Rows, error) {
	return t.o.Query(ctx, sql, args...)
}

func (t *observedTx) Begin(ctx context.Context) (sorm.Tx, error) {
	return t.o.Begin(ctx)
}

func (t *observedTx) Commit(ctx context.Context) error {
	err := t.Tx.Commit(ctx)
	t.record(ctx, "commit")
	return err
}

func (t *observedTx) Rollback(ctx context.Context) error {
	err := t.Tx.Rollback(ctx)
	t.record(ctx, "rollback")
	return err
}

func (t *observedTx) record(ctx context.Context, outcome string) {
	t.once.Do(func() {
		t.o.m.txDuration.Record(ctx, time.Since(t.start).Seconds(), metric.WithAttributes(
			attribute.String("db.system", t.o.system),
			attribute.String("outcome", outcome),
		))
	})
}
