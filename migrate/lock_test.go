package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/dvislobokov/sorm/migrate"
)

// Гонка реплик: несколько экземпляров одновременно вызывают Up —
// каждый файл миграции должен примениться ровно один раз.
func TestConcurrentUpPostgres(t *testing.T) {
	dsn := pgDSN(t)
	ctx := context.Background()

	setup, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	for _, q := range []string{
		`DROP TABLE IF EXISTS profiles`,
		`DROP TABLE IF EXISTS user_tags`, `DROP TABLE IF EXISTS tags`,
		`DROP TABLE IF EXISTS api_keys`, `DROP TABLE IF EXISTS posts`, `DROP TABLE IF EXISTS users`,
		`DROP TABLE IF EXISTS ` + migrate.HistoryTable,
	} {
		if _, err := setup.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	// Каталог миграций: генерируем диффом на sqlite нельзя (диалект другой) —
	// собираем PG-файл вручную из простых statements.
	dir := t.TempDir()
	writeFile(t, dir, "0001_users.sql", `
CREATE TABLE users (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, email TEXT NOT NULL UNIQUE);
`)
	writeFile(t, dir, "0002_posts.sql", `
CREATE TABLE posts (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, author_id BIGINT NOT NULL REFERENCES users(id));
`)

	const replicas = 8
	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	applied := make(chan int, replicas)

	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			files, err := migrate.Up(ctx, db, "postgres", dir)
			if err != nil {
				errs <- err
				return
			}
			applied <- len(files)
		}()
	}
	wg.Wait()
	close(errs)
	close(applied)

	for err := range errs {
		t.Fatalf("реплика упала: %v", err)
	}
	total := 0
	for n := range applied {
		total += n
	}
	if total != 2 {
		t.Fatalf("суммарно применено %d файлов, want 2 (каждый ровно один раз)", total)
	}

	var cnt int
	if err := setup.QueryRow(`SELECT count(*) FROM ` + migrate.HistoryTable).Scan(&cnt); err != nil || cnt != 2 {
		t.Fatalf("история: %d записей (err=%v), want 2", cnt, err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
