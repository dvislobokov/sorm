package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// seedPrefix marks seed records in the history table, keeping them apart
// from migration file names.
const seedPrefix = "seed:"

// Seed executes a named one-time data seed. The name is recorded in the
// sorm_migrations history as "seed:<name>" — subsequent calls (new
// deploys, other replicas) are no-ops; concurrent callers serialize on
// the same advisory lock as migrations. fn runs inside a transaction
// together with the history record: either the seed and its record
// commit, or neither.
//
//	err := migrate.Seed(ctx, sdb, "postgres", "default-admin",
//	    func(ctx context.Context, tx *sql.Tx) error {
//	        _, err := tx.ExecContext(ctx,
//	            `INSERT INTO users (email, name, active, created_at, version)
//	             VALUES ($1, $2, true, now(), 1)`, "admin@corp.io", "Admin")
//	        return err
//	    })
//
// Rename the seed to run a new version of it; data mutations of an
// already-run seed belong in a new seed with a new name.
func Seed(ctx context.Context, db *sql.DB, dialect, name string, fn func(ctx context.Context, tx *sql.Tx) error) error {
	if name == "" {
		return fmt.Errorf("github.com/dvislobokov/sorm/migrate: seed name is required")
	}
	version := seedPrefix + sanitizeName(name)
	return withMigrationLock(ctx, db, dialect, func() error {
		if err := ensureHistory(ctx, db, dialect); err != nil {
			return err
		}
		applied, err := appliedVersions(ctx, db)
		if err != nil {
			return err
		}
		if applied[version] {
			return nil
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := fn(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: seed %q: %w", name, err)
		}
		record := fmt.Sprintf("INSERT INTO %s (version) VALUES (%s)", HistoryTable, placeholder(dialect, 1))
		if _, err := tx.ExecContext(ctx, record, version); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

// SeedApplied reports whether the named seed has already run.
func SeedApplied(ctx context.Context, db *sql.DB, dialect, name string) (bool, error) {
	if err := ensureHistory(ctx, db, dialect); err != nil {
		return false, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return false, err
	}
	return applied[seedPrefix+sanitizeName(name)], nil
}

// isSeedVersion filters seed records out of file-oriented walks (Down).
func isSeedVersion(v string) bool { return strings.HasPrefix(v, seedPrefix) }
