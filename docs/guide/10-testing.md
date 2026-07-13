# Testing projects built on sorm

The `sormtest` package supports a four-level pyramid. The one thing NOT
on the list: **mocking `sorm.DB`**. A mock proves your code called the
mock — it verifies neither the SQL nor the schema nor the predicates,
which is where data bugs actually live.

## 1. Query construction — no database

Builders expose `ToSQL()`; assert the exact SQL and arguments:

```go
sormtest.AssertSQL(t,
    sorm.Query[models.Post](nil).Where(p.Views.Gt(10)).Limit(5),
    `SELECT ... FROM "posts" WHERE "views" > $1 LIMIT 5`,
    10)
```

Works with every builder (Query/Update/Delete/Upsert/Project).
Nanoseconds; ideal for locking down predicate logic.

## 2. Data-access code — in-memory SQLite

```go
func TestMessageRepo_Page(t *testing.T) {
    t.Parallel()
    db := sormtest.NewSQLite(t)        // real schema of your registered models
    sormtest.Load(t, db, "testdata/rooms.yaml")

    repo := repository.NewMessages(db)
    msgs, err := repo.Page(ctx, 1, time.Now(), 10)
    ...
}
```

`NewSQLite` opens `:memory:`, applies the schema via `migrate.Apply`
(import your sormgen package for its registration side effect) and
cleans up with the test. Millisecond-fast, one private database per
call — `t.Parallel()` is free. This level runs your **real** SQL:
soft-delete filters, includes, diffs, hooks — everything.

PostgreSQL-only features fail here with explicit build errors ("only
supported on postgres") — that is the signal to move the test one level
up, not a silent pass.

## 3. Dialect-specific code — isolated schemas in one PostgreSQL

```go
func TestArrayPredicates(t *testing.T) {
    t.Parallel()
    db := sormtest.NewPostgres(t)      // skips when SORM_TEST_DSN is unset
    ...
}
```

`NewPostgres` creates a `sormtest_<random>` schema in the server pointed
to by `SORM_TEST_DSN`, applies migrations into it and returns a
connection bound via `sorm.InSchema`. Tests share one server yet cannot
see each other; the schema is dropped afterwards. No per-test containers.

## 4. Business logic — no sorm at all

Depend on your own repository interfaces (see `examples/chat`) and mock
those. The repository implementations are covered by levels 2–3.

## Fixtures

YAML files, top-level keys are table names, rows are column maps:

```yaml
users:
  - {id: 1, email: alice@a.b, name: Alice, active: true,
     created_at: 2026-01-01T00:00:00Z, version: 1}
posts:
  - {id: 1, author_id: 1, title: hi, body: "", views: 0,
     created_at: 2026-01-01T00:00:00Z, updated_at: 2026-01-01T00:00:00Z}
```

Tables are inserted **parents-first** regardless of file order (the FK
graph comes from the registered TableDefs). Rows are raw facts: every
NOT NULL column must be present — fixtures bypass auto-timestamps and
hooks by design. Maps/slices marshal to JSON for json columns.

## Query budgets / catching N+1

```go
db, queries := sormtest.CountQueries(sormtest.NewSQLite(t))
page, _ := repo.ListWithAuthors(ctx, db)
if queries.Selects() > 2 {
    t.Fatalf("N+1: %d selects for one page", queries.Selects())
}
```

`Counter` tallies selects and writes (batch items counted individually)
through the regular `Instrument` mechanism.
