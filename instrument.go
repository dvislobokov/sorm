package sorm

import (
	"context"

	"github.com/dvislobokov/sorm/dialect"
)

// Op — an operation description for instrumentation.
type Op struct {
	Kind string // "query" | "exec" | "batch" | "begin" | "commit" | "rollback"
	SQL  string // query text ("" for begin/commit/rollback; first statement for batch)
	Args []any
	// Statements — all statements of the batch (Kind == "batch").
	Statements []string
}

// InstrumentFunc wraps every DB operation: logging, metrics,
// tracing (an OpenTelemetry span around next). It must call next exactly
// once and return its error (possibly wrapped).
type InstrumentFunc func(ctx context.Context, op Op, next func(ctx context.Context) error) error

// Instrument wraps a DB with a middleware function. Works with any
// adapter; transactions are instrumented with the same fn.
//
//	db = sorm.Instrument(db, func(ctx context.Context, op sorm.Op, next func(context.Context) error) error {
//	    start := time.Now()
//	    err := next(ctx)
//	    slog.Info("sql", "kind", op.Kind, "sql", op.SQL, "dur", time.Since(start), "err", err)
//	    return err
//	})
func Instrument(db DB, fn InstrumentFunc) DB {
	return instrumented{inner: db, fn: fn}
}

type instrumented struct {
	inner DB
	fn    InstrumentFunc
}

func (d instrumented) Dialect() dialect.Dialect { return d.inner.Dialect() }

// EmitOp lets library internals surface synthetic operations to the
// instrumentation middleware — e.g. RunInTx emits Op{Kind: "tx.retry"}
// before every transient-error retry. next is a no-op for such events.
func (d instrumented) EmitOp(ctx context.Context, op Op) {
	_ = d.fn(ctx, op, func(context.Context) error { return nil })
}

// RetryableError delegates to the adapter (for RunInTx).
func (d instrumented) RetryableError(err error) bool {
	if rc, ok := d.inner.(retryClassifier); ok {
		return rc.RetryableError(err)
	}
	return false
}

func (d instrumented) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	var rows Rows
	err := d.fn(ctx, Op{Kind: "query", SQL: sql, Args: args}, func(ctx context.Context) error {
		var err error
		rows, err = d.inner.Query(ctx, sql, args...)
		return err
	})
	return rows, err
}

func (d instrumented) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	var n int64
	err := d.fn(ctx, Op{Kind: "exec", SQL: sql, Args: args}, func(ctx context.Context) error {
		var err error
		n, err = d.inner.Exec(ctx, sql, args...)
		return err
	})
	return n, err
}

func (d instrumented) ExecBatch(ctx context.Context, items []BatchItem) error {
	op := Op{Kind: "batch", Statements: make([]string, len(items))}
	for i, it := range items {
		op.Statements[i] = it.SQL
	}
	if len(items) > 0 {
		op.SQL = items[0].SQL
	}
	return d.fn(ctx, op, func(ctx context.Context) error {
		return d.inner.ExecBatch(ctx, items)
	})
}

func (d instrumented) Begin(ctx context.Context) (Tx, error) {
	var tx Tx
	err := d.fn(ctx, Op{Kind: "begin"}, func(ctx context.Context) error {
		var err error
		tx, err = d.inner.Begin(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return instrumentedTx{instrumented{inner: tx, fn: d.fn}, tx}, nil
}

type instrumentedTx struct {
	instrumented
	tx Tx
}

func (t instrumentedTx) Commit(ctx context.Context) error {
	return t.fn(ctx, Op{Kind: "commit"}, func(ctx context.Context) error {
		return t.tx.Commit(ctx)
	})
}

func (t instrumentedTx) Rollback(ctx context.Context) error {
	return t.fn(ctx, Op{Kind: "rollback"}, func(ctx context.Context) error {
		return t.tx.Rollback(ctx)
	})
}
