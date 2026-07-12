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

## OpenTelemetry: traces and metrics

`sorm/otelsorm` builds tracing **and metrics** on top of `Instrument`
(the core stays otel-free — the dependency links only if you import the
package):

```go
import "github.com/dvislobokov/sorm/otelsorm"

db = otelsorm.Wrap(db)                              // global providers
db = otelsorm.Wrap(db,
    otelsorm.WithTracerProvider(tp),
    otelsorm.WithMeterProvider(mp),
    otelsorm.WithDBStats(sdb),                      // connection-pool gauges
    otelsorm.WithArgs(),                            // span args (off by default)
    otelsorm.WithoutTableAttr(),                    // drop db.collection.name
)
```

Every operation becomes a `CLIENT` span (`sorm.query`, `sorm.batch`, …)
with `db.system`, `db.statement` and — for batches —
`db.operation.batch.size` attributes; failures set the span status and
record the error. Argument recording is opt-in because arguments routinely
contain personal data.

### The metric set

| Metric | Instrument | Attributes | Answers |
|---|---|---|---|
| `db.client.operation.duration` | histogram (s) | `db.system`, `db.operation.name`, `sorm.query.name`, `db.collection.name`, `error.type` | latency percentiles per operation, table and named query (OTel semconv-compatible) |
| `sorm.db.batch.size` | histogram | `db.system` | how well SaveChanges batches |
| `sorm.db.statements` | counter | `db.statement.kind` (insert/update/delete/select) | the write profile of the app |
| `sorm.db.errors` | counter | `error.type` (`conflict`, `constraint.unique`, `constraint.foreign_key`, `transient`, `timeout`, `other`) | optimistic-concurrency contention vs real failures — alert differently |
| `sorm.db.rows.returned` | histogram | `db.system`, `sorm.query.name` | fat result sets (a forgotten Limit) |
| `sorm.tx.duration` | histogram (s) | `outcome` (commit/rollback) | long transactions holding locks |
| `sorm.tx.retries` | counter | — | RunInTx transient-error retries |
| `sorm.pool.connections.{max,idle,used}`, `sorm.pool.acquire.*` | gauges/counters | `db.system` | pool exhaustion, acquire queuing (via `WithDBStats` / `WithPoolStats`) |

SQL text is never used as a metric attribute (unbounded cardinality) — it
lives on spans. The table attribute is a best-effort parse of the first
statement and can be disabled with `WithoutTableAttr`.

### Naming queries

Metrics become actionable when operations carry a logical name. Two ways:

```go
// Builder sugar — for a single query:
tasks, err := sorm.Query[models.Task](db).
    Named("GetOpenTasks").
    Where(t.Done.Eq(false)).
    All(ctx)

// Context — covers everything underneath: sessions, SaveChanges,
// transactions, raw SQL. An explicit context name wins over Named.
ctx = sorm.WithQueryName(ctx, "CreateOrder")
err = sorm.RunInTx(ctx, db, func(tx sorm.Tx) error { ... })
```

`Named` exists on every builder (`Query`, `Update`, `Delete`, `Raw`,
`RawAs`, `From`). The name appears as `sorm.query.name` on both spans and
metrics; custom `InstrumentFunc` middleware can read it with
`sorm.QueryNameFromContext(ctx)`.

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
