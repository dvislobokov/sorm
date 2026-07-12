# Observability & error handling

## Instrument: the middleware hook

`sorm.Instrument` wraps any `sorm.DB` with a function that sees every
operation — queries, execs, write batches, and transaction begin/commit/
rollback. It is the single extension point for logging, metrics and
tracing:

```go
db = sorm.Instrument(db, func(ctx context.Context, op sorm.Op, next func(context.Context) error) error {
    start := time.Now()
    err := next(ctx)
    slog.InfoContext(ctx, "sql",
        "kind", op.Kind,          // query | exec | batch | begin | commit | rollback
        "sql", op.SQL,            // first statement for batches
        "batch", len(op.Statements),
        "dur", time.Since(start),
        "err", err,
    )
    return err
})
```

`Op.Args` carries the query arguments — log them only where that is
acceptable. Wrappers compose: transactions opened through an instrumented
`DB` are instrumented too.

## OpenTelemetry

`sorm/otelsorm` builds tracing on top of `Instrument` (the core stays
otel-free — the dependency links only if you import the package):

```go
import "sorm/otelsorm"

db = otelsorm.Wrap(db)                              // global TracerProvider
db = otelsorm.Wrap(db,
    otelsorm.WithTracerProvider(tp),                // explicit provider
    otelsorm.WithArgs(),                            // record args (off by default)
)
```

Every operation becomes a `CLIENT` span (`sorm.query`, `sorm.batch`, …)
with `db.system`, `db.statement` and — for batches —
`db.operation.batch.size` attributes; failures set the span status and
record the error. Argument recording is opt-in because arguments routinely
contain personal data.

## Typed errors

Handlers should branch on what happened, not parse driver strings. sorm
translates at the adapter layer:

| Error | When | How to check |
|---|---|---|
| `sorm.ErrNotFound` | `One()` with no row | `errors.Is` |
| `*sorm.ConflictError` | optimistic-concurrency conflict: the row changed or vanished since it was loaded | `errors.As`; fields `Table`, `PK` |
| `*sorm.ConstraintError` | database constraint violation, `Kind` ∈ unique / foreign key / not null / check | `errors.As`, or `sorm.IsUniqueViolation(err)` |
| `*sorm.ScanError` | column/field mismatch in `Raw`, `RawAs`, `Project` | `errors.As`; fields `Missing`, `Extra` |
| `sorm.ErrCyclicGraph` | cyclic dependencies between new entities in one `SaveChanges` | `errors.Is` |
| `*migrate.SumError` | migration directory does not match `sorm.sum` | `errors.As` |

The typical HTTP mapping:

```go
err := s.SaveChanges(ctx)
switch {
case err == nil:
case sorm.IsUniqueViolation(err):
    http.Error(w, "email already taken", http.StatusConflict)
case errors.As(err, &conflict):
    http.Error(w, "modified concurrently, retry", http.StatusConflict)
case errors.Is(err, sorm.ErrNotFound):
    http.Error(w, "not found", http.StatusNotFound)
default:
    http.Error(w, "internal error", http.StatusInternalServerError)
}
```

Everything else is wrapped with context (`sorm: update users: ...`) and
unwraps to the underlying driver error for the rare case you need it.

## Inspecting SQL without running it

Every builder exposes `ToSQL()`:

```go
sqlStr, args := sorm.Query[models.User](db).Where(u.Active.Eq(false)).ToSQL()
sqlStr, args, err := sorm.Update[models.User](db).Set(...).Where(...).ToSQL()
sqlStr, args, err = sorm.Project[stat](from, exprs...).ToSQL()
```

This is also the recommended way to write snapshot tests for your queries.
