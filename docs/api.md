# API reference

A condensed map of the public API. `E`, `C`, `P` are entity type
parameters; `V` is a column's value type; `R` is a user-defined result
struct. For narrative documentation see the [guide](guide/01-getting-started.md).

## Package `sorm` — querying

```go
func Query[E any](db DB) QueryBuilder[E]

type QueryBuilder[E any] // immutable: every method returns a copy
    func (q) Where(ps ...Pred[E]) QueryBuilder[E]      // ANDed
    func (q) OrderBy(os ...Order[E]) QueryBuilder[E]
    func (q) With(specs ...IncludeSpec[E]) QueryBuilder[E]
    func (q) Limit(n int) QueryBuilder[E]
    func (q) Offset(n int) QueryBuilder[E]
    func (q) All(ctx) ([]*E, error)                    // empty slice when none
    func (q) One(ctx) (*E, error)                      // ErrNotFound when none
    func (q) Count(ctx) (int64, error)
    func (q) Iter(ctx) iter.Seq2[*E, error]            // streaming; no With
    func (q) ToSQL() (string, []any)
```

### Column descriptors (generated)

```go
type Col[E, V]     // Eq, Neq, In, NotIn, IsNull, IsNotNull, Set, SetNull, Asc, Desc, ColName
type OrdCol[E, V]  // + Gt, Gte, Lt, Lte, Between
type StrCol[E]     // + Like, ILike, HasPrefix, HasSuffix, Contains
type BytesCol[E]   // Eq, Neq, IsNull, IsNotNull, Set, SetNull
type JSONCol[E]    // Path(p) JSONPath, Contains(v), HasKey(k), IsNull, IsNotNull, Set(any), SetNull
type JSONPath[E]   // Eq, Neq, In, IsNull, IsNotNull (text extraction, dot notation)

// typed accessors, generated as <Field>Doc for struct-shaped json columns:
type JSONStr[E]      // Eq, Neq, Like, In, IsNull, IsNotNull
type JSONNum[E, V]   // Eq, Neq, Gt, Gte, Lt, Lte, IsNull, IsNotNull (V: int64 | float64)
type JSONBool[E]     // Eq, IsTrue, IsFalse, IsNull, IsNotNull
type JSONArr[E]      // Contains (PG/MySQL), IsNull, IsNotNull

// JSON helpers used by generated code (available for custom tooling):
func JSONValue(v any) driver.Valuer
func JSONScan[T any](dst *T) sql.Scanner
func JSONSnapshot(v any) []byte

type Pred[E]   // a condition; a value
type Order[E]  // a sort key
type Assign[E] // a typed assignment (from Col.Set)

func And[E](ps ...Pred[E]) Pred[E]
func Or[E](ps ...Pred[E]) Pred[E]
func Not[E](p Pred[E]) Pred[E]
```

### Set-based writes

```go
func Update[E any](db DB) UpdateBuilder[E]
    // Set(as ...Assign[E]) · Where(ps ...Pred[E]) · AllRows()
    // Exec(ctx) (rowsAffected int64, err error) · ToSQL() (string, []any, error)
    // no Where and no AllRows() → error; version column auto-incremented

func Delete[E any](db DB) DeleteBuilder[E]
    // Where · AllRows · Exec · ToSQL — same rules
```

### Raw SQL

```go
func Raw[E any](db DB, sql string, args ...any) RawQuery[E]   // scan into entities
func RawAs[R any](db DB, sql string, args ...any) RawQuery[R] // scan into any struct
    // All(ctx) ([]*T, error) · One(ctx) (*T, error)
    // strict column matching → *ScanError on mismatch
```

## Package `sorm` — sessions (Unit of Work)

```go
func NewSession(db DB) *Session            // not goroutine-safe; one per unit of work
func Track[E any](s *Session) QueryBuilder[E] // same builder; results are tracked
func Add[E any](s *Session, entities ...*E)   // schedule INSERT; FKs via navigations
func Remove[E any](s *Session, entities ...*E)// schedule DELETE

func (s *Session) SaveChanges(ctx) error   // diff → order → batch → one transaction
func (s *Session) SaveChangesTx(ctx, tx Tx) error // flush into an external tx
func (s *Session) DB() DB                  // the connection/tx the session runs on
```

### Sets & the generated Context

`sorm gen` emits `sormgen.Context` — a Session plus a typed set per entity
(field names from table names: `Users`, `ApiKeys`). Reads through a set
are tracked; `NoTracking()` opts out.

```go
// generated:
func NewContext(db sorm.DB) *Context
func (c *Context) RunInTx(ctx, fn func(txc *Context) error) error
    // fresh child context per attempt; SaveChanges joins the ambient tx

// runtime:
type Set[E any]
func NewSet[E any](s *Session) Set[E]        // wired by NewContext
func (s Set[E]) Query() QueryBuilder[E]      // tracked query root
func (s Set[E]) NoTracking() QueryBuilder[E] // untracked, read-only
func (s Set[E]) Where/OrderBy/With/Named/Limit(...) QueryBuilder[E]
func (s Set[E]) All(ctx) ([]*E, error)       // + Count, Iter
func (s Set[E]) Add(entities ...*E)
func (s Set[E]) Remove(entities ...*E)
func (s Set[E]) Find(ctx, pk any) (*E, error) // identity map first, then SELECT
```

### Transactions

```go
func RunInTx(ctx, db DB, fn func(tx Tx) error) error
// commit on nil, rollback on error; up to 3 retries on transient failures
// (PG 40001/40P01, MySQL 1213/1205, SQLITE_BUSY); fn may run more than once.
// Sessions created over tx flush into it without nesting.
```

## Package `sorm` — relations (generated descriptors)

```go
type HasMany[E, C]
    func (r) Include(opts ...ChildOpt[C]) IncludeSpec[E]
    func (r) Any(ps ...Pred[C]) Pred[E]   // EXISTS
    func (r) None(ps ...Pred[C]) Pred[E]  // NOT EXISTS
    func (r) LeftJoin(ps ...Pred[C]) JoinSpec[E]   // projections
    func (r) InnerJoin(ps ...Pred[C]) JoinSpec[E]

type BelongsTo[C, P]
    func (r) Include(opts ...ChildOpt[P]) IncludeSpec[C]
    func (r) Is(ps ...Pred[P]) Pred[C]    // filter children by parent

type HasOne[E, C]
    func (r) Include(opts ...ChildOpt[C]) IncludeSpec[E]
    func (r) Any / None

type ManyToMany[E, C]
    func (r) Include(opts ...ChildOpt[C]) IncludeSpec[E]
    func (r) Any(ps ...Pred[C]) Pred[E]
    func (r) Link(ctx, db DB, parent *E, children ...*C) error
    func (r) Unlink(ctx, db DB, parent *E, children ...*C) error

// ChildOpt[C] is satisfied by:
//   Pred[C]        — filter the loaded children
//   Order[C]       — order them
//   IncludeSpec[C] — nested eager loading (ThenInclude), any depth
```

## Package `sorm` — projections

```go
func From[E any](db DB) FromBuilder[E]
    // Where · GroupBy(cols ...ColOf[E]) · Having(ps ...Pred[E])
    // OrderBy · Limit · Offset · Join(specs ...JoinSpec[E])

// aggregates (root entity explicit, value type inferred from the column)
func CountAll[E]() AggExpr[E, int64]
func Count[E](c AnyCol) AggExpr[E, int64]
func Sum[E, V](c ColV[V]) AggExpr[E, V]
func Avg[E](c AnyCol) AggExpr[E, float64]
func Min[E, V](c ColV[V]) AggExpr[E, V]
func Max[E, V](c ColV[V]) AggExpr[E, V]
func CountDistinct[E](c AnyCol) AggExpr[E, int64]    // count(DISTINCT col), portable
// AggExpr comparisons (Eq/Gt/Gte/Lt/Lte) yield Pred[E] valid only in Having

// extension API for custom / dialect-specific aggregates
func NewAgg[E any, V comparable](parts ...AggPart) AggExpr[E, V]
func AggRaw(sql string) AggPart      // verbatim SQL fragment
func AggCol(c AnyCol) AggPart        // qualified column reference
func AggArg(v any) AggPart           // bind parameter
func AggLit(s string) AggPart        // quote-escaped string literal
func AggDialect(name string) AggPart // guard: build error on any other dialect

// select list
func Field[E](c ColOf[E]) SelectExpr[E]              // root column, E inferred
func FieldAs[E](c ColOf[E], alias string) SelectExpr[E]
func FieldOf[E](c AnyCol) SelectExpr[E]              // joined entity's column
func FieldOfAs[E](c AnyCol, alias string) SelectExpr[E]
func As[E, V](a AggExpr[E, V], alias string) SelectExpr[E]

// arbitrary joins
func ColEq[C, E, V](joined ColOfV[C, V], existing ColOfV[E, V]) JoinOn[C, E]
func LeftJoinOn[C, E](on JoinOn[C, E], ps ...Pred[C]) JoinSpec[E]
func InnerJoinOn[C, E](on JoinOn[C, E], ps ...Pred[C]) JoinSpec[E]
func CrossJoin[C, E]() JoinSpec[E]

func Project[R, E any](q FromBuilder[E], exprs ...SelectExpr[E]) ProjQuery[R]
    // All(ctx) ([]*R, error) · One(ctx) (*R, error) · ToSQL() (string, []any, error)
```

## Package `pgagg` — PostgreSQL aggregates

Guarded: executing on another dialect returns a build error.

```go
func StringAgg[E](c AnyCol, sep string) AggExpr[E, string]   // string_agg(col::text, $1)
func ArrayAgg[E](c AnyCol) AggExpr[E, string]                // array_agg(col)
func JSONBAgg[E](c AnyCol) AggExpr[E, string]                // jsonb_agg(col)
func JSONBObjectAgg[E](k, v AnyCol) AggExpr[E, string]       // jsonb_object_agg(k, v)
func BoolAnd[E](c AnyCol) AggExpr[E, bool]                   // bool_and(col)
func BoolOr[E](c AnyCol) AggExpr[E, bool]                    // bool_or(col)
func BitAnd[E](c AnyCol) AggExpr[E, int64]
func BitOr[E](c AnyCol) AggExpr[E, int64]
func StdDev[E](c AnyCol) AggExpr[E, float64]                 // + StdDevPop, StdDevSamp
func Variance[E](c AnyCol) AggExpr[E, float64]               // + VarPop, VarSamp
func Corr[E](y, x AnyCol) AggExpr[E, float64]
func CovarPop[E](y, x AnyCol) AggExpr[E, float64]            // + CovarSamp
func PercentileCont[E](fraction float64, orderBy AnyCol) AggExpr[E, float64]
    // percentile_cont($1) WITHIN GROUP (ORDER BY col); + PercentileDisc
func Mode[E](orderBy AnyCol) AggExpr[E, string]              // mode() WITHIN GROUP (ORDER BY col)
```

## Package `myagg` — MySQL aggregates

Guarded the same way.

```go
func GroupConcat[E](c AnyCol) AggExpr[E, string]                     // GROUP_CONCAT(col)
func GroupConcatSep[E](c AnyCol, sep string) AggExpr[E, string]      // ... SEPARATOR 'sep'
func GroupConcatDistinct[E](c AnyCol, sep string) AggExpr[E, string] // GROUP_CONCAT(DISTINCT ...)
func JSONArrayAgg[E](c AnyCol) AggExpr[E, string]                    // JSON_ARRAYAGG(col)
func JSONObjectAgg[E](k, v AnyCol) AggExpr[E, string]                // JSON_OBJECTAGG(k, v)
func AnyValue[E, V](c ColV[V]) AggExpr[E, V]                         // ANY_VALUE(col)
func StdDev[E](c AnyCol) AggExpr[E, float64]                         // + StdDevPop, StdDevSamp
func VarPop[E](c AnyCol) AggExpr[E, float64]                         // + VarSamp
func BitAnd[E](c AnyCol) AggExpr[E, int64]                           // + BitOr, BitXor
```

## Package `sorm` — infrastructure

```go
type DB interface { // implemented by driver adapters
    Dialect() dialect.Dialect
    Query(ctx, sql string, args ...any) (Rows, error)
    Exec(ctx, sql string, args ...any) (int64, error)
    ExecBatch(ctx, items []BatchItem) error
    Begin(ctx) (Tx, error)
}
type Tx interface { DB; Commit(ctx) error; Rollback(ctx) error }

func Instrument(db DB, fn InstrumentFunc) DB
type Op struct { Kind, SQL string; Args []any; Statements []string }
type InstrumentFunc func(ctx, op Op, next func(ctx) error) error
```

### Errors

```go
var ErrNotFound error
var ErrCyclicGraph error
type ConflictError struct{ Table string; PK any }          // optimistic concurrency
type ConstraintError struct{ Kind ConstraintKind; Constraint string; Err error }
// ConstraintUnique | ConstraintForeignKey | ConstraintNotNull | ConstraintCheck
func IsUniqueViolation(err error) bool
type ScanError struct{ Missing, Extra []string }
```

### Schema metadata (advanced / generated)

```go
type TableDef struct{ Name string; Columns []ColumnDef; Indexes []IndexDef }
type ColumnDef struct{ Name, GoKind, SQLType, RefTable, RefCol string; Nullable, Unique, PK, Auto bool }
type IndexDef struct{ Name string; Columns []string; Parts []IndexPart; Unique bool; Type, Where string }
type IndexPart struct{ Column, Expr string; Desc bool }
func RegisterTable(def TableDef) / UnregisterTable(name string) / Tables() []TableDef
func SQLTypeFor(dialect string, c ColumnDef) string
func MetaOf[E any]() *Meta[E]  // registered runtime metadata (tests, tooling)
```

## Package `sorm/migrate`

```go
// declarative
func Apply(ctx, db *sql.DB, dialect string) error
func Plan(ctx, db *sql.DB, dialect string) ([]string, error)

// versioned files (dir + sorm_migrations history table)
func Diff(ctx, dev *sql.DB, dialect, dir, name string) (filename string, err error)
func Up(ctx, db *sql.DB, dialect, dir string) (applied []string, err error)
func Down(ctx, db *sql.DB, dialect, dir string, steps int) (reverted []string, err error)
func Pending(ctx, db *sql.DB, dialect, dir string) ([]string, error)

// checksums (sorm.sum)
func WriteSum(dir string) error
func VerifySum(dir string) error   // nil if sorm.sum absent
type SumError struct{ Modified, Missing, Extra []string }

const HistoryTable = "sorm_migrations"
const SumFile = "sorm.sum"
func SplitStatements(content string) []string
```

Dialect strings everywhere: `"postgres"`, `"mysql"`, `"sqlite"`.

## Driver adapters & dialects

```go
// sorm/driver/pgxd
func Wrap(p Pgx) sorm.DB            // *pgxpool.Pool, *pgx.Conn, pgx.Tx

// sorm/driver/sqld
func Wrap(sdb *sql.DB, d dialect.Dialect) sorm.DB

// sorm/dialect/{pg,my,lite}
type Dialect struct{}               // Name, Placeholder, QuoteIdent, ReturningSupported
```

### Query naming

```go
func WithQueryName(ctx context.Context, name string) context.Context
func QueryNameFromContext(ctx context.Context) string
// plus .Named(name) on QueryBuilder, UpdateBuilder, DeleteBuilder,
// RawQuery and FromBuilder — the name reaches spans and metrics as
// sorm.query.name (an explicit context name wins over Named).
```

## Package `sorm/otelsorm`

```go
func Wrap(db sorm.DB, opts ...Option) sorm.DB   // traces + metrics
func WithTracerProvider(tp trace.TracerProvider) Option
func WithMeterProvider(mp metric.MeterProvider) Option
func WithArgs() Option              // record query args on spans (off by default)
func WithoutTableAttr() Option      // drop best-effort db.collection.name
func WithDBStats(sdb *sql.DB) Option            // pool gauges from database/sql
func WithPoolStats(fn func() PoolStats) Option  // pool gauges from any source
type PoolStats struct{ Max, Idle, Used, WaitCount int64; WaitDuration time.Duration }
```

Metrics: `db.client.operation.duration`, `sorm.db.batch.size`,
`sorm.db.statements`, `sorm.db.errors`, `sorm.db.rows.returned`,
`sorm.tx.duration`, `sorm.tx.retries`, `sorm.pool.*` — see the
[observability guide](guide/08-observability.md#the-metric-set).

## CLI `sorm`

```text
sorm gen [models dir]                       generate the sormgen package
sorm schema  -dialect D [-out F] [models]   render CREATE TABLE DDL
sorm migrate diff -dialect D -dir DIR -dev-dsn DSN <name> [models]
sorm migrate up   -dialect D -dir DIR -dsn DSN
```

## Struct tags

See the full [tag reference](guide/02-schema.md#tag-reference):
`pk` · `auto` · `unique` · `version` · `autoCreate` · `autoUpdate` ·
`col:` · `type:` · `fk:` ·
`index[:name]` · `uniqueIndex[:name]` · `table:` · `-` ·
`hasMany:` · `belongsTo:` · `hasOne:` · `many2many:` · `json`
