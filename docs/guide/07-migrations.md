# Migrations

sorm embeds a schema-diff engine (the Atlas SDK as a Go dependency — no
external CLI, no Docker magic). Whenever an operation needs a database,
**you** provide it as a DSN; sorm never spins up infrastructure behind
your back.

Your registered models are always the *desired* state. Importing the
generated `sormgen` package registers every table (including implicit
many-to-many join tables); diffs are scoped to sorm-managed tables, so
foreign tables in the same schema are never touched.

## Declarative: Apply / Plan

The safe replacement for "AutoMigrate" — inspect, diff, reconcile:

```go
import (
    "database/sql"
    _ "github.com/jackc/pgx/v5/stdlib"
    "github.com/dvislobokov/sorm/migrate"
    _ "yourapp/models/sormgen"
)

sdb, _ := sql.Open("pgx", dsn)

stmts, err := migrate.Plan(ctx, sdb, "postgres")  // dry-run: the SQL diff
err  = migrate.Apply(ctx, sdb, "postgres")        // execute it
```

Unlike GORM's AutoMigrate this is a real diff: it adds *and* removes
columns, changes types, manages indexes, detects drift and repairs it.
Ideal for development and tests; production usually wants files.

## Versioned files: Diff / Up / Down / Pending

```go
// Generate a new migration by diffing models against the replayed history.
// dev is an EMPTY scratch database you provide (SQLite: just ":memory:").
fname, err := migrate.Diff(ctx, devDB, "postgres", "migrations", "add users")
// → migrations/20260712093000_add_users.sql
//   migrations/20260712093000_add_users.down.sql   (if reversible)
//   migrations/sorm.sum                            (checksums, updated)

// Apply pending files, in order, tracked in the sorm_migrations table.
applied, err := migrate.Up(ctx, sdb, "postgres", "migrations")

// What would Up run?
pending, err := migrate.Pending(ctx, sdb, "postgres", "migrations")

// Roll back the last N applied migrations using their .down.sql files.
reverted, err := migrate.Down(ctx, sdb, "postgres", "migrations", 1)
```

How `Diff` works: the existing migration directory is **replayed** onto
the scratch database, the result is inspected and diffed against your
models, and the difference becomes the new file. For PostgreSQL/MySQL,
point `devDB` at an empty database you control; for SQLite an in-memory
handle suffices — nothing to set up.

### Safety properties

- **Advisory lock.** `Up`, `Down` and `Apply` serialize through
  `pg_advisory_lock` / `GET_LOCK` — replicas starting simultaneously
  cannot apply the same file twice or interleave DDL.
- **Checksums.** `sorm.sum` records the SHA-256 of every migration file.
  `Up`/`Down`/`Pending` verify the directory first: a file modified after
  the fact, a missing file or a rogue extra file fails with a `*SumError`
  **before any SQL runs**. Directories without `sorm.sum` are accepted
  (hand-written migrations remain usable); the first `Diff` creates it.
- **Transactions.** On PostgreSQL and SQLite each file applies inside a
  transaction. MySQL commits DDL implicitly, so files execute
  statement-by-statement there — keep MySQL migrations small.
- **Honest down files.** If Atlas cannot reverse even one change in a
  migration, no `.down.sql` is written at all — a partial rollback is
  worse than none. `Down` stops with an error at the first migration that
  lacks a down file.

## CLI equivalents

Thin wrappers over the same functions — handy in CI and Makefiles:

```bash
# generate a migration (models parsed statically; sormgen not required)
sorm migrate diff -dialect postgres -dir migrations \
     -dev-dsn 'postgres://.../scratch' add_users ./models

# apply
sorm migrate up -dialect postgres -dir migrations -dsn 'postgres://.../prod'

# render the full schema as SQL (documentation / external diff tools)
sorm schema -dialect postgres ./models > schema.sql
```

For SQLite, `-dev-dsn` defaults to in-memory. Flags come before
positional arguments (standard Go `flag` behavior).

## A production recipe

1. Change the models; run `go generate ./...`.
2. `migrate.Diff` (or the CLI) against a scratch database → a new `.sql`
   file + `.down.sql` + updated `sorm.sum`.
3. The `.sql` file is reviewed in the pull request like any code.
4. Deploy: the application calls `migrate.Up` on startup (safe across
   replicas thanks to the lock), or an operator runs `sorm migrate up`.

The [`examples/webapp`](../../examples/webapp) example implements exactly
this flow, Dockerfile and Compose included.
