package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
	pgm "github.com/dvislobokov/sorm/internal/pgmodels"
	pggen "github.com/dvislobokov/sorm/internal/pgmodels/sormgen"
	"github.com/dvislobokov/sorm/internal/ddl"
	"github.com/dvislobokov/sorm/internal/parse"
)

// Custom scalars (driver.Valuer + sql.Scanner): roundtrip, predicates and
// snapshot diffing — the Go type is not comparable-constrained.

func runCentsScenario(t *testing.T, db sorm.DB, dialect string) {
	t.Helper()
	ctx := context.Background()
	c := gen.NewContext(db)

	owner := &models.User{Email: "cents-" + dialect + "@a.b", Name: "O", Active: true, CreatedAt: nowNoZero()}
	dev := &models.Device{ID: uuid.New(), Owner: owner, Name: "meter", Price: models.Cents{Units: 1999}}
	c.Users.Add(owner)
	c.Devices.Add(dev)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatalf("%s: %v", dialect, err)
	}

	// Roundtrip + typed predicate (comparison happens in SQL).
	got, err := c.Devices.NoTracking().
		Where(gen.Device.Price.Gt(models.Cents{Units: 1000})).
		One(ctx)
	if err != nil || got.Price.Units != 1999 {
		t.Fatalf("%s: roundtrip %+v (err=%v)", dialect, got, err)
	}

	// Diff: price change → UPDATE; reload confirms.
	tracked, err := c.Devices.Where(gen.Device.Name.Eq("meter")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tracked.Price = models.Cents{Units: 2500}
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatalf("%s: update: %v", dialect, err)
	}
	again, err := c.Devices.NoTracking().Where(gen.Device.Price.Eq(models.Cents{Units: 2500})).One(ctx)
	if err != nil || again.Price.Units != 2500 {
		t.Fatalf("%s: after update %+v (err=%v)", dialect, again, err)
	}
}

func TestScalarSQLite(t *testing.T) { runCentsScenario(t, sqliteDB(t), "sqlite") }
func TestScalarPG(t *testing.T)     { runCentsScenario(t, testPool(t), "postgres") }
func TestScalarMySQL(t *testing.T)  { runCentsScenario(t, mysqlDB(t), "mysql") }

// Native PG arrays: DDL, roundtrip, predicates; guard on other dialects.

func TestArrayPG(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Tables from the generated DDL for the PG-only schema.
	s, err := parse.Load("./internal/pgmodels")
	if err != nil {
		t.Fatal(err)
	}
	sqlText, err := ddl.Generate(s, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `DROP TABLE IF EXISTS articles`)
	for _, chunk := range strings.Split(sqlText, ";") {
		var keep []string
		for _, ln := range strings.Split(chunk, "\n") {
			if tl := strings.TrimSpace(ln); tl != "" && !strings.HasPrefix(tl, "--") {
				keep = append(keep, ln)
			}
		}
		if st := strings.TrimSpace(strings.Join(keep, "\n")); st != "" {
			mustExec(t, pool, st)
		}
	}

	sess := sorm.NewSession(pool)
	a1 := &pgm.Article{Title: "go", Tags: []string{"go", "orm"}, Nums: []int64{1, 2}}
	a2 := &pgm.Article{Title: "db", Tags: []string{"db"}}
	a3 := &pgm.Article{Title: "none"} // nil slices ⇒ NULL
	sorm.Add(sess, a1, a2, a3)
	if err := sess.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	ar := pggen.Article
	// Contains (@>), Has, Overlaps (&&), IsNull.
	got, err := sorm.Query[pgm.Article](pool).Where(ar.Tags.Contains("go", "orm")).All(ctx)
	if err != nil || len(got) != 1 || got[0].Title != "go" {
		t.Fatalf("Contains: %v (err=%v)", got, err)
	}
	if got[0].Tags[1] != "orm" || got[0].Nums[1] != 2 {
		t.Fatalf("roundtrip: %+v", got[0])
	}
	overl, err := sorm.Query[pgm.Article](pool).Where(ar.Tags.Overlaps("db", "misc")).All(ctx)
	if err != nil || len(overl) != 1 || overl[0].Title != "db" {
		t.Fatalf("Overlaps: %v (err=%v)", overl, err)
	}
	if n, err := sorm.Query[pgm.Article](pool).Where(ar.Tags.IsNull()).Count(ctx); err != nil || n != 1 {
		t.Fatalf("IsNull count = %d (err=%v)", n, err)
	}

	// Diff on an array column → UPDATE.
	tr, err := sorm.Track[pgm.Article](sess).Where(ar.Title.Eq("db")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tr.Tags = append(tr.Tags, "extra")
	if err := sess.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := sorm.Query[pgm.Article](pool).Where(ar.Tags.Has("extra")).Count(ctx); n != 1 {
		t.Fatal("array update not persisted")
	}
}

func TestArrayGuardOnSQLite(t *testing.T) {
	db := sqliteDB(t)
	_, err := sorm.Query[pgm.Article](db).
		Where(pggen.Article.Tags.Has("x")).
		All(context.Background())
	if err == nil || !strings.Contains(err.Error(), "only supported on postgres") {
		t.Fatalf("expected array guard error, got %v", err)
	}
}
