# Relations

Four relation kinds, one loading model. Eager loading uses *split queries*
(one `WHERE fk IN (...)` query per relation, chunked at 1000 keys) — no
row multiplication, no broken pagination, results distributed in memory.

Declared in the [schema](02-schema.md):

```go
type User struct {
    ID      int64    `sorm:"pk,auto"`
    Posts   []*Post  `sorm:"hasMany:AuthorID"`
    Profile *Profile `sorm:"hasOne:UserID"`
    Tags    []*Tag   `sorm:"many2many:user_tags"`
}
type Post struct {
    AuthorID int64 `sorm:"fk:User.ID"`
    Author   *User `sorm:"belongsTo:AuthorID"`
    ...
}
```

## Eager loading: Include + With

```go
users, err := sorm.Query[models.User](db).
    With(u.Posts.Include()).
    With(u.Profile.Include()).
    All(ctx)
```

`Include` accepts options — all three kinds through one variadic:

```go
users, err := sorm.Query[models.User](db).
    With(u.Posts.Include(
        p.Published.Eq(true),          // Pred[Post]   — filter the children
        p.CreatedAt.Desc(),            // Order[Post]  — order the children
        p.Author.Include(              // IncludeSpec[Post] — nested loading
            u.Profile.Include(),       //   ...to any depth
        ),
    )).
    All(ctx)
```

After loading, an empty `hasMany`/`many2many` collection is an **empty
slice**; `nil` means the relation was never included. Parents shared by
several children resolve to the **same pointer**.

Inside a `Track` query, eagerly loaded entities join the session too — you
can mutate a grandchild and `SaveChanges` will see it.

## Filtering by relations (EXISTS)

These return ordinary predicates, so they compose with everything else:

```go
// parents by children
sorm.Query[models.User](db).Where(u.Posts.Any(p.Views.Gte(1000)))   // EXISTS
sorm.Query[models.User](db).Where(u.Posts.None())                   // NOT EXISTS
sorm.Query[models.User](db).Where(u.Profile.Any())                  // hasOne too

// children by parent
sorm.Query[models.Post](db).Where(p.Author.Is(u.Active.Eq(true)))

// by many-to-many
sorm.Query[models.User](db).Where(u.Tags.Any(t.Label.Eq("go")))
```

All compile to correlated `EXISTS` subqueries — the parent set is never
duplicated by a join, and pagination stays correct.

## Many-to-many

The join table is implicit (declared as `many2many:user_tags`), created by
migrations with a composite primary key and foreign keys to both sides.

Reading works like any relation (`Include`, `Any`). Writing is **explicit**
— linking is an operation, not a collection mutation to be guessed at:

```go
err := u.Tags.Link(ctx, db, alice, goTag, dbTag) // INSERT into user_tags
err  = u.Tags.Unlink(ctx, db, alice, dbTag)      // DELETE from user_tags
```

Linking the same pair twice violates the join table's composite PK and
comes back as a typed unique violation (`sorm.IsUniqueViolation(err)`).

## Joins for projections

Relations also power typed joins in the projection layer
([Projections](06-projections.md)):

```go
rows, err := sorm.Project[stat](
    sorm.From[models.User](db).
        Join(u.Posts.LeftJoin()).      // ON posts.author_id = users.id
        GroupBy(u.Name),
    sorm.Field(u.Name),
    sorm.As(sorm.Count[models.User](p.ID), "n"),
).All(ctx)
```

## Cheat sheet

| Kind | Declared on | Load | Filter | Write |
|---|---|---|---|---|
| `hasMany` | parent, `[]*C` | `Include` | `Any` / `None` | via child FK / navigation |
| `belongsTo` | child, `*P` | `Include` | `Is` | set the navigation on insert |
| `hasOne` | parent, `*C` | `Include` | `Any` / `None` | via child FK |
| `many2many` | either side, `[]*C` | `Include` | `Any` | `Link` / `Unlink` |
