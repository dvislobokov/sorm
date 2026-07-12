package sorm

import (
	"context"

	"github.com/dvislobokov/sorm/dialect"
)

// Op — описание операции для инструментирования.
type Op struct {
	Kind string // "query" | "exec" | "batch" | "begin" | "commit" | "rollback"
	SQL  string // текст запроса ("" для begin/commit/rollback; первый статимент для batch)
	Args []any
	// Statements — все статименты батча (Kind == "batch").
	Statements []string
}

// InstrumentFunc оборачивает каждую операцию БД: логирование, метрики,
// трейсинг (OpenTelemetry-спан вокруг next). Обязана вызвать next ровно
// один раз и вернуть его ошибку (можно обёрнутую).
type InstrumentFunc func(ctx context.Context, op Op, next func(ctx context.Context) error) error

// Instrument оборачивает DB middleware-функцией. Работает с любым
// адаптером; транзакции инструментируются тем же fn.
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

// RetryableError делегируется адаптеру (для RunInTx).
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
