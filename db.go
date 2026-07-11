package sorm

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB — минимальная поверхность исполнения запросов. Ей структурно удовлетворяют
// *pgxpool.Pool, *pgx.Conn и pgx.Tx: и запросы, и сессии работают поверх любого из них.
// Адаптер database/sql для MySQL/SQLite появится вместе с мультидиалектностью.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// txBeginner — то, из чего Session умеет открывать транзакцию
// (*pgxpool.Pool, *pgx.Conn; pgx.Tx даёт вложенный savepoint).
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}
