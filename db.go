package sorm

import (
	"context"

	"github.com/dvislobokov/sorm/dialect"
)

// DB — sorm's driver abstraction. The runtime does not depend on a specific
// driver: the adapters are sorm/driver/pgxd (pgx, PostgreSQL) and
// sorm/driver/sqld (database/sql: MySQL, SQLite).
type DB interface {
	Dialect() dialect.Dialect
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	// Exec returns the number of affected rows.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	// ExecBatch executes statements in order: pgx — in one roundtrip
	// (pgx.Batch), database/sql — sequentially on the current connection.
	ExecBatch(ctx context.Context, items []BatchItem) error
	Begin(ctx context.Context) (Tx, error)
}

// Tx — a transaction; implements DB (queries/batches inside the transaction).
type Tx interface {
	DB
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Rows — a minimal result cursor shared by pgx and database/sql.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
	Columns() []string
}

// BatchItem — a single statement of a write batch.
type BatchItem struct {
	SQL  string
	Args []any
	// IDCount > 0: a multi-row INSERT with auto-PK for IDCount rows. The
	// adapter obtains the ids via RETURNING (if the dialect supports it) or
	// LastInsertId arithmetic (MySQL — first id of the batch, SQLite — last)
	// and calls OnIDs with a slice of length IDCount in VALUES row order.
	IDCount int
	OnIDs   func(ids []int64)
	// Check is called with the number of affected rows (optimistic concurrency).
	Check func(rowsAffected int64) error
}
