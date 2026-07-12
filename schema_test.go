package sorm_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect"
	"github.com/dvislobokov/sorm/dialect/pg"
	"github.com/dvislobokov/sorm/internal/ddl"
	"github.com/dvislobokov/sorm/internal/parse"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
	"github.com/dvislobokov/sorm/migrate"
)

// stdlibPG opens a database/sql handle to the same PG (for migrate.*).
func stdlibPG(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SORM_TEST_DSN")
	if dsn == "" {
		t.Skip("SORM_TEST_DSN not set")
	}
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	return sdb
}

// InSchema: every table sorm renders becomes schema-qualified; the same
// models work in different schemas via different wrappers over one pool.

func TestInSchemaSQL(t *testing.T) {
	db := sorm.InSchema(pgOnlyDB{}, "billing")

	sql, _ := sorm.Query[models.User](db).Where(gen.User.Age.Gt(18)).ToSQL()
	if !strings.Contains(sql, `FROM "billing"."users"`) {
		t.Fatalf("select: %s", sql)
	}

	sql, _, err := sorm.Update[models.User](db).
		Set(gen.User.Name.Set("x")).AllRows().ToSQL()
	if err != nil || !strings.Contains(sql, `UPDATE "billing"."users"`) {
		t.Fatalf("update: %s (err=%v)", sql, err)
	}

	sql, _, err = sorm.Delete[models.User](db).AllRows().ToSQL()
	if err != nil || !strings.Contains(sql, `DELETE FROM "billing"."users"`) {
		t.Fatalf("delete: %s (err=%v)", sql, err)
	}

	// EXISTS via a relation qualifies both tables.
	sql, _ = sorm.Query[models.User](db).
		Where(gen.User.Posts.Any(gen.Post.Views.Gt(0))).ToSQL()
	if !strings.Contains(sql, `FROM "billing"."posts"`) || !strings.Contains(sql, `= "billing"."users".`) {
		t.Fatalf("exists: %s", sql)
	}

	// Projections qualify FROM, JOIN and qualified column references.
	psql, _, err := sorm.Project[struct{ N int64 }](
		sorm.From[models.User](db).GroupBy(gen.User.Age),
		sorm.As(sorm.CountAll[models.User](), "n"),
	).ToSQL()
	if err != nil || !strings.Contains(psql, `FROM "billing"."users"`) {
		t.Fatalf("projection: %s (err=%v)", psql, err)
	}
}

// pgOnlyDB carries the postgres dialect without a connection — enough
// for ToSQL-only tests (InSchema needs a non-nil wrappee).
type pgOnlyDB struct{ sorm.DB }

func (pgOnlyDB) Dialect() dialect.Dialect { return pg.Dialect{} }

func TestInSchemaPG(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// A dedicated schema with the same tables.
	mustExec(t, pool, `DROP SCHEMA IF EXISTS sorm_tenant CASCADE`)
	mustExec(t, pool, `CREATE SCHEMA sorm_tenant`)
	s, err := parse.Load("./internal/testmodels")
	if err != nil {
		t.Fatal(err)
	}
	ddlSQL, err := ddl.Generate(s, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `SET search_path TO sorm_tenant`)
	for _, chunk := range strings.Split(ddlSQL, ";") {
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
	mustExec(t, pool, `SET search_path TO public`)

	db := sorm.InSchema(pool, "sorm_tenant")
	c := gen.NewContext(db)

	// The whole unit of work lands in the tenant schema.
	u := &models.User{Email: "tenant@a.b", Name: "T", Active: true, CreatedAt: nowNoZero()}
	c.Users.Add(u)
	c.Posts.Add(&models.Post{Author: u, Title: "in tenant", Body: ""})
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := c.Users.Where(gen.User.Email.Eq("tenant@a.b")).
		With(gen.User.Posts.Include()).One(ctx)
	if err != nil || len(got.Posts) != 1 {
		t.Fatalf("tenant read: %+v (err=%v)", got, err)
	}

	// public.users must NOT contain the tenant row (schemas are isolated).
	if n, _ := sorm.Query[models.User](pool).Where(gen.User.Email.Eq("tenant@a.b")).Count(ctx); n != 0 {
		t.Fatal("row leaked into public schema")
	}

	// Tracked update + delete inside the schema.
	got.Name = "T2"
	c.Posts.Remove(got.Posts[0])
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := sorm.Query[models.Post](db).Count(ctx); n != 0 {
		t.Fatal("post not deleted in tenant schema")
	}

	// RunInTx keeps the schema (Begin wraps the tx).
	err = c.RunInTx(ctx, func(txc *gen.Context) error {
		txc.Users.Add(&models.User{Email: "tx-tenant@a.b", Name: "X", Active: true, CreatedAt: nowNoZero()})
		return txc.SaveChanges(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := sorm.Query[models.User](db).Where(gen.User.Email.Eq("tx-tenant@a.b")).Count(ctx); n != 1 {
		t.Fatal("tx insert missing in tenant schema")
	}

	// Declarative migrations scoped to the schema: Apply converges the
	// tenant schema (custom Indexes() and the pgmodels tables registered by
	// sormgen inits are missing from the CLI-derived DDL above), after
	// which the plan must be empty.
	sdb := stdlibPG(t)
	if err := migrate.Apply(ctx, sdb, "postgres", migrate.WithSchema("sorm_tenant")); err != nil {
		t.Fatal(err)
	}
	plan, err := migrate.Plan(ctx, sdb, "postgres", migrate.WithSchema("sorm_tenant"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Fatalf("schema-scoped plan after Apply must be empty, got %v", plan)
	}
}
