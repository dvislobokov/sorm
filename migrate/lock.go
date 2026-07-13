package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Advisory lock held while migrations are applied: several application
// replicas starting at once will not apply the same file twice or fight
// over DDL. The lock is held on a dedicated connection (session-scoped).
//
// SQLite needs no advisory lock: the database is a local file, and
// concurrent writers are serialized by SQLite itself.
//
// CockroachDB has no advisory locks at all — a lease row in the
// sorm_migration_lock table serializes replicas there (see withLeaseLock).

// lockKey is the shared lock key for sorm migrations ("sorm" in hex).
const lockKey int64 = 0x736F726D

// LockTable is the lease-lock table used on engines without advisory
// locks (CockroachDB).
const LockTable = "sorm_migration_lock"

func withMigrationLock(ctx context.Context, db *sql.DB, dialect string, fn func() error) error {
	switch dialect {
	case "postgres":
		conn, err := db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: lock conn: %w", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
			// CockroachDB speaks the PG protocol but has no advisory locks
			// (SQLSTATE 42883, unknown function) — fall back to a lease row.
			if strings.Contains(err.Error(), "42883") || strings.Contains(err.Error(), "unknown function") {
				return withLeaseLock(ctx, db, fn)
			}
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

// withLeaseLock serializes via a single-row lease: the holder writes its
// expiry, competitors poll until the row is free or the lease expires
// (crash protection — a dead holder's lease times out).
func withLeaseLock(ctx context.Context, db *sql.DB, fn func() error) error {
	const lease = 5 * time.Minute
	const poll = 500 * time.Millisecond

	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS `+LockTable+` (id INT PRIMARY KEY, expires_at TIMESTAMPTZ NOT NULL)`); err != nil {
		return fmt.Errorf("github.com/dvislobokov/sorm/migrate: lease table: %w", err)
	}

	deadline := time.Now().Add(lease)
	for {
		// Acquire: insert the lease row, or take over an expired one.
		res, err := db.ExecContext(ctx,
			`INSERT INTO `+LockTable+` (id, expires_at) VALUES (1, now() + INTERVAL '5 minutes')
			 ON CONFLICT (id) DO UPDATE SET expires_at = now() + INTERVAL '5 minutes'
			 WHERE `+LockTable+`.expires_at < now()`)
		if err != nil {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: lease acquire: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			break // lease is ours
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: lease timeout — another migration has held the lock for over %s", lease)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	defer db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM `+LockTable+` WHERE id = 1`)
	return fn()
}
