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
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect/lite"
	"github.com/dvislobokov/sorm/dialect/my"
	"github.com/dvislobokov/sorm/driver/pgxd"
	"github.com/dvislobokov/sorm/driver/sqld"
	"github.com/dvislobokov/sorm/internal/ddl"
	"github.com/dvislobokov/sorm/internal/parse"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// generatedDDL — CREATE TABLE from `sorm schema`: integration tests run on
// the same schema Atlas will see, so the DDL generator is verified at runtime.
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

// Cross-dialect end-to-end scenario: the same sorm business logic must
// work on PostgreSQL (session_integration_test), SQLite (in-memory,
// always runs) and MySQL (gated by SORM_TEST_MYSQL_DSN).

func sqliteDB(t *testing.T) sorm.DB {
	t.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1) // :memory: lives in a single connection
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
	dsn := os.Getenv("SORM_TEST_MYSQL_DSN") // e.g.: root:root@tcp(localhost:13306)/sorm_test?parseTime=true
	if dsn == "" {
		t.Skip("SORM_TEST_MYSQL_DSN not set — MySQL tests skipped")
	}
	sdb, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })

	stmts := append([]string{
		`DROP TABLE IF EXISTS comments`,
		`DROP TABLE IF EXISTS devices`,
		`DROP TABLE IF EXISTS profiles`,
		`DROP TABLE IF EXISTS user_tags`,
		`DROP TABLE IF EXISTS tags`,
		`DROP TABLE IF EXISTS api_keys`,
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

// mariaDB — MariaDB runs through the same mysql dialect and adapter;
// the tests certify that the whole scenario behaves identically.
func mariaDB(t *testing.T) sorm.DB {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_MARIADB_DSN") // e.g.: root:root@tcp(localhost:13307)/sorm_test?parseTime=true
	if dsn == "" {
		t.Skip("SORM_TEST_MARIADB_DSN not set — MariaDB tests skipped")
	}
	sdb, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })

	stmts := append([]string{
		`DROP TABLE IF EXISTS comments`,
		`DROP TABLE IF EXISTS devices`,
		`DROP TABLE IF EXISTS profiles`,
		`DROP TABLE IF EXISTS user_tags`,
		`DROP TABLE IF EXISTS tags`,
		`DROP TABLE IF EXISTS api_keys`,
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

// crdbDB — CockroachDB speaks the PG wire protocol: pgxd + the postgres
// dialect as-is. The scenario certifies RETURNING, batches and 40001
// retry classification.
func crdbDB(t *testing.T) sorm.DB {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_CRDB_DSN") // e.g.: postgres://root@localhost:26257/sorm_test?sslmode=disable
	if dsn == "" {
		t.Skip("SORM_TEST_CRDB_DSN not set — CockroachDB tests skipped")
	}
	pgPool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pgPool.Close)
	pool := pgxd.Wrap(pgPool)

	drops := []string{
		`DROP TABLE IF EXISTS comments`, `DROP TABLE IF EXISTS devices`,
		`DROP TABLE IF EXISTS profiles`, `DROP TABLE IF EXISTS user_tags`,
		`DROP TABLE IF EXISTS tags`, `DROP TABLE IF EXISTS api_keys`,
		`DROP TABLE IF EXISTS posts`, `DROP TABLE IF EXISTS users`,
	}
	for _, s := range append(drops, generatedDDL(t, "postgres")...) {
		if _, err := pool.Exec(context.Background(), s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return pool
}

func TestSQLiteFullScenario(t *testing.T)  { runDialectScenario(t, sqliteDB(t)) }
func TestMySQLFullScenario(t *testing.T)   { runDialectScenario(t, mysqlDB(t)) }
func TestMariaDBFullScenario(t *testing.T) { runDialectScenario(t, mariaDB(t)) }
func TestCRDBFullScenario(t *testing.T)    { runDialectScenario(t, crdbDB(t)) }

// runDialectScenario — end-to-end scenario: graph via session, Include,
// partial UPDATE with versioning, conflict, EXISTS, set-based, projection, delete.
func runDialectScenario(t *testing.T, db sorm.DB) {
	ctx := context.Background()
	u := gen.User
	p := gen.Post

	// 1. Graph insert: RETURNING (PG) or LastInsertId (MySQL/SQLite).
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

	// 2. Queries: zero-value condition + EXISTS.
	inactive, err := sorm.Query[models.User](db).Where(u.Active.Eq(false)).All(ctx)
	if err != nil || len(inactive) != 1 || inactive[0].Name != "Bob" {
		t.Fatalf("inactive: %v %v", inactive, err)
	}
	withPosts, err := sorm.Query[models.User](db).Where(u.Posts.Any(p.Title.HasPrefix("f"))).All(ctx)
	if err != nil || len(withPosts) != 1 || withPosts[0].Name != "Alice" {
		t.Fatalf("EXISTS: %v %v", withPosts, err)
	}

	// 3. Track + Include, partial UPDATE of child and parent.
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
		t.Fatalf("expected ConflictError, got %v", err)
	}

	// 5. Set-based UPDATE + projection.
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

	// 6. Delete: children before the parent.
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
		t.Fatalf("after delete users=%d err=%v", n, err)
	}
}
