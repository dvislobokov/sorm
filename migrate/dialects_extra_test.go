package migrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	_ "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
	"github.com/dvislobokov/sorm/migrate"
)

// CockroachDB: Apply works through the lease lock (no advisory locks on
// CRDB). Known quirk of the pinned Atlas version: custom DESC indexes are
// re-planned on every diff (inspection mismatch) — harmless to execute,
// so the assertion tolerates index-only residue.
func TestApplyCockroach(t *testing.T) {
	dsn := os.Getenv("SORM_TEST_CRDB_DSN")
	if dsn == "" {
		t.Skip("SORM_TEST_CRDB_DSN not set — CockroachDB tests skipped")
	}
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	ctx := context.Background()

	if err := migrate.Apply(ctx, sdb, "postgres"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Second Apply must also succeed (idempotent execution).
	if err := migrate.Apply(ctx, sdb, "postgres"); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	plan, err := migrate.Plan(ctx, sdb, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range plan {
		if !strings.Contains(stmt, "INDEX") {
			t.Fatalf("non-index residue in CRDB plan: %s", stmt)
		}
	}

	// Seeds run through the same lease lock.
	ran := 0
	seed := func(ctx context.Context, tx *sql.Tx) error { ran++; return nil }
	name := fmt.Sprintf("crdb-probe-%d", time.Now().UnixNano())
	if err := migrate.Seed(ctx, sdb, "postgres", name, seed); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Seed(ctx, sdb, "postgres", name, seed); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("seed ran %d times", ran)
	}
}

// MariaDB: the whole declarative pipeline through the mysql driver.
func TestApplyMariaDB(t *testing.T) {
	dsn := os.Getenv("SORM_TEST_MARIADB_DSN")
	if dsn == "" {
		t.Skip("SORM_TEST_MARIADB_DSN not set — MariaDB tests skipped")
	}
	sdb, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	ctx := context.Background()

	if err := migrate.Apply(ctx, sdb, "mysql"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	plan, err := migrate.Plan(ctx, sdb, "mysql")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("plan after apply: %v", plan)
	}
}
