# Queries

All examples assume descriptor shorthands:

```go
var u, p = gen.User, gen.Post
```

## The query builder

```go
users, err := sorm.Query[models.User](db).
    Where(u.Active.Eq(true)).
    Where(u.Age.Gte(18)).            // multiple Where calls are ANDed
    OrderBy(u.Name.Asc(), u.ID.Desc()).
    Limit(50).Offset(100).
    All(ctx)
```

Ground rules, all enforced by the API:

- **Builders are immutable.** Every method returns a copy; deriving two
  queries from one base never leaks conditions between them.
- **`ctx` is mandatory** on every executing method.
- **`One` returns `sorm.ErrNotFound`** (check with `errors.Is`) when there
  is no row. `All` returns an empty slice and a nil error.
- **`ToSQL()`** returns the exact SQL and arguments of any query — the
  answer to "what will this actually run?".

```go
sqlStr, args := q.ToSQL()
// SELECT "id", "email", ... FROM "users" WHERE ("active" = $1 AND "age" >= $2) ...
```

## Predicates

Predicates are **values**. They are created by generated column
descriptors, checked by the compiler (a `Pred[Post]` does not fit a
`User` query), and composed with plain Go:

| Descriptor | Available predicates |
|---|---|
| every column | `Eq`, `Neq`, `In`, `NotIn`, `IsNull`, `IsNotNull`, `Set` (for updates), `Asc`, `Desc` |
| ordered (numbers, time, strings) | `Gt`, `Gte`, `Lt`, `Lte`, `Between` |
| strings | `Like`, `ILike`¹, `HasPrefix`, `HasSuffix`, `Contains` (literals are LIKE-escaped) |
| `[]byte` | `Eq`, `Neq`, `IsNull`, `IsNotNull` |

¹ `ILike` is PostgreSQL-only; on MySQL/SQLite `Like` is already
case-insensitive for ASCII by default collation.

Combinators: `sorm.And(...)`, `sorm.Or(...)`, `sorm.Not(...)`.

**Zero values are real conditions.** `u.Active.Eq(false)` and
`u.Age.Eq(0)` filter exactly what they say — there is no struct-based
condition API that could silently drop them.

**Dynamic composition** — the pattern SQL-first tools struggle with — is
just code:

```go
q := sorm.Query[models.Task](db)
if f.Done != nil {
    q = q.Where(t.Done.Eq(*f.Done))
}
if f.MinPriority > 0 {
    q = q.Where(t.Priority.Gte(f.MinPriority))
}
tasks, err := q.OrderBy(t.Priority.Desc()).All(ctx)
```

Edge cases are defined, not surprising: an empty `In()` renders as `FALSE`
(matches nothing), an empty `NotIn()` as `TRUE`.

Relation-based filters (`u.Posts.Any(...)`, `p.Author.Is(...)`) are
covered in [Relations](05-relations.md).

## Streaming large result sets

`All` materializes everything. For large scans use the iterator — rows are
yielded as they arrive from the driver:

```go
for user, err := range sorm.Query[models.User](db).Iter(ctx) {
    if err != nil {
        return err
    }
    process(user)
}
```

`Iter` is incompatible with `With` (eager loading needs the full parent
set) and reports that as an error through the iterator.

## Set-based updates and deletes

For bulk writes that don't need entity tracking (the analogue of EF Core's
`ExecuteUpdate` / `ExecuteDelete`):

```go
n, err := sorm.Update[models.User](db).
    Set(u.Active.Set(false), u.Name.Set("archived")).
    Where(u.LastSeen.Lt(cutoff)).
    Exec(ctx)

n, err = sorm.Delete[models.Session](db).
    Where(s.ExpiresAt.Lt(time.Now())).
    Exec(ctx)
```

Safety rails:

- A statement **without `Where` refuses to run**; whole-table writes
  require an explicit `.AllRows()`.
- On versioned entities, set-based updates automatically bump the
  `version` column, so open sessions still detect the conflict.

## Raw SQL

When you need SQL that the builder does not speak, drop down without
losing type-safe scanning:

```go
// Into entities — strict column matching against the entity's columns:
users, err := sorm.Raw[models.User](db,
    `SELECT * FROM users WHERE ...`, arg1).All(ctx)

// Into any struct — for aggregates, CTEs, window functions:
type authorStat struct {
    Name     string
    N        int64 `sorm:"n"`
    MaxViews int64 `sorm:"max_views"`
}
stats, err := sorm.RawAs[authorStat](db, `
    SELECT a.name, count(*) AS n, max(p.views) AS max_views
    FROM users a JOIN posts p ON p.author_id = a.id
    GROUP BY a.name`).All(ctx)
```

Column matching is strict in both directions: an unexpected or missing
column yields a `*sorm.ScanError` listing both sides — never a silently
half-filled struct. Struct fields map by `sorm:"name"` tag or the
snake_case of the field name.
