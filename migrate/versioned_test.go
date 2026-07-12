package migrate_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect/lite"
	"github.com/dvislobokov/sorm/driver/sqld"
	"github.com/dvislobokov/sorm/migrate"
	models "github.com/dvislobokov/sorm/internal/testmodels"
)

func TestVersionedFullCycle(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "migrations")

	// 1. Diff on an empty directory: scratch DB = SQLite in-memory (no docker).
	dev := sqliteDB(t)
	fname, err := migrate.Diff(ctx, dev, "sqlite", dir, "init schema")
	if err != nil {
		t.Fatal(err)
	}
	if fname == "" || !strings.HasSuffix(fname, "_init_schema.sql") {
		t.Fatalf("fname = %q", fname)
	}
	content, _ := os.ReadFile(filepath.Join(dir, fname))
	if !strings.Contains(string(content), "CREATE TABLE") {
		t.Fatalf("migration has no CREATE TABLE:\n%s", content)
	}

	// 2. Up applies the file to the target DB; a repeated Up is a no-op.
	target := sqliteDB(t)
	applied, err := migrate.Up(ctx, target, "sqlite", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != fname {
		t.Fatalf("applied = %v", applied)
	}
	if again, _ := migrate.Up(ctx, target, "sqlite", dir); len(again) != 0 {
		t.Fatalf("repeated Up applied %v", again)
	}

	// 3. The schema works: a session succeeds.
	db := sqld.Wrap(target, lite.Dialect{})
	s := sorm.NewSession(db)
	u := &models.User{Email: "v@b.c", Name: "Ver", Active: true}
	sorm.Add(s, u)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// 4. Repeated Diff (replay on a fresh scratch DB) — no changes.
	dev2 := sqliteDB(t)
	fname2, err := migrate.Diff(ctx, dev2, "sqlite", dir, "noop")
	if err != nil {
		t.Fatal(err)
	}
	if fname2 != "" {
		t.Fatalf("phantom migration %s", fname2)
	}

	// 5. Schema evolution: emulate adding a table to the models.
	sorm.RegisterTable(sorm.TableDef{
		Name: "labels",
		Columns: []sorm.ColumnDef{
			{Name: "id", GoKind: "int64", PK: true, Auto: true},
			{Name: "title", GoKind: "string", Unique: true},
		},
	})
	t.Cleanup(func() { sorm.UnregisterTable("labels") })

	dev3 := sqliteDB(t)
	fname3, err := migrate.Diff(ctx, dev3, "sqlite", dir, "add labels")
	if err != nil {
		t.Fatal(err)
	}
	if fname3 == "" {
		t.Fatal("diff did not detect the new table")
	}
	content3, _ := os.ReadFile(filepath.Join(dir, fname3))
	if !strings.Contains(string(content3), "labels") || strings.Contains(string(content3), "users") {
		t.Fatalf("second migration must contain only labels:\n%s", content3)
	}

	// 6. Pending sees the new file; Up brings the target DB up to date.
	pending, err := migrate.Pending(ctx, target, "sqlite", dir)
	if err != nil || len(pending) != 1 || pending[0] != fname3 {
		t.Fatalf("pending = %v, err=%v", pending, err)
	}
	if applied, err := migrate.Up(ctx, target, "sqlite", dir); err != nil || len(applied) != 1 {
		t.Fatalf("up: %v %v", applied, err)
	}
	if _, err := target.Exec(`INSERT INTO labels (title) VALUES ('go')`); err != nil {
		t.Fatalf("labels table was not created: %v", err)
	}
}

func TestSplitStatements(t *testing.T) {
	in := "-- comment\nCREATE TABLE a (x INT);\n-- another\nCREATE TABLE b (y TEXT);\n"
	got := migrate.SplitStatements(in)
	if len(got) != 2 || !strings.HasPrefix(got[0], "CREATE TABLE a") || !strings.HasPrefix(got[1], "CREATE TABLE b") {
		t.Fatalf("got %#v", got)
	}
}
