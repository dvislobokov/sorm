package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// Advisory lock held while migrations are applied: several application
// replicas starting at once will not apply the same file twice or fight
// over DDL. The lock is held on a dedicated connection (session-scoped).
//
// SQLite needs no advisory lock: the database is a local file, and
// concurrent writers are serialized by SQLite itself.

// lockKey is the shared lock key for sorm migrations ("sorm" in hex).
const lockKey int64 = 0x736F726D

func withMigrationLock(ctx context.Context, db *sql.DB, dialect string, fn func() error) error {
	switch dialect {
	case "postgres":
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: lock conn: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: pg_advisory_lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey)
		return fn()

	case "mysql":
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: lock conn: %w", err)
		}
		defer conn.Close()
		var got int
		if err := conn.QueryRowContext(ctx,
			"SELECT GET_LOCK('sorm_migrations', 300)").Scan(&got); err != nil {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: GET_LOCK: %w", err)
		}
		if got != 1 {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: GET_LOCK timeout — another migration has held the lock for over 300s")
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK('sorm_migrations')")
		return fn()

	default: // sqlite
		return fn()
	}
}
