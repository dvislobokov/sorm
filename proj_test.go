package sorm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sorm"
	models "sorm/internal/testmodels"
	gen "sorm/internal/testmodels/sormgen"
)

type ageStatP struct {
	Age int
	N   int64 `sorm:"n"`
}

func TestProjectGroupByToSQL(t *testing.T) {
	q := sorm.Project[ageStatP](
		sorm.From[models.User](nil).
			Where(u.Active.Eq(true)).
			GroupBy(u.Age).
			Having(sorm.CountAll[models.User]().Gt(10)).
			OrderBy(u.Age.Asc()),
		sorm.Field(u.Age),
		sorm.As(sorm.CountAll[models.User](), "n"),
	)
	sql, args, err := q.ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "users"."age" AS "age", count(*) AS "n" FROM "users"` +
		` WHERE "users"."active" = $1 GROUP BY "users"."age" HAVING count(*) > $2 ORDER BY "users"."age"`
	if sql != want {
		t.Errorf("got:  %s\nwant: %s", sql, want)
	}
	if len(args) != 2 || args[0] != true || args[1] != int64(10) {
		t.Errorf("args = %v", args)
	}
}

func TestProjectJoinToSQL(t *testing.T) {
	type row struct {
		Name string
		N    int64 `sorm:"n"`
	}
	p := gen.Post
	q := sorm.Project[row](
		sorm.From[models.User](nil).
			Join(u.Posts.LeftJoin()).
			GroupBy(u.Name),
		sorm.Field(u.Name),
		sorm.As(sorm.Count[models.User](p.ID), "n"), // считаем по колонке ребёнка
	)
	sql, _, err := q.ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "users"."name" AS "name", count("posts"."id") AS "n" FROM "users"` +
		` LEFT JOIN "posts" ON "posts"."author_id" = "users"."id" GROUP BY "users"."name"`
	if sql != want {
		t.Errorf("got:  %s\nwant: %s", sql, want)
	}
}

func TestProjectArbitraryJoin(t *testing.T) {
	type row struct {
		Email string
		Title string
	}
	p := gen.Post
	q := sorm.Project[row](
		sorm.From[models.User](nil).
			Join(sorm.InnerJoinOn(sorm.ColEq(p.ID, u.ID))), // типы значений совпадают — компилируется
		sorm.Field(u.Email),
		sorm.FieldOf[models.User](p.Title), // колонка присоединённой сущности
	)
	sql, _, err := q.ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `INNER JOIN "posts" ON "posts"."id" = "users"."id"`) {
		t.Errorf("sql = %s", sql)
	}
}

func TestAggregateInWhereRejected(t *testing.T) {
	q := sorm.Project[ageStatP](
		sorm.From[models.User](nil).
			Where(sorm.CountAll[models.User]().Gt(1)), // агрегат в Where — ошибка
		sorm.Field(u.Age),
		sorm.As(sorm.CountAll[models.User](), "n"),
	)
	_, _, err := q.ToSQL()
	if err == nil || !strings.Contains(err.Error(), "Having") {
		t.Fatalf("ожидали ошибку про Having, получили %v", err)
	}
}

func TestProjectStrictMapping(t *testing.T) {
	type wrong struct {
		Age   int
		Bogus string // нет соответствующего выражения
	}
	q := sorm.Project[wrong](
		sorm.From[models.User](nil).GroupBy(u.Age),
		sorm.Field(u.Age),
	)
	_, _, err := q.ToSQL()
	var se *sorm.ScanError
	if !errors.As(err, &se) || len(se.Extra) != 1 || se.Extra[0] != "bogus" {
		t.Fatalf("err = %v", err)
	}
}

func TestProjectIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)
	mustExec(t, pool, `INSERT INTO users (email, name, active, age, balance, created_at, version)
		VALUES ('b@b.c', 'Bob', true, 30, 0, now(), 1), ('c@b.c', 'Cid', false, 40, 0, now(), 1)`)
	mustExec(t, pool, `INSERT INTO posts (author_id, title, body, views) VALUES (1, 't1', '', 0), (1, 't2', '', 0), (2, 't3', '', 0)`)

	// GROUP BY + HAVING.
	stats, err := sorm.Project[ageStatP](
		sorm.From[models.User](pool).GroupBy(u.Age).Having(sorm.CountAll[models.User]().Gte(2)),
		sorm.Field(u.Age),
		sorm.As(sorm.CountAll[models.User](), "n"),
	).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Age != 30 || stats[0].N != 2 {
		t.Fatalf("stats = %+v", stats)
	}

	// LEFT JOIN по связи: авторы с числом постов, включая нулевые.
	type authorPosts struct {
		Name string
		N    int64 `sorm:"n"`
	}
	p := gen.Post
	rows, err := sorm.Project[authorPosts](
		sorm.From[models.User](pool).
			Join(u.Posts.LeftJoin()).
			GroupBy(u.Name).
			OrderBy(u.Name.Asc()),
		sorm.Field(u.Name),
		sorm.As(sorm.Count[models.User](p.ID), "n"),
	).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Name != "Alice" || rows[0].N != 2 || rows[2].N != 0 {
		for _, r := range rows {
			t.Logf("%+v", r)
		}
		t.Fatal("неожиданный результат LEFT JOIN проекции")
	}
}
