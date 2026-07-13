# Schema definition

The schema lives in Go: plain structs with `sorm` tags are the single
source of truth for the generated query layer, the runtime metadata and
the migrations.

## Entities

A struct is an entity when **exactly one** field carries the `pk` tag.
Composite primary keys are not supported on entities (the implicit
many-to-many join tables use them internally).

```go
type User struct {
    ID int64 `sorm:"pk,auto"`
    ...
}
```

## Naming strategies

Derived identifiers default to snake_case: `CreatedAt` → `created_at`,
table `ApiKey` → `api_keys`. The `-naming` flag of `sorm gen` switches the
strategy for the whole schema:

| Strategy | Column (`UserID`) | Table (`ApiKey`) |
|---|---|---|
| `snake` (default) | `user_id` | `api_keys` |
| `camel` | `userId` | `apiKeys` |
| `pascal` | `UserId` | `ApiKeys` |

Explicit `col:` / `table:` overrides always win. Pass the **same**
`-naming` to `sorm schema` and `sorm migrate diff` — the DDL must be
derived with the same rules the queries use. Changing the strategy on an
existing database means a rename migration for every derived identifier.

## Tag reference

Options are comma-separated inside one `sorm:"..."` tag.

| Tag | Meaning |
|---|---|
| `pk` | primary-key column (required, exactly one per entity) |
| `auto` | the database generates the PK (identity / auto-increment). Must be an integer type. Without `auto`, you assign the PK yourself — strings/UUIDs work |
| `unique` | single-column UNIQUE constraint |
| `version` | optimistic-concurrency token; must be a plain `int64`. Initialized to 1 on insert, incremented on every update |
| `autoCreate` | auto-timestamp; plain `time.Time`. Stamped on INSERT when zero — a manually set value wins |
| `autoUpdate` | auto-timestamp; plain `time.Time`. Stamped on INSERT and on every effective UPDATE (a no-op flush does not touch it). All stamps in one `SaveChanges` share a single timestamp. Set-based `sorm.Update` does **not** stamp it — add the assignment explicitly |
| `softDelete` | on a `*time.Time`: queries filter the column `IS NULL` automatically, `Remove`/`Delete` become UPDATEs stamping it. Escapes: `WithDeleted()`, `OnlyDeleted()`, `Delete().Hard()` |
| `col:name` | override the column name (default: `snake_case` of the field name) |
| `type:sqltype` | override the SQL column type, e.g. `type:text`, `type:varchar(36)` |
| `fk:Entity.Field` | declares a foreign key to another entity's field (adds `REFERENCES` in DDL and an edge for write ordering) |
| `index` / `index:name` | secondary index; fields sharing a name form a composite index in declaration order |
| `uniqueIndex` / `uniqueIndex:name` | same, but UNIQUE |
| `table:name` | override the table name (place it on the `pk` field) |
| `-` | ignore the field entirely |
| `hasMany:FKField` | relation: slice of children whose `FKField` points here |
| `belongsTo:FKField` | relation: pointer to the parent referenced by this entity's `FKField` |
| `hasOne:FKField` | relation: pointer to a single child whose `FKField` points here |
| `many2many:join_table` | relation through an implicit join table |

## Column types

| Go type | PostgreSQL | MySQL | SQLite |
|---|---|---|---|
| `bool` | BOOLEAN | BOOLEAN | BOOLEAN |
| `string` | TEXT | VARCHAR(255)¹ | TEXT |
| `int8/int16` | SMALLINT | SMALLINT | INTEGER |
| `int32/int` | INTEGER | INT | INTEGER |
| `int64/uint*` | BIGINT | BIGINT | INTEGER |
| `float32` | REAL | FLOAT | REAL |
| `float64` | DOUBLE PRECISION | DOUBLE | REAL |
| `time.Time` | TIMESTAMPTZ | DATETIME(6) | DATETIME |
| `[]byte` | BYTEA (nullable) | BLOB (nullable) | BLOB (nullable) |
| `uuid.UUID`² | UUID | CHAR(36) | TEXT |
| any type + `sorm:"json"`³ | JSONB | JSON | TEXT |
| Valuer/Scanner type⁴ | from `type:` | from `type:` | from `type:` |
| `[]T` + `sorm:"array"`⁵ | T[] | — | — |

¹ MySQL cannot index unsized TEXT; use `type:text` when you need more than
255 characters and don't index the column.

² `github.com/google/uuid` is supported natively: UUIDs are compared and
mapped **by value** (no strings), work as primary keys (client-assigned —
call `uuid.New()` before `Add`; `auto` does not apply), as foreign keys and
as nullable `*uuid.UUID` columns.

³ Any marshalable Go type (struct, map, slice) tagged `sorm:"json"` is
stored as a JSON document and diffed by content in sessions. Nullability:
a pointer struct, and any map/slice (nil ⇒ SQL NULL), are nullable; a plain
struct is NOT NULL (its zero value marshals to a valid document). Use
`type:json` to get plain `json` instead of `jsonb` on PostgreSQL. Content
queries: see [JSON predicates](03-queries.md#json-predicates).

⁴ A named type implementing `driver.Valuer` **and** `sql.Scanner`
(`decimal.Decimal`, money types, encrypted strings) is a custom scalar.
The SQL type cannot be derived statically, so `type:` is **required**:
`Price decimal.Decimal `sorm:"type:numeric(20,8)"``. The descriptor is
`ScalarCol` — full Eq/Neq/Gt/…/In/Set API; comparisons happen in SQL, so
the Go type does not have to be comparable. Handle NULL inside the type
(e.g. `decimal.NullDecimal`) — pointer scalars are rejected.

⁵ `[]string`, `[]int64/int32/int`, `[]float64`, `[]bool` tagged
`sorm:"array"` map to a **native PostgreSQL array** (`text[]`, `bigint[]`,
…); a nil slice is SQL NULL. Predicates: `Contains(vs...)` (`@>`),
`Overlaps(vs...)` (`&&`), `Has(v)`, `IsNull`. On MySQL/SQLite the DDL
generator and migrations reject the column, and array predicates return a
build error — use `sorm:"json"` for a portable list. Arrays require the
pgx driver (`pgxd`).

**Nullability** is expressed with pointers: `*string`, `*time.Time`,
`*int64`, `*uuid.UUID`, … The generated column descriptor still uses the
base type, so predicates never juggle pointers — `u.Nickname.Eq("gopher")`
plus `u.Nickname.IsNull()`; `SetNull()` writes NULL in set-based updates.
Nullable foreign keys work in relations too: a child with a NULL FK simply
keeps a nil navigation after `Include`.

Named types with a basic underlying type (`type Status string`) are
supported and keep their own type in predicates.

## Custom indexes

Tags cover plain and composite indexes. For anything richer — descending
parts, expressions, full-text, partial indexes — implement the optional
`Indexes()` method on the model:

```go
func (Post) Indexes() []sorm.IndexDef {
    return []sorm.IndexDef{
        // full-text (PostgreSQL GIN over an expression)
        {Name: "idx_posts_fts", Type: "gin",
         Parts: []sorm.IndexPart{{Expr: "to_tsvector('english', title)"}}},
        // partial index with descending order
        {Name: "idx_posts_recent",
         Parts: []sorm.IndexPart{{Column: "created_at", Desc: true}},
         Where: "views > 0"},
    }
}
```

`sorm gen` merges the method's result with tag-derived indexes into the
table definition used by migrations and DDL generation.

Dialect support: `Type` maps to `USING <type>` on PostgreSQL and
`FULLTEXT/SPATIAL INDEX` on MySQL (an error on SQLite); `Where` (partial
indexes) works on PostgreSQL and SQLite (an error on MySQL).

> **CLI caveat:** the static commands (`sorm schema`, `sorm migrate diff`)
> parse your models without executing them, so they cannot see `Indexes()`
> methods and will print a warning. In-code migrations (`sorm/migrate`
> with your `sormgen` package imported) always see the full picture.

## Relations

FK columns are regular fields — relations add *navigation* on top of them:

```go
type User struct {
    ID      int64    `sorm:"pk,auto"`
    Posts   []*Post  `sorm:"hasMany:AuthorID"`    // 1:N — FK on Post
    Profile *Profile `sorm:"hasOne:UserID"`       // 1:1 — FK on Profile
    Tags    []*Tag   `sorm:"many2many:user_tags"` // N:M — implicit join table
}

type Post struct {
    ID       int64 `sorm:"pk,auto"`
    AuthorID int64 `sorm:"fk:User.ID"`
    Author   *User `sorm:"belongsTo:AuthorID"`    // N:1 — FK on this side
}
```

For `many2many`, the join table (`user_tags`) is created implicitly with
two FK columns (`user_id`, `tag_id`) and a composite primary key; it is
registered for migrations automatically. See [Relations](05-relations.md)
for loading and linking.

A useful convention: after eager loading, an empty `hasMany`/`many2many`
collection is an **empty slice**, while `nil` means "was never loaded" —
you can always tell a missed `Include` from genuinely absent data.
(`hasOne`/`belongsTo` pointers cannot make that distinction.)

## Validation

`sorm gen` validates the schema before writing anything: duplicate `pk`,
a non-`int64` `version`, a broken `fk:` reference, a struct-typed field
without a relation tag, an index mixing `index` and `uniqueIndex` under
one name — all fail with a precise error message.
