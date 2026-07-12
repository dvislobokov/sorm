package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver in database/sql
	_ "modernc.org/sqlite"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect/lite"
	"github.com/dvislobokov/sorm/driver/sqld"
	"github.com/dvislobokov/sorm/migrate"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

var _ = gen.User // importing sormgen registers the TableDefs

func sqliteDB(t *testing.T) *sql.DB {
	t.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	t.Cleanup(func() { sdb.Close() })
	return sdb
}

func TestApplySQLiteFromScratch(t *testing.T) {
	ctx := context.Background()
	sdb := sqliteDB(t)

	// 1. Empty DB → Apply creates the schema from the registered models.
	if err := migrate.Apply(ctx, sdb, "sqlite"); err != nil {
		t.Fatal(err)
	}

	// 2. The schema works: end-to-end session scenario.
	db := sqld.Wrap(sdb, lite.Dialect{})
	s := sorm.NewSession(db)
	u := &models.User{Email: "m@b.c", Name: "Mig", Active: true, Age: 20}
	p := &models.Post{Author: u, Title: "migrated", Body: "b"}
	sorm.Add(s, u)
	sorm.Add(s, p)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || p.AuthorID != u.ID {
		t.Fatalf("insert after Apply: id=%d fk=%d", u.ID, p.AuthorID)
	}

	// 3. Idempotency: a repeated Plan is empty.
	plan, err := migrate.Plan(ctx, sdb, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("repeated Plan is not empty (phantom diff):\n%s", strings.Join(plan, "\n"))
	}
}

func TestPlanDetectsDrift(t *testing.T) {
	ctx := context.Background()
	sdb := sqliteDB(t)

	if err := migrate.Apply(ctx, sdb, "sqlite"); err != nil {
		t.Fatal(err)
	}
	// Drift: drop a column by hand.
	if _, err := sdb.Exec(`ALTER TABLE users DROP COLUMN nickname`); err != nil {
		t.Fatal(err)
	}

	plan, err := migrate.Plan(ctx, sdb, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "nickname") {
		t.Fatalf("Plan did not detect the drift:\n%s", joined)
	}
	// Apply fixes the drift.
	if err := migrate.Apply(ctx, sdb, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if plan, _ = migrate.Plan(ctx, sdb, "sqlite"); len(plan) != 0 {
		t.Fatalf("diff is not empty after the fix: %v", plan)
	}
}

// pgDSN provides a separate database for migrate tests: the sorm and
// sorm/migrate packages run in parallel under go test and must not share
// one database.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_DSN")
	if dsn == "" {
		t.Skip("SORM_TEST_DSN is not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	_, _ = admin.Exec(`CREATE DATABASE sorm_migrate_test`) // an "exists" error is ignored

	// replace the database name in the DSN
	i := strings.LastIndex(dsn, "/")
	rest := dsn[i+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		return dsn[:i+1] + "sorm_migrate_test" + rest[q:]
	}
	return dsn[:i+1] + "sorm_migrate_test"
}

func TestApplyPostgres(t *testing.T) {
	dsn := pgDSN(t)
	ctx := context.Background()

	sdb, err := sql.Open("pgx", dsn) // database/sql on top of pgx (stdlib adapter)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()

	for _, q := range []string{`DROP TABLE IF EXISTS comments`, `DROP TABLE IF EXISTS devices`, `DROP TABLE IF EXISTS profiles`, `DROP TABLE IF EXISTS user_tags`, `DROP TABLE IF EXISTS tags`, `DROP TABLE IF EXISTS api_keys`, `DROP TABLE IF EXISTS posts`, `DROP TABLE IF EXISTS users`} {
		if _, err := sdb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	if err := migrate.Apply(ctx, sdb, "postgres"); err != nil {
		t.Fatal(err)
	}
	plan, err := migrate.Plan(ctx, sdb, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("PG: repeated Plan is not empty:\n%s", strings.Join(plan, "\n"))
	}
}
