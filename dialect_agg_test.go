package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
	"github.com/dvislobokov/sorm/myagg"
	"github.com/dvislobokov/sorm/pgagg"
)

type namesRow struct {
	Age   int
	Names string
}

func TestPGAggSQL(t *testing.T) {
	// nil db renders with the postgres dialect.
	q := sorm.Project[namesRow](
		sorm.From[models.User](nil).GroupBy(u.Age),
		sorm.Field(u.Age),
		sorm.As(pgagg.StringAgg[models.User](u.Name, ", "), "names"),
	)
	sql, args, err := q.ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `string_agg("users"."name"::text, $1) AS "names"`
	if !strings.Contains(sql, want) {
		t.Fatalf("sql:\n%s\nmissing:\n%s", sql, want)
	}
	if len(args) != 1 || args[0] != ", " {
		t.Fatalf("args: %v", args)
	}

	// percentile_cont — WITHIN GROUP syntax.
	q2 := sorm.Project[struct{ Median float64 }](
		sorm.From[models.User](nil),
		sorm.As(pgagg.PercentileCont[models.User](0.5, u.Age), "median"),
	)
	sql2, _, err := q2.ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql2, `percentile_cont($1) WITHIN GROUP (ORDER BY "users"."age")`) {
		t.Fatalf("sql2: %s", sql2)
	}
}

func TestDialectGuard(t *testing.T) {
	// A PG-only aggregate on SQLite fails at build time with a clear error.
	db := sqliteDB(t)
	_, err := sorm.Project[namesRow](
		sorm.From[models.User](db).GroupBy(u.Age),
		sorm.Field(u.Age),
		sorm.As(pgagg.StringAgg[models.User](u.Name, ","), "names"),
	).All(context.Background())
	if err == nil || !strings.Contains(err.Error(), "only supported on postgres") {
		t.Fatalf("expected dialect guard error, got %v", err)
	}

	// And a MySQL-only aggregate guarded the same way.
	_, err = sorm.Project[namesRow](
		sorm.From[models.User](db).GroupBy(u.Age),
		sorm.Field(u.Age),
		sorm.As(myagg.GroupConcat[models.User](u.Name), "names"),
	).All(context.Background())
	if err == nil || !strings.Contains(err.Error(), "only supported on mysql") {
		t.Fatalf("expected dialect guard error, got %v", err)
	}
}

func TestCountDistinctAllDialects(t *testing.T) {
	for _, tc := range []struct {
		name string
		db   sorm.DB
	}{
		{"sqlite", sqliteDB(t)}, {"postgres", testPool(t)}, {"mysql", mysqlDB(t)},
	} {
		db := tc.db
		ctx := context.Background()
		s := sorm.NewSession(db)
		sorm.Add(s,
			&models.User{Email: "d1@b.c", Name: "Dup", Active: true, Age: 30, CreatedAt: nowNoZero()},
			&models.User{Email: "d2@b.c", Name: "Dup", Active: true, Age: 30, CreatedAt: nowNoZero()},
			&models.User{Email: "d3@b.c", Name: "Uniq", Active: true, Age: 30, CreatedAt: nowNoZero()},
		)
		if err := s.SaveChanges(ctx); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		rows, err := sorm.Project[struct{ N int64 }](
			sorm.From[models.User](db),
			sorm.As(sorm.CountDistinct[models.User](gen.User.Name), "n"),
		).All(ctx)
		if err != nil || len(rows) != 1 || rows[0].N != 2 {
			t.Fatalf("%s: count distinct = %v (err=%v)", tc.name, rows, err)
		}
	}
}

func TestPGAggIntegration(t *testing.T) {
	db := testPool(t)
	ctx := context.Background()
	seedAggUsers(t, db)

	type agg struct {
		Names  string
		AllAct bool    `sorm:"all_act"`
		Median float64
	}
	rows, err := sorm.Project[agg](
		sorm.From[models.User](db),
		sorm.As(pgagg.StringAgg[models.User](gen.User.Name, "|"), "names"),
		sorm.As(pgagg.BoolAnd[models.User](gen.User.Active), "all_act"),
		sorm.As(pgagg.PercentileCont[models.User](0.5, gen.User.Age), "median"),
	).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	if !strings.Contains(r.Names, "|") || r.AllAct || r.Median != 30 {
		t.Fatalf("pg agg: %+v", r)
	}
}

func TestMySQLAggIntegration(t *testing.T) {
	db := mysqlDB(t)
	ctx := context.Background()
	seedAggUsers(t, db)

	type agg struct {
		Names string
		JArr  string `sorm:"jarr"`
	}
	rows, err := sorm.Project[agg](
		sorm.From[models.User](db),
		sorm.As(myagg.GroupConcatSep[models.User](gen.User.Name, "|"), "names"),
		sorm.As(myagg.JSONArrayAgg[models.User](gen.User.Age), "jarr"),
	).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	if !strings.Contains(r.Names, "|") || !strings.Contains(r.JArr, "20") {
		t.Fatalf("mysql agg: %+v", r)
	}
}

func seedAggUsers(t *testing.T, db sorm.DB) {
	t.Helper()
	s := sorm.NewSession(db)
	sorm.Add(s,
		&models.User{Email: "x@b.c", Name: "Ann", Active: true, Age: 20, CreatedAt: nowNoZero()},
		&models.User{Email: "y@b.c", Name: "Ben", Active: true, Age: 30, CreatedAt: nowNoZero()},
		&models.User{Email: "z@b.c", Name: "Cid", Active: false, Age: 40, CreatedAt: nowNoZero()},
	)
	if err := s.SaveChanges(context.Background()); err != nil {
		t.Fatal(err)
	}
}
