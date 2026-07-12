package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

func TestSubquerySQL(t *testing.T) {
	// IN-subquery: shared placeholder numbering with the outer predicate.
	flagged := sorm.Pick(gen.Post.AuthorID,
		sorm.Query[models.Post](nil).Where(gen.Post.Views.Gt(100)))
	sql, args := sorm.Query[models.User](nil).
		Where(u.Active.Eq(true), sorm.InQuery(u.ID, flagged)).
		ToSQL()
	want := `"id" IN (SELECT "author_id" FROM "posts" WHERE "views" > $2)`
	if !strings.Contains(sql, want) || !strings.Contains(sql, `"active" = $1`) {
		t.Fatalf("sql: %s", sql)
	}
	if len(args) != 2 || args[0] != true || args[1] != 100 {
		t.Fatalf("args: %v", args)
	}

	// Scalar subquery in a comparison.
	minAge := sorm.PickScalar(sorm.Min[models.User](u.Age), sorm.Query[models.User](nil))
	sql, _ = sorm.Query[models.User](nil).Where(sorm.GtQ(u.Age, minAge)).ToSQL()
	if !strings.Contains(sql, `"age" > (SELECT min("age") FROM "users")`) {
		t.Fatalf("scalar sql: %s", sql)
	}

	// ForUpdate renders after LIMIT; sqlite is a build error.
	sql, _ = sorm.Query[models.User](nil).Where(u.ID.Eq(1)).Limit(1).ForUpdate().ToSQL()
	if !strings.HasSuffix(sql, "LIMIT 1 FOR UPDATE") {
		t.Fatalf("lock sql: %s", sql)
	}
	sql, _ = sorm.Query[models.User](nil).ForUpdateSkipLocked().ToSQL()
	if !strings.HasSuffix(sql, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("skip locked sql: %s", sql)
	}
}

func TestForUpdateSQLiteGuard(t *testing.T) {
	db := sqliteDB(t)
	_, err := sorm.Query[models.User](db).ForUpdate().All(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not supported on sqlite") {
		t.Fatalf("want sqlite guard error, got %v", err)
	}
}

func runSubqueryScenario(t *testing.T, db sorm.DB, dialect string) {
	t.Helper()
	ctx := context.Background()

	c := gen.NewContext(db)
	young := &models.User{Email: "sq-young@" + dialect, Name: "Young", Active: true, Age: 20, CreatedAt: nowNoZero()}
	old := &models.User{Email: "sq-old@" + dialect, Name: "Old", Active: true, Age: 60, CreatedAt: nowNoZero()}
	c.Users.Add(young, old)
	c.Posts.Add(&models.Post{Author: old, Title: "hot", Views: 500})
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// IN-subquery: authors of posts with 500+ views.
	authors, err := sorm.Query[models.User](db).
		Where(sorm.InQuery(gen.User.ID,
			sorm.Pick(gen.Post.AuthorID,
				sorm.Query[models.Post](nil).Where(gen.Post.Views.Gte(500))))).
		All(ctx)
	if err != nil {
		t.Fatalf("%s: %v", dialect, err)
	}
	if len(authors) != 1 || authors[0].Name != "Old" {
		t.Fatalf("%s: InQuery = %v", dialect, names(authors))
	}

	// Scalar: users older than the youngest (min(age) over sq- users).
	older, err := sorm.Query[models.User](db).
		Where(
			gen.User.Email.HasPrefix("sq-"),
			sorm.GtQ(gen.User.Age,
				sorm.PickScalar(sorm.Min[models.User](gen.User.Age),
					sorm.Query[models.User](nil).Where(gen.User.Email.HasPrefix("sq-"))))).
		All(ctx)
	if err != nil {
		t.Fatalf("%s: %v", dialect, err)
	}
	if len(older) != 1 || older[0].Name != "Old" {
		t.Fatalf("%s: GtQ = %v", dialect, names(older))
	}

	// NotInQuery.
	quiet, err := sorm.Query[models.User](db).
		Where(
			gen.User.Email.HasPrefix("sq-"),
			sorm.NotInQuery(gen.User.ID,
				sorm.Pick(gen.Post.AuthorID, sorm.Query[models.Post](nil))),
		).
		All(ctx)
	if err != nil || len(quiet) != 1 || quiet[0].Name != "Young" {
		t.Fatalf("%s: NotInQuery = %v (err=%v)", dialect, names(quiet), err)
	}
}

func names(us []*models.User) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.Name
	}
	return out
}

func TestSubquerySQLite(t *testing.T) { runSubqueryScenario(t, sqliteDB(t), "sqlite") }
func TestSubqueryPG(t *testing.T)     { runSubqueryScenario(t, testPool(t), "postgres") }
func TestSubqueryMySQL(t *testing.T)  { runSubqueryScenario(t, mysqlDB(t), "mysql") }

// ForUpdate end-to-end: locked read inside RunInTx on PostgreSQL.
func TestForUpdatePG(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	acc := &models.User{Email: "lock@pg", Name: "L", Active: true, Balance: 100, CreatedAt: nowNoZero()}
	c.Users.Add(acc)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	err := sorm.RunInTx(ctx, db, func(tx sorm.Tx) error {
		s := sorm.NewSession(tx)
		u, err := sorm.Track[models.User](s).
			Where(gen.User.Email.Eq("lock@pg")).
			ForUpdate().
			One(ctx)
		if err != nil {
			return err
		}
		u.Balance = 50
		return s.SaveChanges(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := sorm.Query[models.User](db).Where(gen.User.Email.Eq("lock@pg")).One(ctx)
	if got.Balance != 50 {
		t.Fatalf("balance = %v", got.Balance)
	}
}
