package sorm_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"sorm"
	"sorm/dialect/lite"
	"sorm/dialect/my"
	"sorm/driver/sqld"
	"sorm/internal/ddl"
	"sorm/internal/parse"
	models "sorm/internal/testmodels"
	gen "sorm/internal/testmodels/sormgen"
)

// generatedDDL — CREATE TABLE из `sorm schema`: интеграционные тесты работают
// на той же схеме, которую увидит Atlas, — генератор DDL проверен рантаймом.
func generatedDDL(t *testing.T, dialect string) []string {
	t.Helper()
	s, err := parse.Load("./internal/testmodels")
	if err != nil {
		t.Fatal(err)
	}
	sql, err := ddl.Generate(s, dialect)
	if err != nil {
		t.Fatal(err)
	}
	var stmts []string
	for _, chunk := range strings.Split(sql, ";") {
		var keep []string
		for _, ln := range strings.Split(chunk, "\n") {
			trimmed := strings.TrimSpace(ln)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			keep = append(keep, ln)
		}
		if stmt := strings.TrimSpace(strings.Join(keep, "\n")); stmt != "" {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}

// Кросс-диалектный сквозной сценарий: одна и та же бизнес-логика sorm
// должна работать на PostgreSQL (session_integration_test), SQLite
// (in-memory, гоняется всегда) и MySQL (гейт SORM_TEST_MYSQL_DSN).

func sqliteDB(t *testing.T) sorm.DB {
	t.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1) // :memory: живёт в одном соединении
	t.Cleanup(func() { sdb.Close() })

	for _, s := range generatedDDL(t, "sqlite") {
		if _, err := sdb.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return sqld.Wrap(sdb, lite.Dialect{})
}

func mysqlDB(t *testing.T) sorm.DB {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_MYSQL_DSN") // например: root:root@tcp(localhost:13306)/sorm_test?parseTime=true
	if dsn == "" {
		t.Skip("SORM_TEST_MYSQL_DSN не задан — MySQL-тесты пропущены")
	}
	sdb, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })

	stmts := append([]string{
		`DROP TABLE IF EXISTS posts`,
		`DROP TABLE IF EXISTS users`,
	}, generatedDDL(t, "mysql")...)
	for _, s := range stmts {
		if _, err := sdb.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return sqld.Wrap(sdb, my.Dialect{})
}

func TestSQLiteFullScenario(t *testing.T) { runDialectScenario(t, sqliteDB(t)) }
func TestMySQLFullScenario(t *testing.T)  { runDialectScenario(t, mysqlDB(t)) }

// runDialectScenario — сквозной сценарий: граф через сессию, Include,
// частичный UPDATE с версией, конфликт, EXISTS, set-based, проекция, удаление.
func runDialectScenario(t *testing.T, db sorm.DB) {
	ctx := context.Background()
	u := gen.User
	p := gen.Post

	// 1. Вставка графа: RETURNING (PG) или LastInsertId (MySQL/SQLite).
	now := time.Now()
	s := sorm.NewSession(db)
	alice := &models.User{Email: "a@b.c", Name: "Alice", Active: true, Age: 30, CreatedAt: now}
	post1 := &models.Post{Author: alice, Title: "first", Body: "b1"}
	post2 := &models.Post{Author: alice, Title: "second", Body: "b2"}
	bob := &models.User{Email: "b@b.c", Name: "Bob", Active: false, Age: 40, CreatedAt: now}
	sorm.Add(s, alice, bob)
	sorm.Add(s, post1, post2)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if alice.ID == 0 || bob.ID == 0 || post1.AuthorID != alice.ID {
		t.Fatalf("PK/fixup: alice=%d bob=%d post1.fk=%d", alice.ID, bob.ID, post1.AuthorID)
	}

	// 2. Запросы: zero-value условие + EXISTS.
	inactive, err := sorm.Query[models.User](db).Where(u.Active.Eq(false)).All(ctx)
	if err != nil || len(inactive) != 1 || inactive[0].Name != "Bob" {
		t.Fatalf("inactive: %v %v", inactive, err)
	}
	withPosts, err := sorm.Query[models.User](db).Where(u.Posts.Any(p.Title.HasPrefix("f"))).All(ctx)
	if err != nil || len(withPosts) != 1 || withPosts[0].Name != "Alice" {
		t.Fatalf("EXISTS: %v %v", withPosts, err)
	}

	// 3. Track + Include, частичный UPDATE ребёнка и родителя.
	s2 := sorm.NewSession(db)
	loaded, err := sorm.Track[models.User](s2).
		Where(u.Email.Eq("a@b.c")).
		With(u.Posts.Include()).
		One(ctx)
	if err != nil || len(loaded.Posts) != 2 {
		t.Fatalf("track+include: %v posts=%d", err, len(loaded.Posts))
	}
	loaded.Age = 31
	loaded.Posts[0].Title = "renamed"
	if err := s2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 2 {
		t.Fatalf("version = %d, want 2", loaded.Version)
	}

	// 4. Optimistic concurrency.
	sA, sB := sorm.NewSession(db), sorm.NewSession(db)
	a1, _ := sorm.Track[models.User](sA).Where(u.Email.Eq("a@b.c")).One(ctx)
	a2, _ := sorm.Track[models.User](sB).Where(u.Email.Eq("a@b.c")).One(ctx)
	a1.Name = "A1"
	if err := sA.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	a2.Name = "A2"
	var conflict *sorm.ConflictError
	if err := sB.SaveChanges(ctx); !errors.As(err, &conflict) {
		t.Fatalf("ожидали ConflictError, получили %v", err)
	}

	// 5. Set-based UPDATE + проекция.
	if _, err := sorm.Update[models.Post](db).
		Set(p.Body.Set("bulk")).
		Where(p.AuthorID.Eq(alice.ID)).
		Exec(ctx); err != nil {
		t.Fatal(err)
	}
	type stat struct {
		Name string
		N    int64
	}
	rows, err := sorm.Project[stat](
		sorm.From[models.User](db).
			Join(u.Posts.LeftJoin()).
			GroupBy(u.Name).
			OrderBy(u.Name.Asc()),
		sorm.Field(u.Name),
		sorm.As(sorm.Count[models.User](p.ID), "n"),
	).All(ctx)
	if err != nil || len(rows) != 2 || rows[0].N != 2 || rows[1].N != 0 {
		t.Fatalf("projection: %v err=%v", rows, err)
	}

	// 6. Удаление: дети раньше родителя.
	s3 := sorm.NewSession(db)
	gone, err := sorm.Track[models.User](s3).Where(u.Email.Eq("a@b.c")).With(u.Posts.Include()).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sorm.Remove(s3, gone.Posts...)
	sorm.Remove(s3, gone)
	if err := s3.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := sorm.Query[models.User](db).Count(ctx)
	if err != nil || n != 1 {
		t.Fatalf("после удаления users=%d err=%v", n, err)
	}
}
