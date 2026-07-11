// Пример sorm: типизированные запросы по сгенерированным дескрипторам.
//
// Без переменных окружения печатает SQL (ToSQL — инспекция без БД).
// С SORM_DSN (postgres://user:pass@host:5432/db) выполняет живой прогон:
// создаёт таблицы, сеет данные и делает запросы.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"sorm"
	"sorm/examples/blog/models"
	gen "sorm/examples/blog/models/sormgen"
)

var (
	a  = gen.Author
	ar = gen.Article
)

func main() {
	fmt.Println("== 1. Инспекция SQL без БД ==")
	demoToSQL()

	dsn := os.Getenv("SORM_DSN")
	if dsn == "" {
		fmt.Println("\nSORM_DSN не задан — живой прогон пропущен.")
		fmt.Println("Пример: $env:SORM_DSN = 'postgres://postgres:postgres@localhost:5432/sorm_example'")
		return
	}
	if err := live(context.Background(), dsn); err != nil {
		log.Fatal(err)
	}
}

func demoToSQL() {
	// Zero-values — полноценные условия (в GORM Active=false молча исчезло бы).
	q := sorm.Query[models.Author](nil).
		Where(a.Active.Eq(false), a.Rating.Eq(0))
	printSQL(q.ToSQL())

	// Динамическая композиция условий — то, что не может sqlc.
	minRating := 4.5
	search := "S"
	q2 := sorm.Query[models.Author](nil).Where(a.Active.Eq(true))
	if minRating > 0 {
		q2 = q2.Where(a.Rating.Gte(minRating))
	}
	if search != "" {
		q2 = q2.Where(a.Name.HasPrefix(search))
	}
	printSQL(q2.OrderBy(a.Rating.Desc()).Limit(10).ToSQL())

	// Билдер иммутабелен: base не «загрязняется» производными запросами.
	base := sorm.Query[models.Article](nil).Where(ar.Views.Gt(100))
	fresh := base.Where(ar.PublishedAt.IsNotNull())
	printSQL(base.ToSQL())
	printSQL(fresh.ToSQL())

	// Фильтр родителя по детям: EXISTS вместо JOIN-дубликатов.
	printSQL(sorm.Query[models.Author](nil).
		Where(a.Articles.Any(ar.Views.Gte(1000))).
		ToSQL())
}

func live(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := seed(ctx, pool); err != nil {
		return err
	}

	fmt.Println("\n== 2. Живые запросы ==")

	active, err := sorm.Query[models.Author](pool).
		Where(a.Active.Eq(true)).
		OrderBy(a.Rating.Desc()).
		All(ctx)
	if err != nil {
		return err
	}
	for _, au := range active {
		fmt.Printf("автор: %-8s rating=%.1f\n", au.Name, au.Rating)
	}

	popular, err := sorm.Query[models.Article](pool).Where(ar.Views.Gte(1000)).Count(ctx)
	if err != nil {
		return err
	}
	fmt.Println("статей с 1000+ просмотров:", popular)

	_, err = sorm.Query[models.Author](pool).Where(a.Name.Eq("нет такого")).One(ctx)
	fmt.Println("One по несуществующему:", err) // sorm: not found — единая семантика

	fmt.Println("\n== 3. Eager loading (Include, split-стратегия) ==")

	// Авторы, у которых есть статья с 1000+ просмотров, вместе с
	// опубликованными статьями каждого.
	authors, err := sorm.Query[models.Author](pool).
		Where(a.Articles.Any(ar.Views.Gte(1000))).
		With(a.Articles.Include(ar.PublishedAt.IsNotNull())).
		OrderBy(a.Name.Asc()).
		All(ctx)
	if err != nil {
		return err
	}
	for _, au := range authors {
		fmt.Printf("%s (%d опубл. статей):\n", au.Name, len(au.Articles))
		for _, art := range au.Articles {
			fmt.Printf("  - %-24s views=%d\n", art.Title, art.Views)
		}
	}

	// Загруженная пустая связь — пустой слайс, НЕ nil («забыл Include» отличим от «нет данных»).
	all, err := sorm.Query[models.Author](pool).With(a.Articles.Include()).All(ctx)
	if err != nil {
		return err
	}
	for _, au := range all {
		fmt.Printf("%s: articles nil=%v len=%d\n", au.Name, au.Articles == nil, len(au.Articles))
	}

	return nil
}

func seed(ctx context.Context, pool *pgxpool.Pool) error {
	stmts := []string{
		`DROP TABLE IF EXISTS articles`,
		`DROP TABLE IF EXISTS authors`,
		`CREATE TABLE authors (
			id        BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			name      TEXT NOT NULL,
			email     TEXT NOT NULL UNIQUE,
			active    BOOLEAN NOT NULL,
			rating    DOUBLE PRECISION NOT NULL,
			joined_at TIMESTAMPTZ NOT NULL,
			version   BIGINT NOT NULL
		)`,
		`CREATE TABLE articles (
			id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			author_id    BIGINT NOT NULL REFERENCES authors(id),
			title        TEXT NOT NULL,
			views        INT NOT NULL,
			published_at TIMESTAMPTZ
		)`,
		`INSERT INTO authors (name, email, active, rating, joined_at, version) VALUES
			('Sam',   'sam@x.io',   true,  4.8, now(), 1),
			('Ira',   'ira@x.io',   true,  4.2, now(), 1),
			('Dorm',  'dorm@x.io',  false, 3.0, now(), 1)`,
		`INSERT INTO articles (author_id, title, views, published_at) VALUES
			(1, 'Go generics deep dive', 5000, now()),
			(1, 'Черновик',              10,   NULL),
			(2, 'Unit of Work в Go',     1500, now())`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	return nil
}

func printSQL(sql string, args []any) {
	fmt.Printf("%s\n  args: %v\n", sql, args)
}
