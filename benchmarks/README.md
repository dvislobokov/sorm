# Benchmarks: sorm vs GORM vs Ent vs raw database/sql

A separate Go module — GORM/Ent dependencies do not leak into the library's go.mod.

The DBMS is in-memory SQLite on pure-Go drivers (no cgo or network): we measure
library overhead, not the database. Same table, same data.

## Running

```bash
cd benchmarks
go generate ./models                                  # sormgen
go run -mod=mod entgo.io/ent/cmd/ent generate ./ent/schema  # ent client
go test -bench . -benchmem
```

## Results (Windows, go1.25, SQLite in-memory, 2026-07-12)

| Scenario | sorm | GORM | Ent | raw |
|---|---|---|---|---|
| **Query 1000 rows**, µs/op | **565** | 1039 | 706 | 517 |
| — allocs/op | **8 788** | 13 788 | 13 821 | 7 770 |
| **Insert 100 rows** (bulk), µs/op | **466** | 470 | 550 | — |
| **Update of a single field**, µs/op | **7.3** | 10.8 | 17.1 | — |
| — allocs/op | **39** | 56 | 107 | — |

Takeaways:

- **Reads**: sorm is within ~9% of raw `database/sql` (codegen scanners with
  no reflection), 1.8× faster than GORM and 1.25× faster than Ent, with the
  fewest allocations among the ORMs.
- **Bulk insert**: parity with GORM (both use multi-row VALUES), faster than
  Ent. Note that the sorm scenario is a full Unit of Work (`Add` × 100 →
  `SaveChanges`: snapshots, toposort, transaction), not a bare `Create`.
- **Single-row update**: sorm is faster than both — the snapshot diff is
  cheaper (26 ns, 0 allocations), and the UPDATE carries only the changed
  columns.

The sorm UpdateOne scenario additionally includes optimistic concurrency
(a version predicate) — GORM/Ent do not have it in these measurements.

Fairness notes: each library uses its idiomatic API (gorm.Create batch,
ent.CreateBulk, sorm.Session); the GORM logger is disabled; one seed and
one table shape for all.
