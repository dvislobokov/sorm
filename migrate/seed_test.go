package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dvislobokov/sorm/migrate"
)

// Seed: runs once, records in history, rolls back atomically with its
// record, and never breaks Down.
func TestSeed(t *testing.T) {
	ctx := context.Background()
	db := sqliteDB(t)
	if _, err := db.Exec(`CREATE TABLE things (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	runs := 0
	seedFn := func(ctx context.Context, tx *sql.Tx) error {
		runs++
		_, err := tx.ExecContext(ctx, `INSERT INTO things (name) VALUES ('a'), ('b')`)
		return err
	}
	if err := migrate.Seed(ctx, db, "sqlite", "initial things", seedFn); err != nil {
		t.Fatal(err)
	}
	// Second call is a no-op: fn must not run again.
	if err := migrate.Seed(ctx, db, "sqlite", "initial things", seedFn); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("seed ran %d times, want 1", runs)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM things`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d (err=%v)", n, err)
	}

	ok, err := migrate.SeedApplied(ctx, db, "sqlite", "initial things")
	if err != nil || !ok {
		t.Fatalf("SeedApplied = %v (err=%v)", ok, err)
	}

	// A failing seed rolls back both the data and the history record.
	boom := errors.New("boom")
	err = migrate.Seed(ctx, db, "sqlite", "broken", func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO things (name) VALUES ('c')`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM things`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rollback failed: rows = %d (err=%v)", n, err)
	}
	if ok, _ := migrate.SeedApplied(ctx, db, "sqlite", "broken"); ok {
		t.Fatal("failed seed must not be recorded")
	}
	// After a fix it runs.
	err = migrate.Seed(ctx, db, "sqlite", "broken", func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO things (name) VALUES ('c')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Down must ignore seed records instead of demanding a .down.sql.
	if _, err := migrate.Down(ctx, db, "sqlite", t.TempDir(), 1); err != nil {
		t.Fatalf("Down over seed history: %v", err)
	}
}
