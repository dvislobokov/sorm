package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // регистрирует драйвер "pgx" в database/sql
	_ "modernc.org/sqlite"

	"sorm"
	"sorm/dialect/lite"
	"sorm/driver/sqld"
	"sorm/migrate"
	models "sorm/internal/testmodels"
	gen "sorm/internal/testmodels/sormgen"
)

var _ = gen.User // импорт sormgen регистрирует TableDef'ы

func sqliteDB(t *testing.T) *sql.DB {
	t.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	t.Cleanup(func() { sdb.Close() })
	return sdb
}

func TestApplySQLiteFromScratch(t *testing.T) {
	ctx := context.Background()
	sdb := sqliteDB(t)

	// 1. Пустая БД → Apply создаёт схему из зарегистрированных моделей.
	if err := migrate.Apply(ctx, sdb, "sqlite"); err != nil {
		t.Fatal(err)
	}

	// 2. Схема рабочая: сквозной сценарий сессии.
	db := sqld.Wrap(sdb, lite.Dialect{})
	s := sorm.NewSession(db)
	u := &models.User{Email: "m@b.c", Name: "Mig", Active: true, Age: 20}
	p := &models.Post{Author: u, Title: "migrated", Body: "b"}
	sorm.Add(s, u)
	sorm.Add(s, p)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || p.AuthorID != u.ID {
		t.Fatalf("insert после Apply: id=%d fk=%d", u.ID, p.AuthorID)
	}

	// 3. Идемпотентность: повторный Plan пуст.
	plan, err := migrate.Plan(ctx, sdb, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("повторный Plan не пуст (фантомный дифф):\n%s", strings.Join(plan, "\n"))
	}
}

func TestPlanDetectsDrift(t *testing.T) {
	ctx := context.Background()
	sdb := sqliteDB(t)

	if err := migrate.Apply(ctx, sdb, "sqlite"); err != nil {
		t.Fatal(err)
	}
	// Дрейф: руками сносим колонку.
	if _, err := sdb.Exec(`ALTER TABLE users DROP COLUMN nickname`); err != nil {
		t.Fatal(err)
	}

	plan, err := migrate.Plan(ctx, sdb, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "nickname") {
		t.Fatalf("Plan не увидел дрейф:\n%s", joined)
	}
	// Apply чинит дрейф.
	if err := migrate.Apply(ctx, sdb, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if plan, _ = migrate.Plan(ctx, sdb, "sqlite"); len(plan) != 0 {
		t.Fatalf("после починки дифф не пуст: %v", plan)
	}
}

// pgDSN — отдельная база для migrate-тестов: пакеты sorm и sorm/migrate
// гоняются go test'ом параллельно и не должны делить одну БД.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_DSN")
	if dsn == "" {
		t.Skip("SORM_TEST_DSN не задан")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	_, _ = admin.Exec(`CREATE DATABASE sorm_migrate_test`) // ошибка "exists" игнорируется

	// заменяем имя базы в DSN
	i := strings.LastIndex(dsn, "/")
	rest := dsn[i+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		return dsn[:i+1] + "sorm_migrate_test" + rest[q:]
	}
	return dsn[:i+1] + "sorm_migrate_test"
}

func TestApplyPostgres(t *testing.T) {
	dsn := pgDSN(t)
	ctx := context.Background()

	sdb, err := sql.Open("pgx", dsn) // database/sql поверх pgx (stdlib-адаптер)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()

	for _, q := range []string{`DROP TABLE IF EXISTS profiles`, `DROP TABLE IF EXISTS user_tags`, `DROP TABLE IF EXISTS tags`, `DROP TABLE IF EXISTS api_keys`, `DROP TABLE IF EXISTS posts`, `DROP TABLE IF EXISTS users`} {
		if _, err := sdb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	if err := migrate.Apply(ctx, sdb, "postgres"); err != nil {
		t.Fatal(err)
	}
	plan, err := migrate.Plan(ctx, sdb, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("PG: повторный Plan не пуст:\n%s", strings.Join(plan, "\n"))
	}
}
