# Multiple databases

One code path, three databases. The core never imports a driver — it
talks to `sorm.DB`, and adapters translate.

## Adapters

### PostgreSQL — `sorm/driver/pgxd`

```go
import (
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/dvislobokov/sorm/driver/pgxd"
)

pool, _ := pgxpool.New(ctx, dsn)
db := pgxd.Wrap(pool) // also accepts *pgx.Conn or pgx.Tx
```

Uses the pgx binary protocol; write batches (`SaveChanges`) go out as one
`pgx.Batch` — a single network roundtrip per flush phase; generated keys
come back via `RETURNING`.

### MySQL / SQLite — `sorm/driver/sqld`

Any `database/sql` driver, paired with a dialect:

```go
import (
    "database/sql"
    _ "github.com/go-sql-driver/mysql"
    _ "modernc.org/sqlite"
    "github.com/dvislobokov/sorm/dialect/lite"
    "github.com/dvislobokov/sorm/dialect/my"
    "github.com/dvislobokov/sorm/driver/sqld"
)

mysqlDB, _ := sql.Open("mysql", "user:pass@tcp(host:3306)/app?parseTime=true")
db := sqld.Wrap(mysqlDB, my.Dialect{})

liteDB, _ := sql.Open("sqlite", "file:app.db")
db  = sqld.Wrap(liteDB, lite.Dialect{})
```

Batches execute statement-by-statement on the same connection; generated
keys are recovered from `LastInsertId` (multi-row inserts rely on the
first-id/last-id semantics of MySQL/SQLite respectively).

## Dialect differences that matter

| Aspect | PostgreSQL | MySQL | SQLite |
|---|---|---|---|
| Placeholders | `$1` | `?` | `?` |
| Batch roundtrips | 1 per flush phase | N statements | N statements |
| Generated keys | `RETURNING` | `LastInsertId` | `LastInsertId` |
| `ILike` | native | use `Like` | use `Like` |
| Partial indexes (`Where`) | ✅ | ❌ (build error) | ✅ |
| Index `Type` (gin/fulltext) | `USING ...` | `FULLTEXT INDEX` | ❌ (build error) |
| Migration file transactionality | per file | per statement (implicit DDL commit) | per file |
| Zero `time.Time` into NOT NULL | stored as year 1 | **rejected** by strict mode — set real timestamps | stored |

Practical notes:

- **MySQL DSN**: always add `?parseTime=true`, otherwise `time.Time`
  scanning fails.
- **SQLite in-memory**: `:memory:` lives per connection — set
  `sdb.SetMaxOpenConns(1)`.
- **SQLite in tests**: the pure-Go driver (`modernc.org/sqlite`) makes the
  entire sorm test surface runnable with no external services — the same
  trick works for your application tests.

## Writing portable code

Queries, sessions, relations, projections and migrations are identical
across dialects — the integration suite runs one shared scenario against
all three databases from the same source. The few sharp edges are listed
above; when you hit one intentionally (say, a GIN index), it degrades into
an explicit build-time error on the dialects that lack it, not silent
misbehavior.

For inspection, `sorm.Query[...](nil).ToSQL()` renders with the PostgreSQL
dialect; pass a real `db` to see dialect-specific SQL.
