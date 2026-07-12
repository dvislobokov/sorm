package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

func TestUpsertSQL(t *testing.T) {
	u1 := &models.User{Email: "a@b.c", Name: "A", CreatedAt: nowNoZero()}

	// PG: ON CONFLICT ... DO UPDATE SET ... excluded + version bump.
	sql, args, err := sorm.Upsert[models.User](nil).
		Rows(u1).
		OnConflict(gen.User.Email).
		DoUpdate(gen.User.Name, gen.User.Age).
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ON CONFLICT ("email") DO UPDATE SET`,
		`"name" = excluded."name"`,
		`"age" = excluded."age"`,
		`"version" = "users"."version" + 1`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("sql:\n%s\nmissing: %s", sql, want)
		}
	}
	if len(args) == 0 {
		t.Fatal("no args")
	}

	// DO NOTHING.
	sql, _, err = sorm.Upsert[models.User](nil).
		Rows(u1).OnConflict(gen.User.Email).DoNothing().ToSQL()
	if err != nil || !strings.Contains(sql, "DO NOTHING") {
		t.Fatalf("do nothing: %s (err=%v)", sql, err)
	}

	// Build errors.
	if _, _, err = sorm.Upsert[models.User](nil).OnConflict(gen.User.Email).DoNothing().ToSQL(); err == nil {
		t.Fatal("want error: no rows")
	}
	if _, _, err = sorm.Upsert[models.User](nil).Rows(u1).OnConflict(gen.User.Email).ToSQL(); err == nil {
		t.Fatal("want error: no action")
	}
	if _, _, err = sorm.Upsert[models.User](nil).Rows(u1).DoNothing().ToSQL(); err == nil {
		t.Fatal("want error: no conflict target on postgres")
	}
	if _, _, err = sorm.Upsert[models.User](nil).Rows(u1).
		OnConflict(gen.User.Email).DoUpdate(gen.User.ID).ToSQL(); err == nil {
		t.Fatal("want error: pk is not insertable")
	}
}

func runUpsertScenario(t *testing.T, db sorm.DB, dialect string) {
	t.Helper()
	ctx := context.Background()

	fresh := &models.User{Email: "up@" + dialect, Name: "First", Active: true, Age: 20, CreatedAt: nowNoZero()}
	q := sorm.Upsert[models.User](db).OnConflict(gen.User.Email).DoUpdate(gen.User.Name, gen.User.Age)

	// 1. Insert path.
	if _, err := q.Rows(fresh).Exec(ctx); err != nil {
		t.Fatalf("%s insert: %v", dialect, err)
	}
	got, err := sorm.Query[models.User](db).Where(gen.User.Email.Eq("up@" + dialect)).One(ctx)
	if err != nil || got.Name != "First" || got.Version != 1 {
		t.Fatalf("%s after insert: %+v (err=%v)", dialect, got, err)
	}

	// 2. Conflict → update Name/Age, bump version, keep Active untouched.
	dup := &models.User{Email: "up@" + dialect, Name: "Second", Active: false, Age: 30, CreatedAt: nowNoZero()}
	if _, err := q.Rows(dup).Exec(ctx); err != nil {
		t.Fatalf("%s update: %v", dialect, err)
	}
	got, err = sorm.Query[models.User](db).Where(gen.User.Email.Eq("up@" + dialect)).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Second" || got.Age != 30 {
		t.Fatalf("%s: DoUpdate columns not applied: %+v", dialect, got)
	}
	if !got.Active {
		t.Fatalf("%s: column outside DoUpdate must stay untouched", dialect)
	}
	if got.Version != 2 {
		t.Fatalf("%s: version must be bumped, got %d", dialect, got.Version)
	}

	// 3. DoNothing leaves the row alone.
	skip := &models.User{Email: "up@" + dialect, Name: "Third", CreatedAt: nowNoZero()}
	if _, err := sorm.Upsert[models.User](db).
		Rows(skip).OnConflict(gen.User.Email).DoNothing().Exec(ctx); err != nil {
		t.Fatalf("%s do nothing: %v", dialect, err)
	}
	got, _ = sorm.Query[models.User](db).Where(gen.User.Email.Eq("up@" + dialect)).One(ctx)
	if got.Name != "Second" || got.Version != 2 {
		t.Fatalf("%s: DoNothing must not modify: %+v", dialect, got)
	}

	// 4. Multi-row: one insert + one update in a single statement.
	batch := []*models.User{
		{Email: "up2@" + dialect, Name: "New", Active: true, CreatedAt: nowNoZero()},
		{Email: "up@" + dialect, Name: "Fourth", Age: 40, CreatedAt: nowNoZero()},
	}
	if _, err := q.Rows(batch...).Exec(ctx); err != nil {
		t.Fatalf("%s multi: %v", dialect, err)
	}
	if n, _ := sorm.Query[models.User](db).Where(gen.User.Email.Eq("up2@" + dialect)).Count(ctx); n != 1 {
		t.Fatalf("%s: new row missing", dialect)
	}
	got, _ = sorm.Query[models.User](db).Where(gen.User.Email.Eq("up@" + dialect)).One(ctx)
	if got.Name != "Fourth" || got.Version != 3 {
		t.Fatalf("%s: multi-row update: %+v", dialect, got)
	}
}

func TestUpsertSQLite(t *testing.T) { runUpsertScenario(t, sqliteDB(t), "sqlite") }
func TestUpsertPG(t *testing.T)     { runUpsertScenario(t, testPool(t), "postgres") }
func TestUpsertMySQL(t *testing.T)  { runUpsertScenario(t, mysqlDB(t), "mysql") }
