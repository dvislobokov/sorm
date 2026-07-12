package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// Advisory lock на время применения миграций: несколько реплик приложения,
// стартующих одновременно, не применят один файл дважды и не подерутся
// за DDL. Замок держится на выделенном соединении (session-scoped).
//
// SQLite не нуждается в advisory lock: база — локальный файл, конкурентные
// писатели сериализуются самим SQLite.

// lockKey — общий ключ замка sorm-миграций ("sorm" в hex).
const lockKey int64 = 0x736F726D

func withMigrationLock(ctx context.Context, db *sql.DB, dialect string, fn func() error) error {
	switch dialect {
	case "postgres":
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("sorm/migrate: lock conn: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
			return fmt.Errorf("sorm/migrate: pg_advisory_lock: %w", err)
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey)
		return fn()

	case "mysql":
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("sorm/migrate: lock conn: %w", err)
		}
		defer conn.Close()
		var got int
		if err := conn.QueryRowContext(ctx,
			"SELECT GET_LOCK('sorm_migrations', 300)").Scan(&got); err != nil {
			return fmt.Errorf("sorm/migrate: GET_LOCK: %w", err)
		}
		if got != 1 {
			return fmt.Errorf("sorm/migrate: GET_LOCK timeout — другая миграция держит замок дольше 300s")
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK('sorm_migrations')")
		return fn()

	default: // sqlite
		return fn()
	}
}
