package sorm_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"sorm"
	"sorm/driver/pgxd"
	models "sorm/internal/testmodels"
	gen "sorm/internal/testmodels/sormgen"
)

// Интеграционные тесты сессии: живой PostgreSQL.
// Запуск: $env:SORM_TEST_DSN = 'postgres://postgres:postgres@localhost:15432/sorm_example'; go test ./...

func testPool(t *testing.T) sorm.DB {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_DSN")
	if dsn == "" {
		t.Skip("SORM_TEST_DSN не задан — интеграционные тесты пропущены")
	}
	pgPool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pgPool.Close)
	pool := pgxd.Wrap(pgPool)

	ctx := context.Background()
	stmts := append([]string{
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
		t.Fatal("auto-PK автора не проставлен")
	}
	if p1.AuthorID != u.ID || p2.AuthorID != u.ID {
		t.Fatalf("FK-fixup не сработал: %d/%d, want %d", p1.AuthorID, p2.AuthorID, u.ID)
	}
	if u.Version != 1 {
		t.Fatalf("version = %d, want 1 (инициализация при insert)", u.Version)
	}

	n, err := sorm.Query[models.Post](pool).Where(gen.Post.AuthorID.Eq(u.ID)).Count(ctx)
	if err != nil || n != 2 {
		t.Fatalf("в БД %d постов (err=%v), want 2", n, err)
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

	u.Name = "Alicia" // единственное изменение
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if u.Version != 2 {
		t.Fatalf("version = %d, want 2 (инкремент при update)", u.Version)
	}

	// Повторный SaveChanges без изменений — no-op.
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if u.Version != 2 {
		t.Fatalf("no-op SaveChanges изменил version: %d", u.Version)
	}

	fresh, err := sorm.Query[models.User](pool).Where(gen.User.Email.Eq("a@b.c")).One(ctx)
	if err != nil || fresh.Name != "Alicia" {
		t.Fatalf("в БД name=%q (err=%v)", fresh.Name, err)
	}
}

func TestSessionIdentityMap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)

	s := sorm.NewSession(pool)
	u1, _ := sorm.Track[models.User](s).Where(gen.User.Email.Eq("a@b.c")).One(ctx)
	u1.Name = "локальное изменение"
	u2, _ := sorm.Track[models.User](s).Where(gen.User.Email.Eq("a@b.c")).One(ctx)

	if u1 != u2 {
		t.Fatal("identity map: два указателя на одну строку")
	}
	if u2.Name != "локальное изменение" {
		t.Fatal("повторная загрузка перезатёрла локальные изменения")
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
		t.Fatalf("ожидали ConflictError, получили %v", err)
	}
	if conflict.Table != "users" {
		t.Fatalf("conflict.Table = %q", conflict.Table)
	}
}

func TestSessionRemoveOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)
	mustExec(t, pool, `INSERT INTO posts (author_id, title, body) VALUES (1, 't', 'b'), (1, 't2', 'b2')`)

	s := sorm.NewSession(pool)
	u, err := sorm.Track[models.User](s).
		Where(gen.User.Email.Eq("a@b.c")).
		With(gen.User.Posts.Include()).
		One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Posts) != 2 {
		t.Fatalf("постов %d, want 2", len(u.Posts))
	}

	// Удаляем родителя и детей в одном SaveChanges: posts должны удалиться
	// раньше users (иначе FK violation).
	sorm.Remove(s, u.Posts...)
	sorm.Remove(s, u)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	n, _ := sorm.Query[models.User](pool).Count(ctx)
	if n != 0 {
		t.Fatalf("users не удалён: %d строк", n)
	}
}

func TestSessionTrackedChildrenViaInclude(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)
	mustExec(t, pool, `INSERT INTO posts (author_id, title, body) VALUES (1, 'old', 'b')`)

	s := sorm.NewSession(pool)
	u, err := sorm.Track[models.User](s).
		Where(gen.User.Email.Eq("a@b.c")).
		With(gen.User.Posts.Include()).
		One(ctx)
	if err != nil {
		t.Fatal(err)
	}

	u.Posts[0].Title = "new" // мутация ребёнка, загруженного через Include
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
	// NOT NULL FK без навигации и без значения — ошибка до похода в БД.
	sorm.Add(s, &models.Post{Title: "orphan", Body: "..."})
	err := s.SaveChanges(ctx)
	if err == nil {
		t.Fatal("ожидали ошибку валидации FK")
	}

	// Навигация на незарегистрированного родителя без PK — тоже ошибка.
	s2 := sorm.NewSession(pool)
	ghost := &models.User{Email: "g@b.c", Name: "Ghost", Active: true}
	sorm.Add(s2, &models.Post{Author: ghost, Title: "x", Body: "y"}) // ghost не Add-нут
	if err := s2.SaveChanges(ctx); err == nil {
		t.Fatal("ожидали ошибку: родитель не persisted и не Added")
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
