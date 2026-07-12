# Getting started

This chapter takes you from an empty module to a working program that
creates its schema, writes a graph of entities and queries it back.

## Installation

```bash
go get github.com/dvislobokov/sorm
```

sorm needs Go 1.25+. The core has a single heavy dependency per feature
area, and each is isolated in its own package so you only pay for what you
import:

| Import | Pulls in |
|---|---|
| `sorm`, `sorm/dialect/...` | nothing beyond the standard library |
| `sorm/driver/pgxd` | `github.com/jackc/pgx/v5` |
| `sorm/driver/sqld` | `database/sql` only (bring your own driver) |
| `sorm/migrate` | `ariga.io/atlas` (the diff engine, as a Go library) |
| `sorm/otelsorm` | `go.opentelemetry.io/otel` |

## 1. Define models

Models are plain structs in a package of your choice. An entity is any
struct with a `sorm:"pk"` field. Navigation fields (relations) are declared
with tags and are **not** columns.

```go
// models/models.go
package models

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

import "time"

type User struct {
    ID        int64     `sorm:"pk,auto"`
    Email     string    `sorm:"unique"`
    Name      string
    Active    bool
    CreatedAt time.Time
    Version   int64     `sorm:"version"`
    Posts     []*Post   `sorm:"hasMany:AuthorID"`
}

type Post struct {
    ID       int64  `sorm:"pk,auto"`
    AuthorID int64  `sorm:"fk:User.ID"`
    Author   *User  `sorm:"belongsTo:AuthorID"`
    Title    string
}
```

There is nothing to embed and no interface to implement — these structs can
live in your domain layer.

## 2. Generate the typed layer

```bash
go generate ./...
# or explicitly:
go run github.com/dvislobokov/sorm/cmd/sorm gen ./models
```

This creates `models/sormgen`: typed column descriptors (`User.Email`,
`Post.Title`), relation descriptors, and per-entity metadata (scanners,
snapshots, diff functions). The generated code is compact (~150–220 lines
per entity), deterministic, and meant to be committed — it reads well in
code review. Everything "smart" lives in the generic runtime; the generated
code is flat data and field access, with **zero reflection at runtime**.

## 3. Connect

sorm talks to databases through adapters implementing `sorm.DB`:

```go
import (
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/dvislobokov/sorm/driver/pgxd"
)

pool, err := pgxpool.New(ctx, "postgres://user:pass@localhost:5432/app")
db := pgxd.Wrap(pool)
```

For MySQL or SQLite use the `database/sql` adapter with the matching
dialect — see [Multiple databases](09-multi-database.md).

## 4. Create the schema

The fastest way during development is a declarative migration from code
(the schema is diffed against your registered models and reconciled):

```go
import (
    "database/sql"
    _ "github.com/jackc/pgx/v5/stdlib" // registers driver "pgx"
    "github.com/dvislobokov/sorm/migrate"
    _ "yourapp/models/sormgen"          // registers table definitions
)

sdb, _ := sql.Open("pgx", dsn)
if err := migrate.Apply(ctx, sdb, "postgres"); err != nil { ... }
```

For production you will want reviewed, versioned migration files — see
[Migrations](07-migrations.md).

## 5. Write and read

```go
import (
    "github.com/dvislobokov/sorm"
    "yourapp/models"
    gen "yourapp/models/sormgen"
)

var u, p = gen.User, gen.Post

// Insert a graph: FKs are set through navigation fields; sorm orders the
// inserts, retrieves the generated ids and fixes up the foreign keys.
s := sorm.NewSession(db)
alice := &models.User{Email: "alice@example.com", Name: "Alice", Active: true}
sorm.Add(s, alice)
sorm.Add(s, &models.Post{Author: alice, Title: "Hello sorm"})
if err := s.SaveChanges(ctx); err != nil { ... }

// Query without tracking (the default — cheap, read-only).
users, err := sorm.Query[models.User](db).
    Where(u.Active.Eq(true)).
    With(u.Posts.Include()).
    OrderBy(u.Name.Asc()).
    All(ctx)

// Update through the Unit of Work.
s2 := sorm.NewSession(db)
loaded, err := sorm.Track[models.User](s2).Where(u.ID.Eq(alice.ID)).One(ctx)
loaded.Name = "Alice Cooper"
err = s2.SaveChanges(ctx) // UPDATE users SET name = $1, version = version + 1 WHERE ...
```

## Where to go next

- How tags map to columns, and how to declare indexes → [Schema](02-schema.md)
- The full query surface, streaming, raw SQL → [Queries](03-queries.md)
- What exactly `SaveChanges` does → [Sessions](04-sessions.md)
- A complete runnable service → [`examples/chat`](../../examples/chat)
