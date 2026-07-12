package migrate_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sorm/migrate"
)

func TestDownAndChecksums(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "migrations")

	// Diff пишет up, down и sorm.sum.
	dev := sqliteDB(t)
	fname, err := migrate.Diff(ctx, dev, "sqlite", dir, "init")
	if err != nil {
		t.Fatal(err)
	}
	downName := strings.TrimSuffix(fname, ".sql") + ".down.sql"
	downContent, err := os.ReadFile(filepath.Join(dir, downName))
	if err != nil {
		t.Fatalf("down-файл не создан: %v", err)
	}
	if !strings.Contains(string(downContent), "DROP TABLE") {
		t.Fatalf("down не содержит DROP TABLE:\n%s", downContent)
	}
	if _, err := os.Stat(filepath.Join(dir, migrate.SumFile)); err != nil {
		t.Fatalf("sorm.sum не создан: %v", err)
	}

	// Up применяет только up-файлы (down не считается pending).
	target := sqliteDB(t)
	applied, err := migrate.Up(ctx, target, "sqlite", dir)
	if err != nil || len(applied) != 1 {
		t.Fatalf("up: %v %v", applied, err)
	}

	// Down откатывает и чистит историю; таблицы исчезли.
	reverted, err := migrate.Down(ctx, target, "sqlite", dir, 1)
	if err != nil || len(reverted) != 1 || reverted[0] != fname {
		t.Fatalf("down: %v %v", reverted, err)
	}
	if _, err := target.Exec(`SELECT 1 FROM users`); err == nil {
		t.Fatal("users должна быть удалена откатом")
	}
	pending, err := migrate.Pending(ctx, target, "sqlite", dir)
	if err != nil || len(pending) != 1 {
		t.Fatalf("после отката pending = %v (err=%v)", pending, err)
	}

	// Повторный Up после отката работает.
	if applied, err = migrate.Up(ctx, target, "sqlite", dir); err != nil || len(applied) != 1 {
		t.Fatalf("re-up: %v %v", applied, err)
	}
}

func TestChecksumTamperDetection(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "migrations")

	dev := sqliteDB(t)
	fname, err := migrate.Diff(ctx, dev, "sqlite", dir, "init")
	if err != nil {
		t.Fatal(err)
	}

	// Подмена применённого файла задним числом.
	path := filepath.Join(dir, fname)
	content, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(content, []byte("\n-- tampered\nDROP TABLE users;\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	target := sqliteDB(t)
	_, err = migrate.Up(ctx, target, "sqlite", dir)
	var se *migrate.SumError
	if !errorsAs(err, &se) || len(se.Modified) != 1 {
		t.Fatalf("подмена не обнаружена: %v", err)
	}

	// Лишний файл, не записанный в sorm.sum.
	if err := os.WriteFile(filepath.Join(dir, "9999_rogue.sql"), []byte("DROP TABLE users;"), 0o644); err != nil {
		t.Fatal(err)
	}
	// возвращаем оригинал, чтобы поймать именно Extra
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = migrate.Up(ctx, target, "sqlite", dir)
	if !errorsAs(err, &se) || len(se.Extra) != 1 || se.Extra[0] != "9999_rogue.sql" {
		t.Fatalf("подложенный файл не обнаружен: %v", err)
	}
}

func errorsAs[T any](err error, target *T) bool {
	for err != nil {
		if t, ok := err.(T); ok {
			*target = t
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
