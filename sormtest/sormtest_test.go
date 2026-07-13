package sormtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
	"github.com/dvislobokov/sorm/sormtest"
)

var u, p = gen.User, gen.Post

func TestNewSQLiteAndFixtures(t *testing.T) {
	t.Parallel()
	db := sormtest.NewSQLite(t)
	sormtest.Load(t, db, "testdata/seed.yaml")
	ctx := context.Background()

	// FK order held (posts section precedes users in the file), rows landed.
	alice, err := sorm.Query[models.User](db).
		Where(u.Email.Eq("alice@a.b")).
		With(u.Posts.Include(), u.Profile.Include()).
		One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(alice.Posts) != 2 || alice.Profile == nil {
		t.Fatalf("fixture graph: posts=%d profile=%v", len(alice.Posts), alice.Profile)
	}
	// JSON column came from a YAML map.
	if alice.Profile.Prefs == nil || alice.Profile.Prefs.Theme != "dark" {
		t.Fatalf("json fixture: %+v", alice.Profile.Prefs)
	}

	// The database is real: sessions work on top of fixtures.
	c := gen.NewContext(db)
	loaded, err := c.Users.Find(ctx, int64(1))
	if err != nil {
		t.Fatal(err)
	}
	loaded.Name = "Alice II"
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAssertSQL(t *testing.T) {
	t.Parallel()
	sormtest.AssertSQL(t,
		sorm.Query[models.Post](nil).Where(p.Views.Gt(10)).OrderBy(p.ID.Asc()).Limit(5),
		`SELECT "id", "author_id", "title", "body", "views", "created_at", "updated_at" FROM "posts" WHERE "views" > $1 ORDER BY "id" LIMIT 5`,
		10)

	// The 3-value ToSQL shape (Update/Delete/Upsert) works too.
	sormtest.AssertSQL(t,
		sorm.Update[models.Post](nil).Set(p.Views.Set(0)).AllRows(),
		`UPDATE "posts" SET "views" = $1`,
		0)
}

func TestCountQueries(t *testing.T) {
	t.Parallel()
	base := sormtest.NewSQLite(t)
	sormtest.Load(t, base, "testdata/seed.yaml")

	db, queries := sormtest.CountQueries(base)
	ctx := context.Background()

	// One page with Include = 2 selects (parents + children), not N+1.
	users, err := sorm.Query[models.User](db).With(u.Posts.Include()).All(ctx)
	if err != nil || len(users) == 0 {
		t.Fatal(err)
	}
	if n := queries.Selects(); n != 2 {
		t.Fatalf("selects = %d, want 2 (split include)", n)
	}

	queries.Reset()
	s := sorm.NewSession(db)
	sorm.Add(s, &models.User{Email: "b@b.c", Name: "B", Active: true, CreatedAt: users[0].CreatedAt})
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if queries.Writes() == 0 {
		t.Fatal("writes must be counted")
	}
}

func TestNewPostgresIsolated(t *testing.T) {
	t.Parallel()
	db := sormtest.NewPostgres(t) // skips without SORM_TEST_DSN
	ctx := context.Background()

	c := gen.NewContext(db)
	c.Users.Add(&models.User{Email: "iso@a.b", Name: "Iso", Active: true, CreatedAt: time.Now()})
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// A second helper call = a different schema: the row is invisible there.
	other := sormtest.NewPostgres(t)
	if n, _ := sorm.Query[models.User](other).Count(ctx); n != 0 {
		t.Fatalf("schemas must be isolated, saw %d rows", n)
	}
}
