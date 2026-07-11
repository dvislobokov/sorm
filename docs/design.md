# Дизайн MVP: публичный API и схема кодогенерации

*Статус: черновик v2 после адversarial-ревью на реализуемость в Go 1.23 generics. Опирается на [концепцию](concept.md); скоуп — этап 1 roadmap (PG-only).*

## 1. Цели и не-цели MVP

**Цели** — продемонстрировать дифференциатор end-to-end на PostgreSQL:
- schema-as-code → `sorm gen` → типизированный DSL + snapshot/diff + сканеры;
- запросы: Where/OrderBy/Limit/Include (split query и JOIN), вложенное сканирование;
- сессия: identity map, tracking, `SaveChanges` с диффом, топосортом и `pgx.Batch`;
- optimistic concurrency через поле-версию;
- raw-escape с маппингом в те же модели.

**Не-цели MVP**: MySQL/SQLite (но абстракция диалекта закладывается), миграции (Atlas — этап 3), hooks/interceptors, OTel, JSON-предикаты, many2many, полиморфные связи. Проекционный слой (GROUP BY/агрегации/произвольные JOIN, §5.3) — последний PR MVP: спроектирован сейчас, реализуется после ядра.

**Главное языковое ограничение, определившее форму API**: в Go методы не могут вводить новые type parameters. Поэтому всё, что требует второго типа (связанная сущность `C`, тип значения `V`), выражается не методами `Query[E]`, а **методами дескрипторов** (колонок и связей), которые возвращают значения, параметризованные только `E`: `Assign[E]`, `Pred[E]`, `IncludeSpec[E]`. Билдер принимает уже «замкнутые» значения.

## 2. Пакеты

```
sorm/                   публичный рантайм (единственный import для пользователя)
  ├─ dialect/           интерфейс Dialect + dialect/pg (pgx v5)
  ├─ internal/plan      построение SQL из AST предикатов
  └─ internal/flush     дифф → топосорт → батчи
cmd/sorm/               CLI: `sorm gen`
  ├─ internal/parse     разбор схемы через go/packages + AST
  └─ internal/codegen   шаблоны генерации
```

Пользовательский код:

```
yourapp/
  ├─ models/            структуры с тегами `sorm:` (источник истины)
  └─ models/sormgen/    сгенерированный пакет (в VCS, читаемый в PR)
```

## 3. Schema-as-code

Обычные Go-структуры с тегом `sorm`. Никаких интерфейсов, которые нужно реализовать (анти-Ent: сущность — просто данные, пригодные в domain-слое).

```go
package models

type User struct {
    ID      int64     `sorm:"pk,auto"`
    Email   string    `sorm:"unique"`
    Name    string
    Active  bool
    Age     int
    Version int64     `sorm:"version"`
    Posts   []*Post   `sorm:"hasMany:AuthorID"`      // навигация, не колонка
}

type Post struct {
    ID       int64  `sorm:"pk,auto"`
    AuthorID int64  `sorm:"fk:User.ID"`
    Author   *User  `sorm:"belongsTo:AuthorID"`
    Title    string
    Body     string `sorm:"type:text"`
}
```

Соглашения (переопределяемые тегом):
- таблица — snake_case множественное (`users`), колонка — snake_case (`author_id`);
- `pk` обязателен ровно один (композитные PK — вне MVP); `auto` = identity;
- `version` — int64; при INSERT инициализируется в 1, при UPDATE инкрементируется рантаймом;
- nullable ⇔ указатель (`*string`) или `sql.Null*`; `sorm:"-"` — игнорировать поле;
- навигационные поля не являются колонками; nil-слайс = «не загружено», загруженный пустой = `[]*Post{}` — различие «забыл Include» и «нет данных» видно в данных (против тихих nil GORM).

Валидация схемы — в `sorm gen`, падает с внятной ошибкой (битая ссылка `fk:`, два `pk`, `version` не int64, `hasMany` без обратного FK и т.д.).

## 4. Что генерирует `sorm gen`

Один пакет `sormgen` на пакет моделей. Компактность — плоские дескрипторы и мета + замыкание на generic-рантайм (не тысячи строк методов на сущность, как у Ent). Оценка: ~150–220 строк на сущность.

### 4.1 Дескрипторы колонок и связей

Генератор выбирает тип дескриптора по Go-типу поля: `Col` (равенство: bool и др.), `OrdCol` (числа, time.Time: + Gt/Gte/Lt/Lte/Between), `StrCol` (строки: + Like/ILike/HasPrefix), `BytesCol` (`[]byte`: только Eq/Neq/IsNull — без In, см. §10). Для nullable-полей (`*string`) дескриптор генерируется по **base-типу** (`StrCol[E]` с предикатами по `string`) плюс `IsNull()/IsNotNull()` — пользователь не жонглирует указателями в предикатах.

```go
// sormgen/user.go (сгенерировано)
var User = struct {
    ID      sorm.OrdCol[models.User, int64]
    Email   sorm.StrCol[models.User]
    Name    sorm.StrCol[models.User]
    Active  sorm.Col[models.User, bool]
    Age     sorm.OrdCol[models.User, int]
    Posts   sorm.HasMany[models.User, models.Post]
}{
    ID:     sorm.NewOrdCol[models.User, int64]("id"),
    Email:  sorm.NewStrCol[models.User]("email"),
    // ...
    Posts:  sorm.NewHasMany[models.User, models.Post]("author_id"),
}
```

Рантаймные типы (пакет `sorm`, не генерируются):

```go
type Col[E any, V comparable] struct{ name string }

func (c Col[E, V]) Eq(v V) Pred[E]
func (c Col[E, V]) Neq(v V) Pred[E]
func (c Col[E, V]) In(vs ...V) Pred[E]
func (c Col[E, V]) IsNull() Pred[E]
func (c Col[E, V]) Set(v V) Assign[E]   // для set-based Update и INSERT DSL
func (c Col[E, V]) Asc() Order[E]
func (c Col[E, V]) Desc() Order[E]
```

Ключевые свойства:
- `Pred[E]`/`Assign[E]`/`Order[E]` параметризованы сущностью — **условие по Post нельзя подать в запрос по User**, ошибка компиляции.
- `Eq(false)`, `Eq(0)`, `Set(false)` — полноценные операции: предикаты и присваивания не знают понятия zero-value (закрывает GORM #6232).
- `Pred[E]` — иммутабельное значение (AST); композиция `sorm.And/Or/Not` и свободное накопление в `if`-ах (закрывает слабость sqlc).

### 4.2 Связи как источник типобезопасности

`HasMany[E, C]` знает оба типа, поэтому всё «двухтиповое» — его методы:

```go
type HasMany[E, C any] struct{ fkCol string }

// фильтр родителя по детям → компилируется в EXISTS(...)
func (r HasMany[E, C]) Any(preds ...Pred[C]) Pred[E]
func (r HasMany[E, C]) None(preds ...Pred[C]) Pred[E]

// eager loading: возвращает спецификацию, замкнутую по E
func (r HasMany[E, C]) Include(preds ...Pred[C]) IncludeSpec[E]
```

`IncludeSpec[E]` настраивается чейнингом: `.Join()` / `.Split()` (дефолт), `.OrderBy(o Order[C])` — стратегия не передаётся в общем variadic с фильтрами, потому что неgeneric-значение опции не может удовлетворить `IncludeOpt[C]` для произвольного `C`.

### 4.3 Метаданные и функции без рефлексии

Снапшот — **типизированная генерируемая структура** (не `[]any`): без boxing-аллокаций, и сравнение генерируется корректно по типу поля — `bytes.Equal` для `[]byte`, `.Equal()` c нормализацией monotonic-компоненты для `time.Time` (наивный `==` по time.Time даёт фантомные UPDATE, по `[]byte` в интерфейсе — панику).

```go
// sormgen/user_meta.go (сгенерировано)
func init() { sorm.Register(userMeta) }

type userSnap struct {
    email, name string
    active      bool
    age         int
    version     int64
}

var userMeta = sorm.Meta[models.User]{
    Table: "users",
    PK:    "id", Auto: true, Version: "version",
    SelectCols: []string{"id", "email", "name", "active", "age", "version"},
    InsertCols: []string{"email", "name", "active", "age", "version"}, // без auto-PK
    Scan: func(dest *models.User) []any {
        return []any{&dest.ID, &dest.Email, &dest.Name, &dest.Active, &dest.Age, &dest.Version}
    },
    InsertValues: func(e *models.User) []any {
        return []any{e.Email, e.Name, e.Active, e.Age, e.Version}
    },
    // мост между диффом и UPDATE: значения по индексам изменённых колонок
    ValuesFor: func(e *models.User, cols []int) []any { /* switch по индексу */ },
    Snapshot: func(e *models.User) userSnap { /* пополевая копия */ },
    Diff: func(s userSnap, e *models.User) []int {
        var ch []int
        if s.email != e.Email { ch = append(ch, 1) }
        if s.name != e.Name   { ch = append(ch, 2) }
        // []byte → bytes.Equal, time.Time → .Equal(), сгенерировано по типу
        return ch
    },
    SetPK: func(e *models.User, id int64) { e.ID = id },
    // навигационные рёбра для топосорта по ЭКЗЕМПЛЯРАМ и FK-fixup
    Refs: []sorm.Ref[models.User]{ /* см. §6.2 */ },
}
```

Примечание: состав и порядок колонок различаются для SELECT / INSERT / UPDATE (auto-PK не вставляется, version при INSERT инициализируется) — поэтому в мете отдельные списки, а не одна `Cols`.

`sorm.Register` кладёт мету в реестр `map[reflect.Type]any`. Единственная рефлексия рантайма — **один lookup `reflect.TypeOf((*E)(nil))` при построении запроса/трекинге**; сканирование, дифф и маппинг рефлексию не используют.

## 5. Query DSL

```go
q := sorm.Query[models.User](db).
    Where(User.Active.Eq(true)).
    Where(User.Age.Gte(18)).          // несколько Where = AND
    OrderBy(User.Name.Asc()).
    Limit(50)

users, err := q.All(ctx)      // []*models.User, БЕЗ трекинга
user, err  := q.One(ctx)      // *models.User или sorm.ErrNotFound
n, err     := q.Count(ctx)
sqlStr, args := q.ToSQL()     // инспекция — ответ скептикам
```

Правила:
- билдер **иммутабелен**: каждый метод возвращает копию; переиспользование базового `q` безопасно (закрывает GORM SQL pollution);
- `ctx` — параметр каждого исполняющего метода, вариантов без ctx нет;
- `One` при отсутствии → `sorm.ErrNotFound` (единая семантика «не найдено» во всём API); `All` при пустоте → пустой слайс, nil error.

### 5.1 Include и фильтрация по связям

```go
users, err := sorm.Query[models.User](db).
    Where(User.Active.Eq(true)).
    Where(User.Posts.Any(Post.Body.Like("%go%"))).       // EXISTS — фильтр родителя по детям (закрывает Bun #604)
    With(User.Posts.Include(Post.Title.HasPrefix("A"))). // eager, дефолт — split query
    All(ctx)

// JOIN-стратегия — явный выбор на спецификации:
q.With(User.Posts.Include().Join())
```

- **Split (дефолт)**: 2 запроса — родители, затем дети `WHERE author_id IN (...)`, раскладка in-memory. Предсказуемо, без дублирования строк, дружит с Limit.
- **Join**: один запрос; сканер дедуплицирует родителей по PK при сборке. **Сочетание `.Join()` с `Limit/Offset` — ошибка валидации в MVP**: JOIN размножает родительские строки и ломает пагинацию (ровно та причина, по которой EF ввёл split queries).
- Вложенность (`ThenInclude`) — один уровень в MVP: `User.Posts.Include().Then(Post.Comments.Include())`.

### 5.2 Set-based операции (без сессии)

```go
n, err := sorm.Update[models.User](db).
    Set(User.Active.Set(false), User.Name.Set("archived")). // Assign[E] от дескриптора
    Where(User.Age.Lt(18)).
    Exec(ctx)

n, err = sorm.Delete[models.User](db).Where(...).Exec(ctx)
```

Типобезопасность — на дескрипторе (`Col[E, V].Set(V)`), а не на методе билдера: метод принимает уже замкнутые `Assign[E]`.

### 5.3 Проекционный слой: GROUP BY, HAVING, агрегации, произвольные JOIN

Запросы с `GROUP BY`/агрегациями возвращают не сущности, а произвольные формы строк — это принципиально другой слой API. EF Core решает его анонимными типами в LINQ; в Go анонимных типов нет, поэтому **каждой проекции нужна именованная структура результата** (обычная, без кодогена):

```go
type AgeStat struct {
    Age int   `sorm:"age"`
    N   int64 `sorm:"n"`
}

stats, err := sorm.Project[AgeStat](
    sorm.From[models.User](db).
        Where(User.Active.Eq(true)).
        GroupBy(User.Age).
        Having(sorm.CountAll().Gt(10)).
        OrderBy(User.Age.Asc()),
    sorm.Field(User.Age),            // SELECT age
    sorm.As(sorm.CountAll(), "n"),   // SELECT count(*) AS n
).All(ctx)
```

Устройство:
- `sorm.From[E](db)` — иммутабельный билдер с `GroupBy(cols ...ColOf[E])`, `Having(preds ...Pred[E])`, `OrderBy/Limit/Offset`, `Join(specs ...JoinSpec[E])`.
- Агрегатные выражения — свободные функции (им нужны свои type params, методам билдера — нельзя): `sorm.CountAll[E]()`, `sorm.Count[E](col)`, `sorm.Sum[E](col)`, `Avg/Min/Max`. Корневая сущность `E` указывается явно; колонка — любой сущности (`AnyCol`/`ColV[V]` — count по колонке ребёнка после JOIN — типичный случай), тип значения `V` выводится из колонки. Сравнения дают `Pred[E]` для HAVING.
- `sorm.Project[R](builder, exprs...)` — свободная функция с независимым параметром `R`. Маппинг алиасов выражений на поля `R` — по тегу/имени, строгий в обе стороны (расхождение → `ScanError` до запроса); план сканирования строится **один раз на тип `R` через рефлексию и кэшируется** (осознанное исключение из «zero reflection», стоимость как регистрация модели в Bun).
- Валидации при построении: агрегатный `Pred` в `Where` → ошибка «use Having»; колонка не из FROM/JOIN-набора → ошибка принадлежности (вместо SQL-ошибки сервера).

**Произвольные JOIN** (не по объявленной связи) живут в этом же слое:

```go
rows, err := sorm.Project[R](
    sorm.From[models.User](db).
        LeftJoin(User.Posts).                                    // по связи — типобезопасно полностью
        LeftJoinOn(sormgen.OrdersTable, sorm.ColEq(Order.UserID, User.ID)). // произвольный ON
        CrossJoin(sormgen.RegionsTable),
    ...,
).All(ctx)
```

- По связи: `rel.LeftJoin(preds...)` / `rel.InnerJoin(preds...)` — методы дескриптора связи (оба типа известны), ON = FK-равенство + preds детей, полная типобезопасность.
- Произвольный: `sorm.LeftJoinOn/InnerJoinOn(sorm.ColEq(joined, existing), preds...)`, `sorm.CrossJoin[C, E]()`. `ColEq` типизирован по значению колонок (`ColOfV`): сравнение int-колонки со string-колонкой не скомпилируется. Принадлежность колонок FROM/JOIN-набору — рантайм-валидация при построении, как у Jet: на кросс-табличном уровне энтити-параметризация ослабляется осознанно.
- Колонки присоединённых сущностей в SELECT — `sorm.FieldOf[E](c)` / `FieldOfAs` (ослабленный режим); фильтры по присоединённой сущности — в ON через preds JOIN-спецификации.
- `RIGHT JOIN` не реализован в MVP — переписывается в LEFT.
- SQL проекций всегда квалифицирован (`"users"."id"`) — после JOIN имена неоднозначны; энтити-запросы остаются неквалифицированными.

**Дальние SQL-фичи** (UNION/INTERSECT, CTE, window functions, `DISTINCT ON` с сортировкой, LATERAL): в MVP — через Raw; проекционный слой спроектирован так, чтобы добавлять их аддитивно (`sorm.Union(a, b)`, `sorm.With("cte", sub)` — свободные функции над билдерами). Не пытаемся покрыть 100% SQL типизированно — это путь Jet к многословности; правило: **типизируем то, что пишут каждый день, для остального — первоклассный Raw**.

### 5.4 Raw escape

```go
users, err := sorm.Raw[models.User](db,
    "SELECT * FROM users WHERE ...", args...).All(ctx)     // в сущности (Meta.Scan)

stats, err := sorm.RawAs[AgeStat](db,
    "SELECT age, count(*) AS n FROM users GROUP BY age HAVING count(*) > $1", 10,
).All(ctx)                                                  // в произвольную структуру
```

`Raw` сканирует в сущности через `Meta.Scan`; `RawAs` — в произвольную структуру через тот же кэшируемый план, что у `Project` (§5.3). Лишние/недостающие колонки → `*sorm.ScanError` со списками имён (закрывает боль sqlx и покрывает CTE/window/LATERAL до их типизации).

## 6. Сессия и SaveChanges

### 6.1 API

```go
s := sorm.NewSession(db)
u, err := sorm.Track[models.User](s).Where(User.ID.Eq(42)).One(ctx)
// Track — тот же билдер, что Query, но материализованные сущности попадают в трекер

u.Active = false                                // обычная мутация
p := &models.Post{Author: u, Title: "hi"}       // FK нового графа — ТОЛЬКО навигацией
s.Add(p)
s.Remove(u.Posts[0])

err = s.SaveChanges(ctx)
```

Контракт:
- **identity map** — только для persisted-сущностей (`map[PK]*T`); повторный `Track`-запрос той же строки возвращает уже отслеживаемый указатель, данные из БД не перезатирают локальные изменения (семантика EF). **Added-сущности хранятся отдельным множеством по указателю** (у них нет валидного PK — нулевой ключ давал бы коллизии) и переносятся в map после `RETURNING`.
- **Правило FK для новых сущностей**: связь задаётся навигацией (`Author: u`); значение FK-колонки Added-сущности игнорируется, если навигация указывает на tracked-объект — рантайм проставит его после вставки родителя (fixup). Если навигация nil, FK-колонка нулевая и колонка NOT NULL — ошибка **до** отправки в БД (не молчаливый INSERT нуля и не невнятный FK violation от сервера).
- Include при Track трекает и детей;
- новые сущности — только через `s.Add` (MVP не делает graph discovery; честная явность, расширение позже);
- `Remove` помечает DELETE; каскады — на стороне БД (`ON DELETE`);
- сессия не потокобезопасна (как DbContext), живёт один юнит работы;
- `SaveChanges` сам открывает транзакцию; `SaveChangesTx(ctx, tx)` — для внешней.

### 6.2 Алгоритм flush

1. Для каждой tracked persisted-сущности: `Diff(snapshot, e)` → индексы изменённых колонок; пусто → пропуск.
2. Классификация: inserts (`Add`), updates (дифф непуст), deletes (`Remove`).
3. **Топосорт по экземплярам, не по таблицам**: рёбра — конкретные навигационные ссылки между tracked-объектами (`Meta.Refs`). Таблично-уровневый сорт ломается на self-reference (`posts.parent_id → posts`) и взаимных FK; сорт по экземплярам решает оба случая. Настоящий цикл экземпляров → `sorm.ErrCyclicGraph` в MVP (позже — insert-then-update по nullable-FK / deferred constraints).
4. Батчирование: **один `pgx.Batch` на топо-уровень**. Insert ребёнка может требовать auto-PK родителя, известный только после `RETURNING id` предыдущего уровня. Типичный кейс (updates + inserts одного уровня) = один roundtrip; цепочка parent→child→grandchild = 3 — честный минимум без CTE-магии. Всё в одной транзакции.
5. После каждого уровня: `RETURNING id` → `Meta.SetPK` → **FK-fixup** ожидающих детей по навигационным ссылкам.
6. UPDATE несёт только изменённые колонки (`Meta.ValuesFor(e, idxs)`) + `SET version = version + 1 WHERE pk = $1 AND version = $2`.
7. rows-affected = 0 у версионируемого UPDATE/DELETE → rollback + `*sorm.ConflictError{Entity, ExpectedVersion}`.
8. Успех → commit; снапшоты пересоздаются, Added переходят в identity map.

### 6.3 Состояния

`Untracked → Tracked(Unchanged) → Modified (вычисляется при flush, не при мутации) / Added / Removed`. Никаких прокси и сеттеров: изменение — обычное присваивание; стоимость — O(колонок) сравнение при SaveChanges (ровно так работает snapshot-трекер EF Core).

## 7. Диалект

```go
type Dialect interface {
    Placeholder(n int) string            // $1 vs ?
    QuoteIdent(s string) string
    InsertReturning(table string, cols []string) (sql string, supportsReturning bool)
    // минимальная поверхность; batch-стратегия — часть драйверного адаптера
}
```

MVP: только `dialect/pg` поверх pgx v5 (`pgx.Batch`, `CollectRows`). Executor принимает интерфейс `sorm.DB`, который реализуют `*pgxpool.Pool` и `pgx.Tx` (позже — адаптер `database/sql` для MySQL/SQLite; для них flush-уровень использует multi-statement в транзакции и `LastInsertId`).

## 8. Ошибки

- `sorm.ErrNotFound` — sentinel, `errors.Is`;
- `*sorm.ConflictError` — optimistic concurrency, `errors.As`;
- `*sorm.ScanError` — несоответствие колонок при Raw;
- `sorm.ErrCyclicGraph` — цикл экземпляров при flush;
- ошибка валидации графа (NOT NULL FK без навигации) — до похода в БД;
- остальное — обёрнутые ошибки драйвера с контекстом (`sorm: update users: %w`).

## 9. CLI

```
sorm gen ./models                          # кодоген ./models/sormgen
sorm schema -dialect postgres ./models     # каноническая DDL-схема (stdout/-out)
sorm migrate diff <name> ./models          # версионная миграция (обёртка atlas CLI)
```

Через `//go:generate sorm gen .`. Генератор детерминирован (стабильный порядок), diff-friendly вывод, ошибки валидации схемы — до записи файлов.

## 9a. Миграции (реализовано)

Два пути, оба на движке Atlas:

1. **Из кода** — пакет `sorm/migrate` (Atlas как Go-зависимость `ariga.io/atlas`, внешний CLI не нужен): `migrate.Apply(ctx, sdb, "postgres")` инспектирует БД, диффует против зарегистрированных моделей (`sorm gen` эмитит `TableDef` в sormgen) и применяет изменения; `migrate.Plan` — dry-run SQL. Дифф ограничен таблицами sorm — чужие таблицы не трогаются. Рантайм sorm от Atlas не зависит — зависимость линкуется только при импорте `sorm/migrate`.
2. **Версионные файлы** (CI/ревью/прод) — `sorm schema` генерирует канонический DDL, `sorm migrate diff` — тонкая обёртка над установленным atlas CLI (`atlas migrate diff --to file://schema.sql --dev-url docker://...`).

Интеграционные тесты создают таблицы сгенерированной схемой — DDL-генератор проверен рантаймом на всех трёх диалектах.

## 10. Решения по типам (бывшие открытые вопросы)

1. **`[]byte`** не comparable → отдельный `BytesCol[E]` c `Eq/Neq/IsNull` (SQL `= $1` допустим), без `In`.
2. **Nullable**: дескриптор по base-типу + `IsNull/IsNotNull` (см. §4.1) — предикаты без указателей.
3. **Снапшот**: типизированные генерируемые структуры, сравнение генерируется по типу поля (`bytes.Equal`, `time.Time.Equal` с нормализацией monotonic) — быстрее `[]any` и без ловушек интерфейсного `==`.
4. **Запросы внутри сессии мимо трекера**: `Query` не сверяется с identity map, только `Track` (MVP).

## 11. План реализации (порядок PR)

1. `sorm`: Meta/Register, дескрипторы Col/OrdCol/StrCol/BytesCol + Pred AST, dialect/pg, план SELECT, executor + сканирование → `Query.All/One` работает на рукописной мете.
2. `cmd/sorm gen`: parse + codegen меты и дескрипторов → рукописная мета заменяется сгенерированной; golden-тесты генератора.
3. Include (split, затем join + дедуп) + `HasMany.Any` (EXISTS).
4. Session: трекер (persisted map + added set), snapshot/diff, flush по уровням с топосортом по экземплярам, RETURNING/fixup, version/ConflictError.
5. Update/Delete set-based, Raw/RawAs, полировка ошибок.
6. Проекционный слой: From/GroupBy/Having/агрегаты, LeftJoinOn/CrossJoin с валидацией принадлежности колонок, Project[R] с кэшируемым планом сканирования.

Верификация: интеграционные тесты на PostgreSQL (testcontainers), каждый этап — сквозной тест из README-примера; `ToSQL()`-снапшот-тесты планировщика; отдельные тесты на ловушки из ревью: self-reference топосорт, Added с navigation-FK, time.Time-фантомные диффы, JOIN+Limit-валидация.
