package sorm

import (
	"context"

	"sorm/dialect"
)

// DB — драйверная абстракция sorm. Рантайм не зависит от конкретного
// драйвера: адаптеры — sorm/driver/pgxd (pgx, PostgreSQL) и
// sorm/driver/sqld (database/sql: MySQL, SQLite).
type DB interface {
	Dialect() dialect.Dialect
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	// Exec возвращает число затронутых строк.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	// ExecBatch исполняет статименты по порядку: pgx — одним roundtrip
	// (pgx.Batch), database/sql — последовательно в текущем соединении.
	ExecBatch(ctx context.Context, items []BatchItem) error
	Begin(ctx context.Context) (Tx, error)
}

// Tx — транзакция; реализует DB (запросы/батчи внутри транзакции).
type Tx interface {
	DB
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Rows — минимальный курсор результата, общий для pgx и database/sql.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
	Columns() []string
}

// BatchItem — один статимент батча записи.
type BatchItem struct {
	SQL  string
	Args []any
	// IDCount > 0: multi-row INSERT с auto-PK на IDCount строк. Адаптер
	// добывает идентификаторы через RETURNING (диалект умеет) или
	// арифметику LastInsertId (MySQL — первый id батча, SQLite — последний)
	// и вызывает OnIDs со срезом длины IDCount в порядке строк VALUES.
	IDCount int
	OnIDs   func(ids []int64)
	// Check вызывается с числом затронутых строк (optimistic concurrency).
	Check func(rowsAffected int64) error
}
