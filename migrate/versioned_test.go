package migrate_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sorm"
	"sorm/dialect/lite"
	"sorm/driver/sqld"
	"sorm/migrate"
	models "sorm/internal/testmodels"
)

func TestVersionedFullCycle(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "migrations")

	// 1. Diff на пустом каталоге: scratch-БД = SQLite in-memory (никакого docker).
	dev := sqliteDB(t)
	fname, err := migrate.Diff(ctx, dev, "sqlite", dir, "init schema")
	if err != nil {
		t.Fatal(err)
	}
	if fname == "" || !strings.HasSuffix(fname, "_init_schema.sql") {
		t.Fatalf("fname = %q", fname)
	}
	content, _ := os.ReadFile(filepath.Join(dir, fname))
	if !strings.Contains(string(content), "CREATE TABLE") {
		t.Fatalf("в миграции нет CREATE TABLE:\n%s", content)
	}

	// 2. Up применяет файл к целевой БД; повторный Up — no-op.
	target := sqliteDB(t)
	applied, err := migrate.Up(ctx, target, "sqlite", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != fname {
		t.Fatalf("applied = %v", applied)
	}
	if again, _ := migrate.Up(ctx, target, "sqlite", dir); len(again) != 0 {
		t.Fatalf("повторный Up применил %v", again)
	}

	// 3. Схема рабочая: сессия проходит.
	db := sqld.Wrap(target, lite.Dialect{})
	s := sorm.NewSession(db)
	u := &models.User{Email: "v@b.c", Name: "Ver", Active: true}
	sorm.Add(s, u)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// 4. Повторный Diff (replay на свежую scratch) — изменений нет.
	dev2 := sqliteDB(t)
	fname2, err := migrate.Diff(ctx, dev2, "sqlite", dir, "noop")
	if err != nil {
		t.Fatal(err)
	}
	if fname2 != "" {
		t.Fatalf("фантомная миграция %s", fname2)
	}

	// 5. Эволюция схемы: эмулируем добавление таблицы в модели.
	sorm.RegisterTable(sorm.TableDef{
		Name: "labels",
		Columns: []sorm.ColumnDef{
			{Name: "id", GoKind: "int64", PK: true, Auto: true},
			{Name: "title", GoKind: "string", Unique: true},
		},
	})
	t.Cleanup(func() { sorm.UnregisterTable("labels") })

	dev3 := sqliteDB(t)
	fname3, err := migrate.Diff(ctx, dev3, "sqlite", dir, "add labels")
	if err != nil {
		t.Fatal(err)
	}
	if fname3 == "" {
		t.Fatal("дифф не увидел новую таблицу")
	}
	content3, _ := os.ReadFile(filepath.Join(dir, fname3))
	if !strings.Contains(string(content3), "labels") || strings.Contains(string(content3), "users") {
		t.Fatalf("вторая миграция должна содержать только labels:\n%s", content3)
	}

	// 6. Pending видит новый файл; Up доводит целевую БД.
	pending, err := migrate.Pending(ctx, target, "sqlite", dir)
	if err != nil || len(pending) != 1 || pending[0] != fname3 {
		t.Fatalf("pending = %v, err=%v", pending, err)
	}
	if applied, err := migrate.Up(ctx, target, "sqlite", dir); err != nil || len(applied) != 1 {
		t.Fatalf("up: %v %v", applied, err)
	}
	if _, err := target.Exec(`INSERT INTO labels (title) VALUES ('go')`); err != nil {
		t.Fatalf("таблица labels не создана: %v", err)
	}
}

func TestSplitStatements(t *testing.T) {
	in := "-- comment\nCREATE TABLE a (x INT);\n-- another\nCREATE TABLE b (y TEXT);\n"
	got := migrate.SplitStatements(in)
	if len(got) != 2 || !strings.HasPrefix(got[0], "CREATE TABLE a") || !strings.HasPrefix(got[1], "CREATE TABLE b") {
		t.Fatalf("got %#v", got)
	}
}
