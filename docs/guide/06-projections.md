# Projections: aggregates, GROUP BY, joins

Entity queries return entities. As soon as you group or aggregate, the
result has a different shape — Go has no anonymous types, so a projection
scans into a **named struct of your own**:

```go
type ageStat struct {
    Age int
    N   int64 `sorm:"n"`
}

stats, err := sorm.Project[ageStat](
    sorm.From[models.User](db).
        Where(u.Active.Eq(true)).
        GroupBy(u.Age).
        Having(sorm.CountAll[models.User]().Gt(10)).
        OrderBy(u.Age.Asc()),
    sorm.Field(u.Age),                       // SELECT "users"."age" AS "age"
    sorm.As(sorm.CountAll[models.User](), "n"), // count(*) AS "n"
).All(ctx)
```

Field mapping follows the same rules as `RawAs`: `sorm:"name"` tag or the
snake_case field name, strict in both directions — a mismatch is a
`*ScanError` before any SQL runs.

## Aggregates

Free functions; the root entity is explicit, the value type is inferred
from the column and enforced in `Having` comparisons:

```go
sorm.CountAll[models.User]()          // count(*)        → int64
sorm.Count[models.User](p.ID)         // count(col)      → int64 (any table's column)
sorm.Sum[models.User](p.Views)        // sum(col)        → column's type
sorm.Avg[models.User](u.Age)          // avg(col)        → float64
sorm.Min[models.User](u.CreatedAt)    // min/max         → column's type
sorm.Max[models.User](p.Views)
```

An aggregate predicate placed in `Where` instead of `Having` is a build
error with a message telling you so.

`sorm.CountDistinct[E](col)` — `count(DISTINCT col)` — is portable across
all three dialects.

## Dialect-specific aggregates

PostgreSQL and MySQL have rich aggregate vocabularies far beyond the
portable five. They live in companion packages so the core stays
dialect-neutral: **`pgagg`** (PostgreSQL) and **`myagg`** (MySQL).
Using one on the wrong dialect is a build error returned when the query
executes — never silently wrong SQL.

```go
import (
    "github.com/dvislobokov/sorm/pgagg"
    "github.com/dvislobokov/sorm/myagg"
)

// PostgreSQL
rows, err := sorm.Project[stat](
    sorm.From[models.User](db).GroupBy(u.Age),
    sorm.Field(u.Age),
    sorm.As(pgagg.StringAgg[models.User](u.Name, ", "), "names"),      // string_agg(name::text, $1)
    sorm.As(pgagg.BoolAnd[models.User](u.Active), "all_active"),       // bool_and(active)
    sorm.As(pgagg.PercentileCont[models.User](0.5, u.Age), "median"),  // percentile_cont(0.5) WITHIN GROUP (ORDER BY age)
).All(ctx)

// MySQL
rows, err := sorm.Project[stat](
    sorm.From[models.User](db).GroupBy(u.Age),
    sorm.Field(u.Age),
    sorm.As(myagg.GroupConcatSep[models.User](u.Name, ", "), "names"), // GROUP_CONCAT(name SEPARATOR ', ')
    sorm.As(myagg.JSONArrayAgg[models.User](u.Age), "ages"),           // JSON_ARRAYAGG(age)
).All(ctx)
```

| pgagg | myagg |
|---|---|
| `StringAgg(col, sep)` | `GroupConcat(col)` / `GroupConcatSep(col, sep)` / `GroupConcatDistinct(col, sep)` |
| `ArrayAgg`, `JSONBAgg`, `JSONBObjectAgg(k, v)` | `JSONArrayAgg`, `JSONObjectAgg(k, v)` |
| `BoolAnd`, `BoolOr` | `AnyValue(col)` |
| `BitAnd`, `BitOr` | `BitAnd`, `BitOr`, `BitXor` |
| `StdDev`, `StdDevPop`, `StdDevSamp`, `Variance`, `VarPop`, `VarSamp` | `StdDev`, `StdDevPop`, `StdDevSamp`, `VarPop`, `VarSamp` |
| `Corr(y, x)`, `CovarPop(y, x)`, `CovarSamp(y, x)` | |
| `PercentileCont(f, col)`, `PercentileDisc(f, col)`, `Mode(col)` | |

All of them return `AggExpr[E, V]`, so `Having` comparisons work the
same as with the portable aggregates.

### Building your own

Both packages are built on a small public extension API in the core —
you can define aggregates (or any SQL function expression) the same way:

```go
// string_agg(name, ', ') — how pgagg.StringAgg is built:
func StringAgg[E any](c sorm.AnyCol, sep string) sorm.AggExpr[E, string] {
    return sorm.NewAgg[E, string](
        sorm.AggDialect("postgres"),                       // guard: build error elsewhere
        sorm.AggRaw("string_agg("), sorm.AggCol(c),        // raw SQL + qualified column
        sorm.AggRaw("::text, "), sorm.AggArg(sep),         // bind parameter
        sorm.AggRaw(")"),
    )
}
```

Parts: `AggRaw` (verbatim SQL), `AggCol` (qualified column reference),
`AggArg` (bind parameter), `AggLit` (quote-escaped string literal, for
spots where the grammar forbids placeholders — e.g. MySQL's
`GROUP_CONCAT ... SEPARATOR`), `AggDialect` (guard). Omit `AggDialect`
for portable expressions.

## Joins

Two flavors:

```go
// 1. Along a declared relation — fully typed, ON derived from the FK:
sorm.From[models.User](db).Join(u.Posts.LeftJoin())
sorm.From[models.User](db).Join(u.Posts.InnerJoin(p.Published.Eq(true))) // extra ON preds

// 2. Arbitrary — the ON condition is value-typed (int column can't meet
//    a string column), table membership is validated at build time:
sorm.From[models.User](db).Join(
    sorm.InnerJoinOn(sorm.ColEq(o.UserID, u.ID)),  // joined col, existing col
)
sorm.From[models.User](db).Join(sorm.CrossJoin[models.Region, models.User]())
```

Columns of *joined* entities go into the select list via the relaxed
helpers (root-entity columns keep full inference):

```go
sorm.Field(u.Email)                       // root column — E inferred
sorm.FieldOf[models.User](p.Title)        // joined entity's column — E explicit
sorm.FieldOfAs[models.User](p.Title, "t") // with an alias
```

Projection SQL is always table-qualified (`"users"."id"`) — after a join,
bare column names would be ambiguous. Entity queries stay unqualified.

## A complete example

```go
type authorStat struct {
    Name     string
    N        int64
    MaxViews int64 `sorm:"max_views"`
}

stats, err := sorm.Project[authorStat](
    sorm.From[models.User](db).
        Join(u.Posts.LeftJoin()).
        GroupBy(u.Name).
        Having(sorm.CountAll[models.User]().Gte(1)).
        OrderBy(u.Name.Asc()),
    sorm.Field(u.Name),
    sorm.As(sorm.CountAll[models.User](), "n"),
    sorm.As(sorm.Max[models.User](p.Views), "max_views"),
).All(ctx)
```

```sql
SELECT "users"."name" AS "name", count(*) AS "n", max("posts"."views") AS "max_views"
FROM "users" LEFT JOIN "posts" ON "posts"."author_id" = "users"."id"
GROUP BY "users"."name" HAVING count(*) >= $1 ORDER BY "users"."name"
```

## Boundaries

The projection layer deliberately does not chase 100% of SQL — that road
leads to a DSL more verbose than SQL itself. UNION, CTEs, window
functions, LATERAL: use [`RawAs`](03-queries.md#raw-sql), which shares the
same strict struct scanning. `ToSQL()` works on projections too.
