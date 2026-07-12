package sorm_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect/lite"
	"github.com/dvislobokov/sorm/driver/sqld"
	"github.com/dvislobokov/sorm/internal/ddl"
	"github.com/dvislobokov/sorm/internal/parse"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	"github.com/dvislobokov/sorm/migrate"
)

// Микробенчмарки против raw database/sql на SQLite in-memory (без сети —
// меряется overhead sorm, не БД). Запуск: go test -bench . -benchmem
//
// Полноценное сравнение с GORM/Ent/sqlc на PostgreSQL — отдельная задача
// (см. efectn/go-orm-benchmarks).

func benchDB(b *testing.B, rows int) (*sql.DB, sorm.DB) {
	b.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	b.Cleanup(func() { sdb.Close() })

	s, err := parse.Load("./internal/testmodels")
	if err != nil {
		b.Fatal(err)
	}
	sqlText, err := ddl.Generate(s, "sqlite")
	if err != nil {
		b.Fatal(err)
	}
	for _, stmt := range migrate.SplitStatements(sqlText) {
		if _, err := sdb.Exec(stmt); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < rows; i++ {
		if _, err := sdb.Exec(
			`INSERT INTO users (email, name, active, age, balance, created_at, version)
			 VALUES (?, ?, 1, ?, 1.5, '2026-01-01T00:00:00Z', 1)`,
			fmt.Sprintf("u%d@b.c", i), fmt.Sprintf("user-%d", i), 20+i%50,
		); err != nil {
			b.Fatal(err)
		}
	}
	return sdb, sqld.Wrap(sdb, lite.Dialect{})
}

func BenchmarkQueryAll1000(b *testing.B) {
	_, db := benchDB(b, 1000)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		users, err := sorm.Query[models.User](db).All(ctx)
		if err != nil || len(users) != 1000 {
			b.Fatal(err, len(users))
		}
	}
}

func BenchmarkRawStdlib1000(b *testing.B) {
	sdb, _ := benchDB(b, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := sdb.Query(`SELECT id, email, name, nickname, active, age, balance, avatar, created_at, deleted_at, version FROM users`)
		if err != nil {
			b.Fatal(err)
		}
		var out []*models.User
		for rows.Next() {
			u := new(models.User)
			if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Nickname, &u.Active, &u.Age,
				&u.Balance, &u.Avatar, &u.CreatedAt, &u.DeletedAt, &u.Version); err != nil {
				b.Fatal(err)
			}
			out = append(out, u)
		}
		rows.Close()
		if len(out) != 1000 {
			b.Fatal(len(out))
		}
	}
}

func BenchmarkToSQL(b *testing.B) {
	q := sorm.Query[models.User](nil).
		Where(u.Active.Eq(true), u.Age.Gte(18)).
		OrderBy(u.Name.Asc()).
		Limit(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.ToSQL()
	}
}

func BenchmarkSnapshotDiffClean(b *testing.B) {
	m := sorm.MetaOf[models.User]()
	usr := newUser()
	snap := m.Snapshot(usr)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ch := m.Diff(snap, usr); len(ch) != 0 {
			b.Fatal("dirty")
		}
	}
}

func BenchmarkSessionUpdateOneField(b *testing.B) {
	_, db := benchDB(b, 1)
	ctx := context.Background()
	s := sorm.NewSession(db)
	usr, err := sorm.Track[models.User](s).One(ctx)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		usr.Age = 20 + i%50
		if err := s.SaveChanges(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
