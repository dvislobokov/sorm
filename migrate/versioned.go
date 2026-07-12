package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Версионные файловые миграции — без внешних инструментов и без docker:
// каталог *.sql + таблица истории sorm_migrations в целевой БД.
//
// Генерация диффа (Diff) использует replay: существующие миграции
// проигрываются на ПУСТОЙ scratch-БД, которую предоставляет пользователь
// (для SQLite достаточно ":memory:"), затем её состояние сравнивается с
// зарегистрированными моделями, и разница записывается новым файлом
// <UTC-timestamp>_<name>.sql.

// HistoryTable — таблица учёта применённых миграций в целевой БД.
const HistoryTable = "sorm_migrations"

// Diff генерирует новый версионный файл миграции в dir.
// dev — пустая одноразовая БД того же диалекта (replay-цель); её содержимое
// уничтожается смыслово: после вызова она находится в состоянии «все миграции
// применены». Возвращает имя созданного файла или "" если изменений нет.
func Diff(ctx context.Context, dev *sql.DB, dialect, dir, name string) (string, error) {
	if _, err := Up(ctx, dev, dialect, dir); err != nil {
		return "", fmt.Errorf("sorm/migrate: replay on dev db: %w", err)
	}
	drv, changes, err := diff(ctx, dev, dialect)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}
	plan, err := drv.PlanChanges(ctx, name, changes)
	if err != nil {
		return "", fmt.Errorf("sorm/migrate: plan: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "-- sorm migration: %s\n", name)
	for _, c := range plan.Changes {
		if c.Comment != "" {
			fmt.Fprintf(&b, "-- %s\n", c.Comment)
		}
		b.WriteString(strings.TrimRight(c.Cmd, ";"))
		b.WriteString(";\n")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	fname := time.Now().UTC().Format("20060102150405") + "_" + sanitizeName(name) + ".sql"
	if err := os.WriteFile(filepath.Join(dir, fname), []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return fname, nil
}

// Up применяет к db все ещё не применённые миграции из dir (по порядку имён
// файлов) и записывает их в HistoryTable. Возвращает применённые файлы.
// На PostgreSQL и SQLite каждый файл применяется в транзакции; MySQL
// коммитит DDL неявно — файл исполняется постейтментно.
//
// Конкурентные вызовы (несколько реплик на старте) сериализуются
// advisory lock'ом — файл не применится дважды.
func Up(ctx context.Context, db *sql.DB, dialect, dir string) ([]string, error) {
	var out []string
	err := withMigrationLock(ctx, db, dialect, func() error {
		applied, err := up(ctx, db, dialect, dir)
		out = applied
		return err
	})
	return out, err
}

func up(ctx context.Context, db *sql.DB, dialect, dir string) ([]string, error) {
	if err := ensureHistory(ctx, db, dialect); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}

	files, err := migrationFiles(dir)
	if err != nil {
		return nil, err
	}

	var done []string
	for _, f := range files {
		if applied[f] {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return done, err
		}
		if err := applyFile(ctx, db, dialect, f, string(content)); err != nil {
			return done, fmt.Errorf("sorm/migrate: %s: %w", f, err)
		}
		done = append(done, f)
	}
	return done, nil
}

// Pending возвращает файлы, которые Up применил бы (без применения).
func Pending(ctx context.Context, db *sql.DB, dialect, dir string) ([]string, error) {
	if err := ensureHistory(ctx, db, dialect); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	files, err := migrationFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		if !applied[f] {
			out = append(out, f)
		}
	}
	return out, nil
}

func applyFile(ctx context.Context, db *sql.DB, dialect, file, content string) error {
	stmts := SplitStatements(content)
	record := fmt.Sprintf(
		"INSERT INTO %s (version) VALUES (%s)",
		HistoryTable, placeholder(dialect, 1),
	)

	if dialect == "mysql" {
		// DDL в MySQL коммитится неявно — транзакция бессмысленна.
		for _, s := range stmts {
			if _, err := db.ExecContext(ctx, s); err != nil {
				return err
			}
		}
		_, err := db.ExecContext(ctx, record, file)
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, record, file); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func ensureHistory(ctx context.Context, db *sql.DB, dialect string) error {
	var ddl string
	switch dialect {
	case "mysql":
		ddl = "CREATE TABLE IF NOT EXISTS " + HistoryTable +
			" (`version` VARCHAR(255) PRIMARY KEY, `applied_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)"
	default: // postgres, sqlite
		ddl = "CREATE TABLE IF NOT EXISTS " + HistoryTable +
			` ("version" VARCHAR(255) PRIMARY KEY, "applied_at" TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`
	}
	_, err := db.ExecContext(ctx, ddl)
	if err != nil {
		return fmt.Errorf("sorm/migrate: history table: %w", err)
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM "+HistoryTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

func migrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil // нет каталога = нет миграций
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// SplitStatements — разбор *.sql файла миграции: строки-комментарии
// отбрасываются, статименты разделяются «;». Файлы пишет Diff — по одному
// статименту на строку; литеральные «;» внутри строк не поддерживаются.
func SplitStatements(content string) []string {
	var stmts []string
	for _, chunk := range strings.Split(content, ";") {
		var keep []string
		for _, ln := range strings.Split(chunk, "\n") {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "--") {
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

func placeholder(dialect string, n int) string {
	if dialect == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

var nameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func sanitizeName(s string) string {
	s = nameSanitizer.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}
