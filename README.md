# sorm

**The first Go ORM with a real Unit of Work.**

Load a graph of entities, mutate them with plain Go assignments, call
`SaveChanges` — sorm computes the minimal diff, orders the writes by their
foreign-key dependencies and applies everything in a single transaction.
No other Go library does this.

```go
s := sorm.NewSession(db)
user, _ := sorm.Track[models.User](s).
    Where(u.Email.Eq("alice@example.com")).
    With(u.Posts.Include()).
    One(ctx)

user.Active = false          // plain assignments —
user.Posts[0].Title = "new"  // no setters, no dirty flags

err := s.SaveChanges(ctx)    // diff → topo-sort → batch → one transaction
```

## Highlights

| | |
|---|---|
| **Unit of Work** | identity map, reflection-free snapshot diffing (26 ns/entity, 0 allocs), UPDATEs carry only changed columns, graph inserts with FK fixup via `RETURNING` |
| **Optimistic concurrency** | a `sorm:"version"` field makes every UPDATE/DELETE carry a version predicate; conflicts surface as a typed `*ConflictError` |
| **Type-safe queries** | generated column descriptors: `Where(u.Age.Gte(18))` is checked by the compiler; predicates are values you compose in plain `if` statements |
| **Zero-value honesty** | `Where(u.Active.Eq(false))` and `Set(u.Age.Set(0))` are real conditions — the classic GORM footgun is impossible by construction |
| **Relations** | hasMany / belongsTo / hasOne / many2many; eager loading with filters, child ordering and arbitrary nesting; `EXISTS` filters in both directions |
| **Projections** | GroupBy / Having, typed aggregates, relation-based and arbitrary joins, scanning into your own structs |
| **Migrations** | embedded diff engine (Atlas SDK, no external CLI): declarative `Apply`/`Plan` and versioned files with `Diff`/`Up`/`Down`, checksums and an advisory lock against racing replicas |
| **Five databases** | PostgreSQL (pgx, single-roundtrip batches), MySQL, SQLite, MariaDB, CockroachDB — one code path, pluggable adapters |
| **Production plumbing** | `RunInTx` with transient-error retries, typed `ConstraintError`s, `Instrument` middleware, OpenTelemetry tracing (`otelsorm`) |

The escape hatches are first-class: `ToSQL()` shows the exact SQL of any
query, `Raw`/`RawAs` scan raw SQL into your types with strict column checks.

Benchmarks against GORM, Ent and raw `database/sql` live in
[`benchmarks/`](benchmarks/README.md): reads within ~9% of raw and 1.8× faster
than GORM; single-field updates faster than both — with optimistic
concurrency included.

## Quick start

**1. Describe your models** — plain structs, no interfaces to implement:

```go
package models

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

type User struct {
    ID      int64   `sorm:"pk,auto"`
    Email   string  `sorm:"unique"`
    Active  bool
    Version int64   `sorm:"version"`
    Posts   []*Post `sorm:"hasMany:AuthorID"`
}

type Post struct {
    ID       int64  `sorm:"pk,auto"`
    AuthorID int64  `sorm:"fk:User.ID"`
    Author   *User  `sorm:"belongsTo:AuthorID"`
    Title    string
}
```

**2. Generate the typed layer:** `go generate ./...` produces a compact,
review-friendly `sormgen` package.

**3. Connect** through a driver adapter:

```go
pool, _ := pgxpool.New(ctx, dsn)
db := pgxd.Wrap(pool)                      // PostgreSQL
// db := sqld.Wrap(sdb, lite.Dialect{})    // SQLite
// db := sqld.Wrap(sdb, my.Dialect{})      // MySQL
```

**4. Create the schema** — from code or with versioned files:

```go
migrate.Apply(ctx, sdb, "postgres")        // diff models vs database, apply
```

```bash
sorm migrate diff -dev-dsn <empty scratch db> add_users ./models
sorm migrate up -dsn <production dsn>
```

**5. Query and mutate** — see the session example above; without tracking:

```go
users, _ := sorm.Query[models.User](db).
    Where(u.Active.Eq(true), u.Posts.Any(p.Title.HasPrefix("Go"))).
    OrderBy(u.Email.Asc()).Limit(20).
    All(ctx)
```

## Documentation

The full guide lives in [`docs/guide`](docs/guide):

1. [Getting started](docs/guide/01-getting-started.md)
2. [Schema definition](docs/guide/02-schema.md) — tags, types, indexes
3. [Queries](docs/guide/03-queries.md) — predicates, streaming, raw SQL
4. [Sessions & Unit of Work](docs/guide/04-sessions.md) — tracking, SaveChanges, transactions
5. [Relations](docs/guide/05-relations.md) — eager loading, nesting, many-to-many
6. [Projections](docs/guide/06-projections.md) — aggregates, GROUP BY, joins
7. [Migrations](docs/guide/07-migrations.md) — declarative & versioned
8. [Observability & errors](docs/guide/08-observability.md) — logging, tracing, typed errors
9. [Multiple databases](docs/guide/09-multi-database.md) — adapters and dialects

Plus the [API reference](docs/api.md).

Design history (in Russian): [concept](docs/concept.md) ·
[detailed design](docs/design.md) ·
[competitive analysis](docs/competitive-analysis.md) ·
[EF Core gap analysis](docs/efcore-gap-analysis.md).

## Examples

- [`examples/chat`](examples/chat) — the showcase application: a chat service on Echo with clean layering (transport / service / repository), a dedicated "chat" DB schema, JSON documents with typed accessors, PG arrays, custom scalars, audit via RunInTx, migrations + seed on startup, srog logging and sconf configuration.

## Philosophy

sorm does not hide SQL — it removes the manual bookkeeping of change
tracking. Its rules were distilled from other libraries' failure modes:
no silently dropped conditions, errors are return values only, `ctx` is
mandatory, builders are immutable, `UPDATE` without `WHERE` refuses to run
unless you say `AllRows()`, and every query can be inspected with `ToSQL()`.

## Status

Under active development; the API may change before v1. Every commit runs
the full test suite against PostgreSQL 17, MySQL 8, MariaDB 10.11, CockroachDB 24 and SQLite (including
the race detector) in CI.
