// sorm example: typed queries over generated descriptors.
//
// Without environment variables it prints SQL (ToSQL — inspection without a DB).
// With SORM_DSN (postgres://user:pass@host:5432/db) it performs a live run:
// creates tables, seeds data and runs queries.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect/lite"
	"github.com/dvislobokov/sorm/driver/pgxd"
	"github.com/dvislobokov/sorm/driver/sqld"
	"github.com/dvislobokov/sorm/examples/blog/models"
	"github.com/dvislobokov/sorm/migrate"
	gen "github.com/dvislobokov/sorm/examples/blog/models/sormgen"
)

var (
	a  = gen.Author
	ar = gen.Article
)

func main() {
	fmt.Println("== 1. SQL inspection without a DB ==")
	demoToSQL()

	dsn := os.Getenv("SORM_DSN")
	if dsn == "" {
		fmt.Println("\nSORM_DSN is not set — live run skipped.")
		fmt.Println("Example: $env:SORM_DSN = 'postgres://postgres:postgres@localhost:5432/sorm_example'")
		return
	}
	if err := live(context.Background(), dsn); err != nil {
		log.Fatal(err)
	}
}

func demoToSQL() {
	// Zero values are first-class conditions (in GORM Active=false would silently disappear).
	q := sorm.Query[models.Author](nil).
		Where(a.Active.Eq(false), a.Rating.Eq(0))
	printSQL(q.ToSQL())

	// Dynamic condition composition — something sqlc cannot do.
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

	// The builder is immutable: base is not "polluted" by derived queries.
	base := sorm.Query[models.Article](nil).Where(ar.Views.Gt(100))
	fresh := base.Where(ar.PublishedAt.IsNotNull())
	printSQL(base.ToSQL())
	printSQL(fresh.ToSQL())

	// Filtering parents by their children: EXISTS instead of JOIN duplicates.
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
	db := pgxd.Wrap(pool) // the single driver binding point

	if err := seed(ctx, db); err != nil {
		return err
	}

	fmt.Println("\n== 2. Live queries ==")

	active, err := sorm.Query[models.Author](db).
		Where(a.Active.Eq(true)).
		OrderBy(a.Rating.Desc()).
		All(ctx)
	if err != nil {
		return err
	}
	for _, au := range active {
		fmt.Printf("author: %-8s rating=%.1f\n", au.Name, au.Rating)
	}

	popular, err := sorm.Query[models.Article](db).Where(ar.Views.Gte(1000)).Count(ctx)
	if err != nil {
		return err
	}
	fmt.Println("articles with 1000+ views:", popular)

	_, err = sorm.Query[models.Author](db).Where(a.Name.Eq("no such author")).One(ctx)
	fmt.Println("One on a missing row:", err) // sorm: not found — uniform semantics

	fmt.Println("\n== 3. Eager loading (Include, split strategy) ==")

	// Authors who have an article with 1000+ views, together with
	// each author's published articles.
	authors, err := sorm.Query[models.Author](db).
		Where(a.Articles.Any(ar.Views.Gte(1000))).
		With(a.Articles.Include(ar.PublishedAt.IsNotNull())).
		OrderBy(a.Name.Asc()).
		All(ctx)
	if err != nil {
		return err
	}
	for _, au := range authors {
		fmt.Printf("%s (%d published articles):\n", au.Name, len(au.Articles))
		for _, art := range au.Articles {
			fmt.Printf("  - %-24s views=%d\n", art.Title, art.Views)
		}
	}

	// A loaded empty relation is an empty slice, NOT nil ("forgot Include" is distinguishable from "no data").
	all, err := sorm.Query[models.Author](db).With(a.Articles.Include()).All(ctx)
	if err != nil {
		return err
	}
	for _, au := range all {
		fmt.Printf("%s: articles nil=%v len=%d\n", au.Name, au.Articles == nil, len(au.Articles))
	}

	return session(ctx, db)
}

// session — sorm's main differentiator: Unit of Work with change tracking.
func session(ctx context.Context, db sorm.DB) error {
	fmt.Println("\n== 4. Session: Unit of Work ==")

	// A new graph: FKs come from navigation; insert order and fixup are handled by sorm.
	s := sorm.NewSession(db)
	max := &models.Author{Name: "Max", Email: "max@x.io", Active: true, Rating: 4.0, JoinedAt: time.Now()}
	art1 := &models.Article{Author: max, Title: "Snapshot tracking in Go", Views: 0}
	art2 := &models.Article{Author: max, Title: "pgx.Batch in practice", Views: 0}
	sorm.Add(s, max)
	sorm.Add(s, art1, art2)
	if err := s.SaveChanges(ctx); err != nil {
		return err
	}
	fmt.Printf("graph inserted: author.id=%d, article fk=[%d %d] (RETURNING + fixup)\n",
		max.ID, art1.AuthorID, art2.AuthorID)

	// Track → mutate with plain Go code → SaveChanges: UPDATE only the
	// changed columns, version predicate for free.
	s2 := sorm.NewSession(db)
	loaded, err := sorm.Track[models.Author](s2).
		Where(gen.Author.Email.Eq("max@x.io")).
		With(gen.Author.Articles.Include()).
		One(ctx)
	if err != nil {
		return err
	}
	loaded.Rating = 4.9                    // changed the author
	loaded.Articles[0].Views = 100         // and a child loaded via Include
	if err := s2.SaveChanges(ctx); err != nil {
		return err
	}
	fmt.Printf("update: rating=%.1f (version %d), article views=%d — one roundtrip\n",
		loaded.Rating, loaded.Version, loaded.Articles[0].Views)

	// Optimistic concurrency: a concurrent modification → typed conflict.
	sA, sB := sorm.NewSession(db), sorm.NewSession(db)
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
		fmt.Printf("concurrent modification caught: %v\n", conflict)
	}

	// Remove: children are deleted before the parent automatically.
	s3 := sorm.NewSession(db)
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
	fmt.Println("author and articles deleted in a single SaveChanges (ordering handled by sorm)")

	return contextDemo(ctx, db)
}

// contextDemo — the generated Context: DbContext-style facade over the
// session. Sets are tracked query roots; Find checks the identity map
// before touching the DB; RunInTx gives a fresh child context per attempt.
func contextDemo(ctx context.Context, db sorm.DB) error {
	fmt.Println("\n== 5. Generated Context (EF Core DbContext style) ==")

	c := gen.NewContext(db)

	// Tracked read through the set → plain mutation → SaveChanges.
	sam, err := c.Authors.Where(a.Email.Eq("sam@x.io")).One(ctx)
	if err != nil {
		return err
	}
	sam.Rating = 4.95
	if err := c.SaveChanges(ctx); err != nil {
		return err
	}
	fmt.Printf("context update: %s rating=%.2f (no manual Track)\n", sam.Name, sam.Rating)

	// Find: the entity is already tracked — no SQL, same pointer.
	same, err := c.Authors.Find(ctx, sam.ID)
	if err != nil {
		return err
	}
	fmt.Printf("Find from identity map: same pointer=%v\n", same == sam)

	// Several operations atomically: a fresh child context per attempt,
	// SaveChanges inside joins this transaction (no nesting).
	err = c.RunInTx(ctx, func(txc *gen.Context) error {
		ira, err := txc.Authors.Where(a.Email.Eq("ira@x.io")).One(ctx)
		if err != nil {
			return err
		}
		ira.Active = false
		if err := txc.SaveChanges(ctx); err != nil {
			return err
		}
		txc.Articles.Add(&models.Article{AuthorID: ira.ID, Title: "farewell post", Views: 1})
		return txc.SaveChanges(ctx) // both flushes commit together
	})
	if err != nil {
		return err
	}
	fmt.Println("RunInTx: two SaveChanges committed atomically")

	return setBasedAndRaw(ctx, db)
}

// setBasedAndRaw — sessionless operations: bulk UPDATE/DELETE and the raw escape hatch.
func setBasedAndRaw(ctx context.Context, db sorm.DB) error {
	fmt.Println("\n== 6. Set-based operations and raw SQL ==")

	// Bulk UPDATE: typed assignments, automatic version increment.
	// Update without Where does not fail silently — it returns a guard error;
	// touching the whole table requires an explicit AllRows().
	n, err := sorm.Update[models.Article](db).
		Set(gen.Article.Views.Set(0)).
		Where(gen.Article.PublishedAt.IsNull()).
		Exec(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("views reset for %d drafts\n", n)

	// Raw into an entity: a column mismatch is a ScanError, not a half-empty object.
	top, err := sorm.Raw[models.Article](db,
		`SELECT * FROM articles WHERE views >= $1 ORDER BY views DESC`, 1000).All(ctx)
	if err != nil {
		return err
	}
	for _, art := range top {
		fmt.Printf("raw: %-24s views=%d\n", art.Title, art.Views)
	}

	// RawAs into an arbitrary struct: aggregates that don't match an entity shape.
	type authorStat struct {
		Name     string
		Articles int64 `sorm:"n"`
		MaxViews int64 `sorm:"max_views"`
	}
	stats, err := sorm.RawAs[authorStat](db, `
		SELECT a.name, count(*) AS n, max(ar.views) AS max_views
		FROM authors a JOIN articles ar ON ar.author_id = a.id
		GROUP BY a.name ORDER BY max_views DESC`).All(ctx)
	if err != nil {
		return err
	}
	for _, st := range stats {
		fmt.Printf("stat: %-8s articles=%d max=%d\n", st.Name, st.Articles, st.MaxViews)
	}

	return projections(ctx, db)
}

// projections — typed GROUP BY/HAVING/JOIN without SQL strings.
func projections(ctx context.Context, db sorm.DB) error {
	fmt.Println("\n== 7. Projections: typed aggregations and JOINs ==")

	// The same aggregate as in the raw section, but compiler-checked:
	// a typo in a column name or a mismatched result struct is an error
	// before ever hitting the DB.
	type authorStat struct {
		Name     string
		N        int64
		MaxViews int64 `sorm:"max_views"`
	}
	stats, err := sorm.Project[authorStat](
		sorm.From[models.Author](db).
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
		fmt.Printf("proj: %-8s articles=%d max=%d\n", st.Name, st.N, st.MaxViews)
	}

	// LEFT JOIN via the relation: authors without articles are included too (count = 0).
	type withCount struct {
		Name string
		N    int64
	}
	all, err := sorm.Project[withCount](
		sorm.From[models.Author](db).
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
		fmt.Printf("left join: %-8s articles=%d\n", row.Name, row.N)
	}

	return multiDialect(ctx)
}

// multiDialect — the same sorm code on top of in-memory SQLite: only the
// adapter changes (sqld.Wrap instead of pgxd.Wrap). MySQL works the same way,
// with dialect/my. The schema is created by migrations from code (Atlas SDK):
// no handwritten DDL.
func multiDialect(ctx context.Context) error {
	fmt.Println("\n== 8. Multi-dialect + migrations from code (in-memory SQLite) ==")

	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer sdb.Close()
	sdb.SetMaxOpenConns(1)

	// The desired schema comes from the models (sormgen registers TableDef);
	// Atlas inspects the DB and applies the diff.
	plan, err := migrate.Plan(ctx, sdb, "sqlite")
	if err != nil {
		return err
	}
	fmt.Printf("migrate.Plan: %d statement(s), e.g.: %s\n", len(plan), plan[0])
	if err := migrate.Apply(ctx, sdb, "sqlite"); err != nil {
		return err
	}
	if again, _ := migrate.Plan(ctx, sdb, "sqlite"); len(again) == 0 {
		fmt.Println("migrate.Apply done; a repeated Plan is empty — schema is in sync")
	}

	db := sqld.Wrap(sdb, lite.Dialect{}) // the only difference from the PG path

	s := sorm.NewSession(db)
	lo := &models.Author{Name: "Lo", Email: "lo@x.io", Active: true, Rating: 3.3, JoinedAt: time.Now()}
	sorm.Add(s, lo)
	sorm.Add(s, &models.Article{Author: lo, Title: "sqlite works", Views: 7})
	if err := s.SaveChanges(ctx); err != nil {
		return err
	}

	found, err := sorm.Query[models.Author](db).
		Where(gen.Author.Articles.Any(gen.Article.Views.Gt(0))).
		One(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("SQLite: author %s (id=%d via LastInsertId), version=%d — same code, different driver\n",
		found.Name, found.ID, found.Version)

	return versionedMigrations(ctx)
}

// versionedMigrations — file-based migrations from code: Diff writes a
// versioned *.sql (you provide the scratch DB for replay — here an in-memory
// SQLite), Up applies it to the target DB and maintains the sorm_migrations
// history table. No external tools and no docker magic.
func versionedMigrations(ctx context.Context) error {
	fmt.Println("\n== 9. Versioned migrations from code ==")

	dir := filepath.Join(os.TempDir(), "sorm-blog-migrations")
	_ = os.RemoveAll(dir)

	scratch, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer scratch.Close()
	scratch.SetMaxOpenConns(1)

	fname, err := migrate.Diff(ctx, scratch, "sqlite", dir, "init schema")
	if err != nil {
		return err
	}
	fmt.Println("migration file created:", fname)

	target, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer target.Close()
	target.SetMaxOpenConns(1)

	applied, err := migrate.Up(ctx, target, "sqlite", dir)
	if err != nil {
		return err
	}
	fmt.Printf("files applied: %d; repeated Up: ", len(applied))
	again, err := migrate.Up(ctx, target, "sqlite", dir)
	if err != nil {
		return err
	}
	fmt.Printf("%d (history in table %s)\n", len(again), migrate.HistoryTable)
	return nil
}

func seed(ctx context.Context, db sorm.DB) error {
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
			(1, 'Draft',                 10,   NULL),
			(2, 'Unit of Work in Go',    1500, now())`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(ctx, s); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	return nil
}

func printSQL(sql string, args []any) {
	fmt.Printf("%s\n  args: %v\n", sql, args)
}
