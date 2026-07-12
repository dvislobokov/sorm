# sorm

**Первая Go ORM с честным Unit of Work.** Загрузили граф сущностей, мутировали
обычным Go-кодом, вызвали `SaveChanges` — sorm сам вычислит минимальный дифф,
упорядочит запись по FK-зависимостям и применит её батчами в одной транзакции.
Ни одна другая Go-библиотека этого не делает.

```go
s := sorm.NewSession(db)
user, _ := sorm.Track[models.User](s).
    Where(u.Email.Eq("alice@example.com")).
    With(u.Posts.Include()).
    One(ctx)

user.Active = false          // обычные присваивания —
user.Posts[0].Title = "new"  // никаких сеттеров и грязных флагов

err := s.SaveChanges(ctx)    // дифф → топосорт → батч → одна транзакция
```

## Что внутри

| | |
|---|---|
| **Unit of Work** | identity map, snapshot-дифф без рефлексии (26 нс/сущность, 0 аллокаций), UPDATE только изменённых колонок, вставка графов с FK-fixup через `RETURNING` |
| **Optimistic concurrency** | поле `sorm:"version"` → каждый UPDATE/DELETE несёт version-предикат; конфликт → типизированный `*ConflictError` |
| **Типобезопасные запросы** | кодоген дескрипторов колонок: `Where(u.Age.Gte(18))` проверяется компилятором; условия — значения, композируются в `if`-ах (то, что не может sqlc) |
| **Zero-value честность** | `Where(u.Active.Eq(false))` и `Set(u.Age.Set(0))` — полноценные операции (классический footgun GORM исключён по построению) |
| **Связи** | hasMany/belongsTo: eager loading split-запросами, `Any`/`Is` → EXISTS-фильтры родителей по детям и наоборот |
| **Проекции** | GroupBy/Having, типизированные агрегаты, JOIN по связям и произвольные, сканирование в ваши структуры |
| **Миграции** | встроенный движок диффа (Atlas SDK, без внешнего CLI): декларативные `Apply`/`Plan` и версионные файлы `Diff`/`Up`/`Pending` — всё из кода; advisory lock от гонки реплик |
| **3 СУБД** | PostgreSQL (pgx, батчи одним roundtrip), MySQL, SQLite — один код, разные адаптеры |
| **Продовые мелочи** | `RunInTx` с ретраями deadlock/serialization, типизированные `ConstraintError`, `Instrument` для логирования SQL и трейсинга |

Эскейпы честные: `ToSQL()` покажет любой запрос, `Raw`/`RawAs` сканируют
сырой SQL в ваши типы со строгой проверкой колонок.

## Быстрый старт

**1. Опишите модели** — обычные структуры, никаких интерфейсов:

```go
package models

//go:generate go run sorm/cmd/sorm gen .

type User struct {
    ID      int64   `sorm:"pk,auto"`
    Email   string  `sorm:"unique"`
    Active  bool
    Version int64   `sorm:"version"`
    Posts   []*Post `sorm:"hasMany:AuthorID"`
}

type Post struct {
    ID       int64  `sorm:"pk,auto"`
    AuthorID int64  `sorm:"fk:User.ID"`
    Author   *User  `sorm:"belongsTo:AuthorID"`
    Title    string
}
```

**2. Сгенерируйте типизированный слой:** `go generate ./...` — появится
пакет `sormgen` (компактный, читаемый в PR).

**3. Подключитесь** через адаптер драйвера:

```go
pool, _ := pgxpool.New(ctx, dsn)
db := pgxd.Wrap(pool)                      // PostgreSQL
// db := sqld.Wrap(sdb, lite.Dialect{})    // SQLite
// db := sqld.Wrap(sdb, my.Dialect{})      // MySQL
```

**4. Схема** — из кода или версионными файлами:

```go
migrate.Apply(ctx, sdb, "postgres")        // дифф моделей против БД, применить
```

```bash
sorm migrate diff -dev-dsn <пустая scratch-БД> add_users ./models  # файл в migrations/
sorm migrate up -dsn <прод>                                        # применить
```

**5. Запрашивайте и меняйте** — см. пример вверху; без трекинга:

```go
users, _ := sorm.Query[models.User](db).
    Where(u.Active.Eq(true), u.Posts.Any(p.Title.HasPrefix("Go"))).
    OrderBy(u.Email.Asc()).Limit(20).
    All(ctx)
```

## Примеры

- [`examples/blog`](examples/blog) — тур по всем возможностям (8 секций,
  от инспекции SQL до миграций из кода).
- [`examples/webapp`](examples/webapp) — боевой каркас: net/http API,
  PostgreSQL, три сгенерированные миграции, Dockerfile, Compose.

## Документация

- [Концепция и позиционирование](docs/concept.md)
- [Детальный дизайн](docs/design.md)
- [Анализ конкурентов](docs/competitive-analysis.md) и
  [gap-анализ против EF Core](docs/efcore-gap-analysis.md)

## Философия

sorm не прячет SQL — он убирает ручную бухгалтерию изменений. Принципы,
выведенные из чужих ошибок: никаких тихо отброшенных условий, ошибки только
как возвращаемые значения, обязательный `ctx`, иммутабельные билдеры,
`UPDATE` без `WHERE` не выполняется без явного `AllRows()`, предсказуемый
SQL с инспекцией через `ToSQL()`.

## Статус

Активная разработка, API может меняться до v1. Тесты гоняются на
PostgreSQL 17, MySQL 8 и SQLite на каждый коммит (CI), включая детектор гонок.
