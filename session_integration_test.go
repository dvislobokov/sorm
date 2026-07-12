package sorm_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/driver/pgxd"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// Session integration tests: live PostgreSQL.
// Run: $env:SORM_TEST_DSN = 'postgres://postgres:postgres@localhost:15432/sorm_example'; go test ./...

func testPool(t *testing.T) sorm.DB {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_DSN")
	if dsn == "" {
		t.Skip("SORM_TEST_DSN not set — integration tests skipped")
	}
	pgPool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pgPool.Close)
	pool := pgxd.Wrap(pgPool)

	ctx := context.Background()
	stmts := append([]string{
		`DROP TABLE IF EXISTS profiles`,
		`DROP TABLE IF EXISTS user_tags`,
		`DROP TABLE IF EXISTS tags`,
		`DROP TABLE IF EXISTS api_keys`,
		`DROP TABLE IF EXISTS posts`,
		`DROP TABLE IF EXISTS users`,
	}, generatedDDL(t, "postgres")...)
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return pool
}

func TestSessionInsertGraph(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	s := sorm.NewSession(pool)
	u := &models.User{Email: "a@b.c", Name: "Alice", Active: true, Age: 30}
	p1 := &models.Post{Author: u, Title: "first", Body: "..."}
	p2 := &models.Post{Author: u, Title: "second", Body: "..."}
	sorm.Add(s, u)
	sorm.Add(s, p1, p2)

	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 {
		t.Fatal("author auto-PK not populated")
	}
	if p1.AuthorID != u.ID || p2.AuthorID != u.ID {
		t.Fatalf("FK fixup did not run: %d/%d, want %d", p1.AuthorID, p2.AuthorID, u.ID)
	}
	if u.Version != 1 {
		t.Fatalf("version = %d, want 1 (initialized on insert)", u.Version)
	}

	n, err := sorm.Query[models.Post](pool).Where(gen.Post.AuthorID.Eq(u.ID)).Count(ctx)
	if err != nil || n != 2 {
		t.Fatalf("DB has %d posts (err=%v), want 2", n, err)
	}
}

func TestSessionPartialUpdateAndVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)

	s := sorm.NewSession(pool)
	u, err := sorm.Track[models.User](s).Where(gen.User.Email.Eq("a@b.c")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}

	u.Name = "Alicia" // the only change
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if u.Version != 2 {
		t.Fatalf("version = %d, want 2 (incremented on update)", u.Version)
	}

	// A second SaveChanges with no changes is a no-op.
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if u.Version != 2 {
		t.Fatalf("no-op SaveChanges changed version: %d", u.Version)
	}

	fresh, err := sorm.Query[models.User](pool).Where(gen.User.Email.Eq("a@b.c")).One(ctx)
	if err != nil || fresh.Name != "Alicia" {
		t.Fatalf("DB has name=%q (err=%v)", fresh.Name, err)
	}
}

func TestSessionIdentityMap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)

	s := sorm.NewSession(pool)
	u1, _ := sorm.Track[models.User](s).Where(gen.User.Email.Eq("a@b.c")).One(ctx)
	u1.Name = "local change"
	u2, _ := sorm.Track[models.User](s).Where(gen.User.Email.Eq("a@b.c")).One(ctx)

	if u1 != u2 {
		t.Fatal("identity map: two pointers for the same row")
	}
	if u2.Name != "local change" {
		t.Fatal("reloading overwrote local changes")
	}
}

func TestSessionConflict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)

	s1, s2 := sorm.NewSession(pool), sorm.NewSession(pool)
	u1, _ := sorm.Track[models.User](s1).Where(gen.User.Email.Eq("a@b.c")).One(ctx)
	u2, _ := sorm.Track[models.User](s2).Where(gen.User.Email.Eq("a@b.c")).One(ctx)

	u1.Age = 31
	if err := s1.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	u2.Age = 99
	err := s2.SaveChanges(ctx)
	var conflict *sorm.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	if conflict.Table != "users" {
		t.Fatalf("conflict.Table = %q", conflict.Table)
	}
}

func TestSessionRemoveOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)
	mustExec(t, pool, `INSERT INTO posts (author_id, title, body, views) VALUES (1, 't', 'b', 0), (1, 't2', 'b2', 0)`)

	s := sorm.NewSession(pool)
	u, err := sorm.Track[models.User](s).
		Where(gen.User.Email.Eq("a@b.c")).
		With(gen.User.Posts.Include()).
		One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Posts) != 2 {
		t.Fatalf("got %d posts, want 2", len(u.Posts))
	}

	// Delete the parent and children in one SaveChanges: posts must be
	// deleted before users (otherwise FK violation).
	sorm.Remove(s, u.Posts...)
	sorm.Remove(s, u)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	n, _ := sorm.Query[models.User](pool).Count(ctx)
	if n != 0 {
		t.Fatalf("users not deleted: %d rows", n)
	}
}

func TestSessionTrackedChildrenViaInclude(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)
	mustExec(t, pool, `INSERT INTO posts (author_id, title, body, views) VALUES (1, 'old', 'b', 0)`)

	s := sorm.NewSession(pool)
	u, err := sorm.Track[models.User](s).
		Where(gen.User.Email.Eq("a@b.c")).
		With(gen.User.Posts.Include()).
		One(ctx)
	if err != nil {
		t.Fatal(err)
	}

	u.Posts[0].Title = "new" // mutating a child loaded via Include
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	p, err := sorm.Query[models.Post](pool).One(ctx)
	if err != nil || p.Title != "new" {
		t.Fatalf("title=%q (err=%v), want new", p.Title, err)
	}
}

func TestSessionAddValidation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	s := sorm.NewSession(pool)
	// A NOT NULL FK with no navigation and no value fails before hitting the DB.
	sorm.Add(s, &models.Post{Title: "orphan", Body: "..."})
	err := s.SaveChanges(ctx)
	if err == nil {
		t.Fatal("expected FK validation error")
	}

	// A navigation to an unregistered parent without a PK is an error too.
	s2 := sorm.NewSession(pool)
	ghost := &models.User{Email: "g@b.c", Name: "Ghost", Active: true}
	sorm.Add(s2, &models.Post{Author: ghost, Title: "x", Body: "y"}) // ghost is not Add-ed
	if err := s2.SaveChanges(ctx); err == nil {
		t.Fatal("expected error: parent is neither persisted nor Added")
	}
}

func seedAlice(t *testing.T, pool sorm.DB) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO users (email, name, active, age, balance, created_at, version)
		VALUES ('a@b.c', 'Alice', true, 30, 0, now(), 1)`)
}

func mustExec(t *testing.T, pool sorm.DB, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}
