// Package sormtest is the testing toolkit for projects built on sorm.
//
// The pyramid it supports (do NOT mock sorm.DB — you would be testing
// the mock, not your queries):
//
//  1. Query construction — AssertSQL, no database at all.
//  2. Data-access code — NewSQLite: an in-memory database with the real
//     schema of your registered models (import your sormgen package).
//  3. Dialect-specific code (PG arrays, pgagg, ForUpdate, JSON operators)
//     — NewPostgres: one shared PostgreSQL, an isolated schema per test
//     (sorm.InSchema), safe under t.Parallel().
//  4. Business logic — mock your own repository interfaces; sorm is not
//     involved.
//
// Plus Load (YAML fixtures, FK-ordered) and CountQueries (N+1 assertions).
package sormtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect/lite"
	"github.com/dvislobokov/sorm/driver/pgxd"
	"github.com/dvislobokov/sorm/driver/sqld"
	"github.com/dvislobokov/sorm/migrate"
)

// NewSQLite returns an in-memory SQLite database with the schema of every
// registered entity applied (import your generated sormgen package for
// its side effect). Closed automatically when the test ends.
//
// Millisecond-fast and safe under t.Parallel() — every call is a private
// database. PostgreSQL-only features (array columns, pgagg, ForUpdate)
// fail here with explicit errors: move those tests to NewPostgres.
func NewSQLite(t testing.TB) sorm.DB {
	t.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sormtest: open sqlite: %v", err)
	}
	sdb.SetMaxOpenConns(1) // :memory: lives in a single connection
	t.Cleanup(func() { sdb.Close() })

	if err := migrate.Apply(context.Background(), sdb, "sqlite"); err != nil {
		t.Fatalf("sormtest: apply schema: %v", err)
	}
	return sqld.Wrap(sdb, lite.Dialect{})
}

// NewPostgres returns a database bound to a FRESH schema inside the
// PostgreSQL pointed to by SORM_TEST_DSN (the test is skipped when the
// variable is unset). Many tests share one server without seeing each
// other — isolation comes from sorm.InSchema — so t.Parallel() is safe.
// The schema is dropped when the test ends.
func NewPostgres(t testing.TB) sorm.DB {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_DSN")
	if dsn == "" {
		t.Skip("sormtest: SORM_TEST_DSN not set — PostgreSQL tests skipped")
	}

	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	schema := "sormtest_" + hex.EncodeToString(buf[:])

	ctx := context.Background()
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sormtest: open postgres: %v", err)
	}
	if _, err := sdb.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		sdb.Close()
		t.Fatalf("sormtest: create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = sdb.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		sdb.Close()
	})
	if err := migrate.Apply(ctx, sdb, "postgres", migrate.WithSchema(schema)); err != nil {
		t.Fatalf("sormtest: apply schema: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("sormtest: pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return sorm.InSchema(pgxd.Wrap(pool), schema)
}

// AssertSQL renders a builder (Query/Update/Delete/Upsert/Project — any
// value with a ToSQL method) and compares the SQL exactly; arguments are
// compared when wantArgs are given.
func AssertSQL(t testing.TB, q any, wantSQL string, wantArgs ...any) {
	t.Helper()
	var gotSQL string
	var gotArgs []any
	switch b := q.(type) {
	case interface{ ToSQL() (string, []any) }:
		gotSQL, gotArgs = b.ToSQL()
	case interface{ ToSQL() (string, []any, error) }:
		var err error
		gotSQL, gotArgs, err = b.ToSQL()
		if err != nil {
			t.Fatalf("sormtest: ToSQL: %v", err)
		}
	default:
		t.Fatalf("sormtest: %T has no ToSQL method", q)
	}
	if gotSQL != wantSQL {
		t.Fatalf("sormtest: SQL mismatch\n got:  %s\n want: %s", gotSQL, wantSQL)
	}
	if len(wantArgs) == 0 {
		return
	}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("sormtest: args mismatch\n got:  %v\n want: %v", gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Fatalf("sormtest: arg %d mismatch: got %v (%T), want %v (%T)",
				i+1, gotArgs[i], gotArgs[i], wantArgs[i], wantArgs[i])
		}
	}
}
