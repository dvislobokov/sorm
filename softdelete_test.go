package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// User.DeletedAt carries sorm:"softDelete": queries see alive rows only,
// Remove/Delete become UPDATEs stamping the column, Hard() really deletes.

func TestSoftDeleteSQL(t *testing.T) {
	// The implicit filter in reads...
	sql, _ := sorm.Query[models.User](nil).Where(u.Active.Eq(true)).ToSQL()
	if !strings.Contains(sql, `"deleted_at" IS NULL`) {
		t.Fatalf("select: %s", sql)
	}
	// ...lifted by WithDeleted, inverted by OnlyDeleted.
	sql, _ = sorm.Query[models.User](nil).WithDeleted().ToSQL()
	if strings.Contains(sql, `"deleted_at" IS NULL`) {
		t.Fatalf("with deleted: %s", sql)
	}
	sql, _ = sorm.Query[models.User](nil).OnlyDeleted().ToSQL()
	if !strings.Contains(sql, `"deleted_at" IS NOT NULL`) {
		t.Fatalf("only deleted: %s", sql)
	}

	// Set-based DELETE renders as an UPDATE over alive rows (+version bump).
	sql, _, err := sorm.Delete[models.User](nil).Where(u.Active.Eq(false)).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`UPDATE "users" SET "deleted_at" = $1`, `"version" = "version" + 1`, `"deleted_at" IS NULL`} {
		if !strings.Contains(sql, want) {
			t.Fatalf("soft delete sql:\n%s\nmissing: %s", sql, want)
		}
	}
	// Hard() is a real DELETE.
	sql, _, _ = sorm.Delete[models.User](nil).Where(u.ID.Eq(1)).Hard().ToSQL()
	if !strings.HasPrefix(sql, `DELETE FROM "users"`) {
		t.Fatalf("hard delete sql: %s", sql)
	}

	// EXISTS через отношение фильтрует удалённых родителей.
	sql, _ = sorm.Query[models.Post](nil).Where(gen.Post.Author.Is(u.Active.Eq(true))).ToSQL()
	if !strings.Contains(sql, `"users"."deleted_at" IS NULL`) &&
		!strings.Contains(sql, `"deleted_at" IS NULL`) {
		t.Fatalf("exists: %s", sql)
	}
}

func runSoftDeleteScenario(t *testing.T, db sorm.DB, dialect string) {
	t.Helper()
	ctx := context.Background()
	c := gen.NewContext(db)

	alive := &models.User{Email: "sd-alive@" + dialect, Name: "Alive", Active: true, CreatedAt: nowNoZero()}
	gone := &models.User{Email: "sd-gone@" + dialect, Name: "Gone", Active: true, CreatedAt: nowNoZero()}
	c.Users.Add(alive, gone)
	c.Posts.Add(&models.Post{Author: gone, Title: "orphaned"})
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// Session Remove → soft delete: entity stamped in memory, row kept.
	c.Users.Remove(gone)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if gone.DeletedAt == nil {
		t.Fatal("Remove must stamp DeletedAt in memory")
	}
	if gone.Version != 2 {
		t.Fatalf("soft delete must bump version, got %d", gone.Version)
	}

	// Invisible to normal reads; visible to WithDeleted/OnlyDeleted.
	pref := gen.User.Email.HasPrefix("sd-")
	if n, _ := sorm.Query[models.User](db).Where(pref).Count(ctx); n != 1 {
		t.Fatalf("%s: alive count = %d", dialect, n)
	}
	if n, _ := sorm.Query[models.User](db).Where(pref).WithDeleted().Count(ctx); n != 2 {
		t.Fatalf("%s: with deleted = %d", dialect, n)
	}
	trash, err := sorm.Query[models.User](db).Where(pref).OnlyDeleted().All(ctx)
	if err != nil || len(trash) != 1 || trash[0].Name != "Gone" {
		t.Fatalf("%s: trash = %v (err=%v)", dialect, names(trash), err)
	}

	// The row is really still there (raw count bypasses the ORM filter).
	type cnt struct{ N int64 }
	raw, err := sorm.RawAs[cnt](db, `SELECT count(*) AS n FROM users WHERE email LIKE 'sd-%'`).One(ctx)
	if err != nil || raw.N != 2 {
		t.Fatalf("%s: raw rows = %d (err=%v)", dialect, raw.N, err)
	}

	// Relation predicate skips deleted parents.
	orphans, err := sorm.Query[models.Post](db).
		Where(gen.Post.Title.Eq("orphaned"), gen.Post.Author.Is(gen.User.Active.Eq(true))).
		All(ctx)
	if err != nil || len(orphans) != 0 {
		t.Fatalf("%s: deleted parent must not satisfy Is(), got %d rows", dialect, len(orphans))
	}

	// Restore: set the column back to NULL via WithDeleted update.
	if _, err := sorm.Update[models.User](db).
		Set(gen.User.DeletedAt.SetNull()).
		Where(gen.User.Email.Eq("sd-gone@" + dialect)).
		WithDeleted().
		Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := sorm.Query[models.User](db).Where(pref).Count(ctx); n != 2 {
		t.Fatalf("%s: restore failed", dialect)
	}

	// Hard purge: children first (posts hard-delete as usual — no soft
	// delete there), then the user with Hard().
	if _, err := sorm.Delete[models.Post](db).Where(gen.Post.Title.Eq("orphaned")).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sorm.Delete[models.User](db).
		Where(gen.User.Email.Eq("sd-gone@" + dialect)).
		Hard().
		Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if raw, _ := sorm.RawAs[cnt](db, `SELECT count(*) AS n FROM users WHERE email LIKE 'sd-%'`).One(ctx); raw.N != 1 {
		t.Fatalf("%s: hard delete left %d rows", dialect, raw.N)
	}
}

func TestSoftDeleteSQLite(t *testing.T) { runSoftDeleteScenario(t, sqliteDB(t), "sqlite") }
func TestSoftDeletePG(t *testing.T)     { runSoftDeleteScenario(t, testPool(t), "postgres") }
func TestSoftDeleteMySQL(t *testing.T)  { runSoftDeleteScenario(t, mysqlDB(t), "mysql") }
