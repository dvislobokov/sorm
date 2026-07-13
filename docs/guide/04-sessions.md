# Sessions & the Unit of Work

The session is sorm's centerpiece — the thing no other Go ORM has. It
implements the same pattern as EF Core's `DbContext`: an identity map plus
snapshot-based change tracking, with an explicit `SaveChanges`.

## Mental model

```go
s := sorm.NewSession(db)

// 1. Load with tracking. Track is the same builder as Query.
user, err := sorm.Track[models.User](s).
    Where(u.Email.Eq("alice@example.com")).
    With(u.Posts.Include()).       // eagerly loaded children are tracked too
    One(ctx)

// 2. Mutate with plain Go. No setters, no dirty flags, no proxies.
user.Active = false
user.Posts[0].Title = "renamed"

// 3. New and removed entities are explicit.
draft := &models.Post{Author: user, Title: "draft"}
sorm.Add(s, draft)
sorm.Remove(s, user.Posts[1])

// 4. One call persists everything.
err = s.SaveChanges(ctx)
```

Queries **without** `Track` never touch the session — reading is untracked
by default (the equivalent of EF's `AsNoTracking`, but as the default).
Tracking is something you opt into, and pay for, explicitly.

## The generated Context — DbContext, literally

`sorm gen` also emits a `Context` into the sormgen package: a session plus
a typed `sorm.Set` per entity, with set fields named after the tables
(`Users`, `Posts`, `ApiKeys`). It is the recommended facade — the raw
session API above is what it's built on.

```go
c := sormgen.NewContext(db)          // one per request / unit of work

// Reads through a set are TRACKED (identity map, snapshots):
user, err := c.Users.
    Where(u.Email.Eq("alice@example.com")).
    With(u.Posts.Include()).
    One(ctx)

user.Active = false                  // plain mutation — picked up by diff
c.Posts.Add(&models.Post{Author: user, Title: "draft"})
c.Posts.Remove(user.Posts[1])

err = c.SaveChanges(ctx)             // one diff, one transaction
```

What sets add on top of the session:

- **Tracked by default.** `c.Users.Where(...)` == `Track` + query. For
  read-only paths use `c.Users.NoTracking().Where(...)` — no snapshots,
  no identity map.
- **`Find`** — fetch by primary key, checking the identity map first
  (no SQL if the entity is already tracked, EF `Find` semantics):

  ```go
  u, err := c.Users.Find(ctx, 42)    // ErrNotFound if the row is absent
  ```
- **Transactions** — `c.RunInTx` runs the closure over a *fresh child
  context* bound to the transaction; commit on nil, rollback on error,
  automatic retries on transient failures:

  ```go
  err := c.RunInTx(ctx, func(txc *sormgen.Context) error {
      from, err := txc.Accounts.Find(ctx, fromID)
      if err != nil { return err }
      from.Balance -= amount
      if err := txc.SaveChanges(ctx); err != nil { return err }

      txc.Transfers.Add(&models.Transfer{...})
      return txc.SaveChanges(ctx)    // both flushes commit or roll back together
  })
  ```

  Each retry attempt gets a **new** child context (no dirty state from a
  rolled-back attempt); work only through `txc` inside the closure. A
  `SaveChanges` on the child joins the ambient transaction instead of
  opening a nested one.
- **Multiple SaveChanges** on one context are safe: each flush clears the
  pending work it applied; already-inserted entities become tracked and
  subsequent mutations turn into UPDATEs.

Like the session it wraps, a context is **not goroutine-safe** and is
meant to be short-lived; the pool underneath is the long-lived object.

## What SaveChanges actually does

1. **Diff.** Every tracked entity is compared field-by-field against the
   snapshot taken when it was loaded. The diff functions are generated
   code — no reflection; `[]byte` compares with `bytes.Equal`, `time.Time`
   with `.Equal` (no phantom updates from monotonic clocks).
2. **Plan.** Deletes are ordered children-first by the FK graph; inserts
   are topologically sorted **per instance** (self-referencing tables
   work); updates carry only the changed columns.
3. **Execute.** Deletes and updates go out in one batch (a single network
   roundtrip on PostgreSQL via `pgx.Batch`); inserts execute level by
   level so children can receive their parents' generated keys. Inserts of
   the same table within a level are combined into multi-row
   `INSERT ... VALUES` statements (up to 500 rows each).
4. **Fix up.** Generated PKs flow back into the structs (`RETURNING` on
   PostgreSQL, `LastInsertId` arithmetic on MySQL/SQLite), and pending
   children get their FK columns set from the navigation fields.
5. **Commit.** All of it inside one transaction; on any error everything
   rolls back and the session state is untouched.

A `SaveChanges` with nothing to do performs no database work at all.

## Identity map

Within one session, one row = one pointer:

```go
a, _ := sorm.Track[models.User](s).Where(u.ID.Eq(1)).One(ctx)
a.Name = "local edit"
b, _ := sorm.Track[models.User](s).Where(u.Email.Eq("alice@example.com")).One(ctx)
// a == b, and b.Name == "local edit" — the database did NOT overwrite
// your in-memory changes.
```

## Inserting graphs

For new entities, foreign keys are expressed through navigation fields —
never by copying IDs around:

```go
author := &models.User{Email: "bob@example.com", Name: "Bob"}
post   := &models.Post{Author: author, Title: "first"}  // not AuthorID!
sorm.Add(s, author)
sorm.Add(s, post)
err := s.SaveChanges(ctx)
// author inserted first, its generated ID written into post.AuthorID,
// then post inserted — regardless of Add order.
```

Validation happens **before** any SQL: a NOT NULL FK with neither a
navigation nor a value, or a navigation pointing at an entity that was
never `Add`ed and has no key, is a clear error message rather than a
database constraint violation. Cyclic graphs of new entities are rejected
with `sorm.ErrCyclicGraph`.

## Lifecycle hooks

Optional interfaces on model types — detected with a plain interface
assertion, no codegen or registration:

```go
func (m *Message) BeforeSave(ctx context.Context, op sorm.SaveOp) error {
    if op == sorm.SaveInsert && m.Text == "" {
        return errors.New("empty message")  // aborts the flush, nothing hits the DB
    }
    m.Text = strings.TrimSpace(m.Text)      // mutations are persisted
    return nil
}

func (m *Message) AfterLoad(ctx context.Context) error {
    m.Preview = preview(m.Text)             // computed, non-column (sorm:"-") fields
    return nil
}
```

- `BeforeSave(ctx, op)` fires during `SaveChanges` **planning** — before
  any SQL: for every insert and delete, and for updates only when the
  entity actually changed (the diff is re-taken afterwards, so hook
  mutations are written; auto-timestamps stamp after the hook). An error
  aborts the whole flush.
- `AfterLoad(ctx)` fires for every materialized row: `All`/`One`/`Iter`,
  `Raw`/`RawAs` (when the target type implements it).
- Set-based statements (`Update`/`Delete`/`Upsert` builders) **bypass**
  hooks — the same rule as EF Core's `ExecuteUpdate`/`ExecuteDelete`.

## Optimistic concurrency

Declare a version field once:

```go
Version int64 `sorm:"version"`
```

Every tracked UPDATE/DELETE then executes as

```sql
UPDATE users SET name = $1, version = version + 1
WHERE id = $2 AND version = $3
```

and a zero-rows-affected result rolls back the whole SaveChanges and
returns a typed conflict:

```go
err := s.SaveChanges(ctx)
var conflict *sorm.ConflictError
if errors.As(err, &conflict) {
    // conflict.Table, conflict.PK — reload, merge, retry, or give up
}
```

Set-based updates (`sorm.Update`) bump the version too, so bulk writes
and sessions stay consistent with each other.

## Transactions

`SaveChanges` manages its own transaction. To combine several operations —
or several SaveChanges — atomically, use `RunInTx`:

```go
err := sorm.RunInTx(ctx, db, func(tx sorm.Tx) error {
    s := sorm.NewSession(tx)      // a session over an open transaction
    ...
    if err := s.SaveChanges(ctx); err != nil { // flushes into tx, no nesting
        return err
    }
    _, err := sorm.Update[models.Counter](tx).Set(...).Where(...).Exec(ctx)
    return err                    // nil → commit, error → rollback
})
```

`RunInTx` retries up to three times with jittered backoff when the driver
classifies the failure as transient (PostgreSQL serialization failures and
deadlocks, MySQL deadlocks/lock timeouts, SQLite `SQLITE_BUSY`). The
closure may therefore run more than once — keep side effects outside the
database idempotent.

## Semantics worth knowing

- The session is **not goroutine-safe** (same as `DbContext`); use one per
  request / unit of work. Sessions are cheap to create.
- `Modified` state is computed at flush time by diffing — mutations
  themselves cost nothing.
- Removing an entity that was `Add`ed in the same session cancels both.
- Re-pointing a *tracked* child to a *new* parent by mutating the
  navigation field is not detected (snapshots cover columns only); set the
  FK column after saving the parent instead.
- Delete cascades are the database's job (`ON DELETE ...`); sorm orders
  its own deletes but does not invent cascades.
