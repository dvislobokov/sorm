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
type Col[E, V]     // Eq, Neq, In, NotIn, IsNull, IsNotNull, Set, Asc, Desc, ColName
type OrdCol[E, V]  // + Gt, Gte, Lt, Lte, Between
type StrCol[E]     // + Like, ILike, HasPrefix, HasSuffix, Contains
type BytesCol[E]   // Eq, Neq, IsNull, IsNotNull, Set

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
// AggExpr comparisons (Eq/Gt/Gte/Lt/Lte) yield Pred[E] valid only in Having

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

## Package `sorm/otelsorm`

```go
func Wrap(db sorm.DB, opts ...Option) sorm.DB
func WithTracerProvider(tp trace.TracerProvider) Option
func WithArgs() Option              // record query args (off by default)
```

## CLI `sorm`

```text
sorm gen [models dir]                       generate the sormgen package
sorm schema  -dialect D [-out F] [models]   render CREATE TABLE DDL
sorm migrate diff -dialect D -dir DIR -dev-dsn DSN <name> [models]
sorm migrate up   -dialect D -dir DIR -dsn DSN
```

## Struct tags

See the full [tag reference](guide/02-schema.md#tag-reference):
`pk` · `auto` · `unique` · `version` · `col:` · `type:` · `fk:` ·
`index[:name]` · `uniqueIndex[:name]` · `table:` · `-` ·
`hasMany:` · `belongsTo:` · `hasOne:` · `many2many:`
