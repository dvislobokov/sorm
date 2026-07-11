// Пример sorm: типизированные запросы по сгенерированным дескрипторам.
//
// Без переменных окружения печатает SQL (ToSQL — инспекция без БД).
// С SORM_DSN (postgres://user:pass@host:5432/db) выполняет живой прогон:
// создаёт таблицы, сеет данные и делает запросы.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

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

	return session(ctx, pool)
}

// session — главный дифференциатор sorm: Unit of Work с change tracking.
func session(ctx context.Context, pool *pgxpool.Pool) error {
	fmt.Println("\n== 4. Сессия: Unit of Work ==")

	// Новый граф: FK задаётся навигацией, порядок вставки и fixup — за sorm.
	s := sorm.NewSession(pool)
	max := &models.Author{Name: "Max", Email: "max@x.io", Active: true, Rating: 4.0, JoinedAt: time.Now()}
	art1 := &models.Article{Author: max, Title: "Снапшот-трекинг в Go", Views: 0}
	art2 := &models.Article{Author: max, Title: "pgx.Batch на практике", Views: 0}
	sorm.Add(s, max)
	sorm.Add(s, art1, art2)
	if err := s.SaveChanges(ctx); err != nil {
		return err
	}
	fmt.Printf("вставлен граф: author.id=%d, статьи fk=[%d %d] (RETURNING + fixup)\n",
		max.ID, art1.AuthorID, art2.AuthorID)

	// Track → мутация обычным Go-кодом → SaveChanges: UPDATE только
	// изменённых колонок, version-предикат бесплатно.
	s2 := sorm.NewSession(pool)
	loaded, err := sorm.Track[models.Author](s2).
		Where(gen.Author.Email.Eq("max@x.io")).
		With(gen.Author.Articles.Include()).
		One(ctx)
	if err != nil {
		return err
	}
	loaded.Rating = 4.9                    // изменили автора
	loaded.Articles[0].Views = 100         // и ребёнка, загруженного Include
	if err := s2.SaveChanges(ctx); err != nil {
		return err
	}
	fmt.Printf("update: rating=%.1f (version %d), article views=%d — один roundtrip\n",
		loaded.Rating, loaded.Version, loaded.Articles[0].Views)

	// Optimistic concurrency: конкурентное изменение → типизированный конфликт.
	sA, sB := sorm.NewSession(pool), sorm.NewSession(pool)
	mA, _ := sorm.Track[models.Author](sA).Where(gen.Author.Email.Eq("max@x.io")).One(ctx)
	mB, _ := sorm.Track[models.Author](sB).Where(gen.Author.Email.Eq("max@x.io")).One(ctx)
	mA.Rating = 5.0
	if err := sA.SaveChanges(ctx); err != nil {
		return err
	}
	mB.Rating = 1.0
	err = sB.SaveChanges(ctx)
	var conflict *sorm.ConflictError
	if errors.As(err, &conflict) {
		fmt.Printf("конкурентное изменение поймано: %v\n", conflict)
	}

	// Remove: дети удаляются раньше родителя автоматически.
	s3 := sorm.NewSession(pool)
	gone, err := sorm.Track[models.Author](s3).
		Where(gen.Author.Email.Eq("max@x.io")).
		With(gen.Author.Articles.Include()).
		One(ctx)
	if err != nil {
		return err
	}
	sorm.Remove(s3, gone.Articles...)
	sorm.Remove(s3, gone)
	if err := s3.SaveChanges(ctx); err != nil {
		return err
	}
	fmt.Println("автор и статьи удалены одним SaveChanges (порядок — за sorm)")

	return setBasedAndRaw(ctx, pool)
}

// setBasedAndRaw — операции без сессии: массовые UPDATE/DELETE и raw-escape.
func setBasedAndRaw(ctx context.Context, pool *pgxpool.Pool) error {
	fmt.Println("\n== 5. Set-based операции и raw SQL ==")

	// Массовый UPDATE: типизированные присваивания, автоинкремент version.
	// Update без Where не скомпилируется в ошибку молча — вернёт guard-ошибку,
	// полная таблица только через явный AllRows().
	n, err := sorm.Update[models.Article](pool).
		Set(gen.Article.Views.Set(0)).
		Where(gen.Article.PublishedAt.IsNull()).
		Exec(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("обнулены просмотры у %d черновиков\n", n)

	// Raw в сущности: несоответствие колонок — ScanError, не полупустой объект.
	top, err := sorm.Raw[models.Article](pool,
		`SELECT * FROM articles WHERE views >= $1 ORDER BY views DESC`, 1000).All(ctx)
	if err != nil {
		return err
	}
	for _, art := range top {
		fmt.Printf("raw: %-24s views=%d\n", art.Title, art.Views)
	}

	// RawAs в произвольную структуру: агрегаты, которые не форма сущности.
	type authorStat struct {
		Name     string
		Articles int64 `sorm:"n"`
		MaxViews int64 `sorm:"max_views"`
	}
	stats, err := sorm.RawAs[authorStat](pool, `
		SELECT a.name, count(*) AS n, max(ar.views) AS max_views
		FROM authors a JOIN articles ar ON ar.author_id = a.id
		GROUP BY a.name ORDER BY max_views DESC`).All(ctx)
	if err != nil {
		return err
	}
	for _, st := range stats {
		fmt.Printf("stat: %-8s статей=%d max=%d\n", st.Name, st.Articles, st.MaxViews)
	}

	return projections(ctx, pool)
}

// projections — типизированные GROUP BY/HAVING/JOIN без строк SQL.
func projections(ctx context.Context, pool *pgxpool.Pool) error {
	fmt.Println("\n== 6. Проекции: типизированные агрегации и JOIN ==")

	// Тот же агрегат, что в raw-секции, но проверенный компилятором:
	// опечатка в колонке или несовпадение структуры результата — ошибка
	// до похода в БД.
	type authorStat struct {
		Name     string
		N        int64
		MaxViews int64 `sorm:"max_views"`
	}
	stats, err := sorm.Project[authorStat](
		sorm.From[models.Author](pool).
			Join(a.Articles.InnerJoin()).
			GroupBy(a.Name).
			Having(sorm.CountAll[models.Author]().Gte(1)).
			OrderBy(a.Name.Asc()),
		sorm.Field(a.Name),
		sorm.As(sorm.CountAll[models.Author](), "n"),
		sorm.As(sorm.Max[models.Author](ar.Views), "max_views"),
	).All(ctx)
	if err != nil {
		return err
	}
	for _, st := range stats {
		fmt.Printf("proj: %-8s статей=%d max=%d\n", st.Name, st.N, st.MaxViews)
	}

	// LEFT JOIN по связи: авторы без статей тоже в результате (count = 0).
	type withCount struct {
		Name string
		N    int64
	}
	all, err := sorm.Project[withCount](
		sorm.From[models.Author](pool).
			Join(a.Articles.LeftJoin()).
			GroupBy(a.Name).
			OrderBy(a.Name.Asc()),
		sorm.Field(a.Name),
		sorm.As(sorm.Count[models.Author](ar.ID), "n"),
	).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range all {
		fmt.Printf("left join: %-8s статей=%d\n", row.Name, row.N)
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
