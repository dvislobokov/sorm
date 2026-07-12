package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Versioned file migrations — no external tools and no docker:
// a directory of *.sql files + the sorm_migrations history table in the
// target database.
//
// Diff generation uses replay: existing migrations are replayed on an
// EMPTY scratch database provided by the user (for SQLite ":memory:" is
// enough), then its state is compared with the registered models, and the
// difference is written as a new <UTC-timestamp>_<name>.sql file.

// HistoryTable is the table tracking applied migrations in the target database.
const HistoryTable = "sorm_migrations"

// Diff generates a new versioned migration file in dir.
// dev is an empty throwaway database of the same dialect (the replay target);
// its contents are effectively consumed: after the call it is in the "all
// migrations applied" state. Returns the name of the created file, or "" if
// there are no changes.
func Diff(ctx context.Context, dev *sql.DB, dialect, dir, name string) (string, error) {
	if _, err := Up(ctx, dev, dialect, dir); err != nil {
		return "", fmt.Errorf("github.com/dvislobokov/sorm/migrate: replay on dev db: %w", err)
	}
	drv, changes, err := diff(ctx, dev, dialect, nil)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}
	plan, err := drv.PlanChanges(ctx, name, changes)
	if err != nil {
		return "", fmt.Errorf("github.com/dvislobokov/sorm/migrate: plan: %w", err)
	}

	var up strings.Builder
	fmt.Fprintf(&up, "-- sorm migration: %s\n", name)
	var downStmts []string
	downComplete := true
	for _, c := range plan.Changes {
		if c.Comment != "" {
			fmt.Fprintf(&up, "-- %s\n", c.Comment)
		}
		up.WriteString(strings.TrimRight(c.Cmd, ";"))
		up.WriteString(";\n")

		rev, err := c.ReverseStmts()
		if err != nil || len(rev) == 0 {
			downComplete = false
			continue
		}
		downStmts = append(downStmts, rev...)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	version := time.Now().UTC().Format("20060102150405") + "_" + sanitizeName(name)
	fname := version + ".sql"
	if err := os.WriteFile(filepath.Join(dir, fname), []byte(up.String()), 0o644); err != nil {
		return "", err
	}

	// Down file: reversals in reverse order. If Atlas could not reverse
	// even one change, we skip the down file entirely (more honest than a
	// partial rollback).
	if downComplete && len(downStmts) > 0 {
		var down strings.Builder
		fmt.Fprintf(&down, "-- sorm migration (down): %s\n", name)
		for i := len(downStmts) - 1; i >= 0; i-- {
			down.WriteString(strings.TrimRight(downStmts[i], ";"))
			down.WriteString(";\n")
		}
		if err := os.WriteFile(filepath.Join(dir, version+".down.sql"), []byte(down.String()), 0o644); err != nil {
			return "", err
		}
	}

	if err := WriteSum(dir); err != nil {
		return "", err
	}
	return fname, nil
}

// Down reverts the last steps applied migrations using their *.down.sql
// files (newest first) and removes the records from HistoryTable. A migration
// without a down file stops the rollback with an error.
func Down(ctx context.Context, db *sql.DB, dialect, dir string, steps int) ([]string, error) {
	var out []string
	err := withMigrationLock(ctx, db, dialect, func() error {
		reverted, err := down(ctx, db, dialect, dir, steps)
		out = reverted
		return err
	})
	return out, err
}

func down(ctx context.Context, db *sql.DB, dialect, dir string, steps int) ([]string, error) {
	if err := VerifySum(dir); err != nil {
		return nil, err
	}
	if err := ensureHistory(ctx, db, dialect); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	var versions []string
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))

	var reverted []string
	for _, v := range versions {
		if len(reverted) == steps {
			break
		}
		downFile := strings.TrimSuffix(v, ".sql") + ".down.sql"
		content, err := os.ReadFile(filepath.Join(dir, downFile))
		if err != nil {
			return reverted, fmt.Errorf("github.com/dvislobokov/sorm/migrate: no down file for %s: %w", v, err)
		}
		if err := revertFile(ctx, db, dialect, v, string(content)); err != nil {
			return reverted, fmt.Errorf("github.com/dvislobokov/sorm/migrate: %s: %w", downFile, err)
		}
		reverted = append(reverted, v)
	}
	return reverted, nil
}

func revertFile(ctx context.Context, db *sql.DB, dialect, version, content string) error {
	stmts := SplitStatements(content)
	record := fmt.Sprintf("DELETE FROM %s WHERE version = %s", HistoryTable, placeholder(dialect, 1))

	if dialect == "mysql" {
		for _, s := range stmts {
			if _, err := db.ExecContext(ctx, s); err != nil {
				return err
			}
		}
		_, err := db.ExecContext(ctx, record, version)
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, record, version); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Up applies to db all not-yet-applied migrations from dir (in file name
// order) and records them in HistoryTable. Returns the applied files.
// On PostgreSQL and SQLite each file is applied in a transaction; MySQL
// commits DDL implicitly, so the file is executed statement by statement.
//
// Concurrent calls (several replicas starting up) are serialized with an
// advisory lock — a file will not be applied twice.
func Up(ctx context.Context, db *sql.DB, dialect, dir string) ([]string, error) {
	var out []string
	err := withMigrationLock(ctx, db, dialect, func() error {
		applied, err := up(ctx, db, dialect, dir)
		out = applied
		return err
	})
	return out, err
}

func up(ctx context.Context, db *sql.DB, dialect, dir string) ([]string, error) {
	if err := VerifySum(dir); err != nil {
		return nil, err
	}
	if err := ensureHistory(ctx, db, dialect); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}

	files, err := migrationFiles(dir)
	if err != nil {
		return nil, err
	}

	var done []string
	for _, f := range files {
		if applied[f] {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return done, err
		}
		if err := applyFile(ctx, db, dialect, f, string(content)); err != nil {
			return done, fmt.Errorf("github.com/dvislobokov/sorm/migrate: %s: %w", f, err)
		}
		done = append(done, f)
	}
	return done, nil
}

// Pending returns the files Up would apply (without applying them).
func Pending(ctx context.Context, db *sql.DB, dialect, dir string) ([]string, error) {
	if err := VerifySum(dir); err != nil {
		return nil, err
	}
	if err := ensureHistory(ctx, db, dialect); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	files, err := migrationFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		if !applied[f] {
			out = append(out, f)
		}
	}
	return out, nil
}

func applyFile(ctx context.Context, db *sql.DB, dialect, file, content string) error {
	stmts := SplitStatements(content)
	record := fmt.Sprintf(
		"INSERT INTO %s (version) VALUES (%s)",
		HistoryTable, placeholder(dialect, 1),
	)

	if dialect == "mysql" {
		// DDL in MySQL commits implicitly — a transaction is pointless.
		for _, s := range stmts {
			if _, err := db.ExecContext(ctx, s); err != nil {
				return err
			}
		}
		_, err := db.ExecContext(ctx, record, file)
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, record, file); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func ensureHistory(ctx context.Context, db *sql.DB, dialect string) error {
	var ddl string
	switch dialect {
	case "mysql":
		ddl = "CREATE TABLE IF NOT EXISTS " + HistoryTable +
			" (`version` VARCHAR(255) PRIMARY KEY, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)"
	default: // postgres, sqlite
		ddl = "CREATE TABLE IF NOT EXISTS " + HistoryTable +
			` ("version" VARCHAR(255) PRIMARY KEY, "applied_at" TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`
	}
	_, err := db.ExecContext(ctx, ddl)
	if err != nil {
		return fmt.Errorf("github.com/dvislobokov/sorm/migrate: history table: %w", err)
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM "+HistoryTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

func migrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil // no directory = no migrations
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if strings.HasSuffix(e.Name(), ".down.sql") {
			continue // down files are applied by Down, not Up
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	return files, nil
}

// SplitStatements parses a *.sql migration file: comment lines are
// discarded, statements are separated by ";". The files are written by Diff —
// one statement per line; literal ";" inside strings is not supported.
func SplitStatements(content string) []string {
	var stmts []string
	for _, chunk := range strings.Split(content, ";") {
		var keep []string
		for _, ln := range strings.Split(chunk, "\n") {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "--") {
				continue
			}
			keep = append(keep, ln)
		}
		if stmt := strings.TrimSpace(strings.Join(keep, "\n")); stmt != "" {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}

func placeholder(dialect string, n int) string {
	if dialect == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

var nameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func sanitizeName(s string) string {
	s = nameSanitizer.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}
