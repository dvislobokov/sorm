# Анализ конкурентов: ORM и инструменты работы с БД в Go

*Состояние экосистемы на середину 2026 года. Данные о звёздах и активности — GitHub API, июль 2026.*

Экосистема делится на два лагеря по фундаментальному выбору:

- **Runtime/reflection-based** — GORM, Bun, sqlx, XORM: маппинг через рефлексию во время выполнения.
- **Codegen-based** — Ent, sqlc, SQLBoiler, Jet, GORM Gen: генерация типизированного кода на этапе сборки.

---

## Часть 1. Reflection-based

### GORM (~39.9k★, активен)

Самая используемая ORM в Go: дефолт в туториалах, крупнейшая экосистема плагинов.

**Сильные стороны**
- Максимальная полнота фич: ассоциации (has-one/has-many/many2many, полиморфные), `Preload`/`Joins`, хуки, soft delete, optimistic locking, `DBResolver` (read/write splitting), шардинг, `CreateInBatches`, `FindInBatches`.
- Самый быстрый старт: крупнейшее комьюнити, больше всего ответов на Stack Overflow.
- Модернизация 2025: generics API `gorm.G[User](db).Where(...).First(ctx)` — возвращает `(T, error)`, обязательный `ctx`, чинит переиспользование builder'а. Плюс два слоя кодогена: `gorm.io/gen` и новый `go-gorm/cli`.

**Слабые стороны**
- **Zero-value footgun — самая цитируемая причина реальных багов.** Struct-условия тихо отбрасывают поля с нулевыми значениями:

  ```go
  db.Where(&User{Name: "John", Age: 0}).Find(&users) // условие Age молча выброшено
  db.Where(&Example{Value: false}).Find(&ex)          // WHERE по false игнорируется (issue #6232)
  db.Model(&u).Updates(User{Active: false})           // Active=false НЕ запишется
  ```

  Документированный «фикс» — отказаться от типобезопасности и писать `map[string]any`.
- **Дизайн ошибок**: ошибки едут в поле `.Error` цепочки — легко забыть проверить. `First()` возвращает `ErrRecordNotFound`, а `Find()` — nil с пустым слайсом; `errors.Is` срабатывает только на последнюю ошибку цепочки (issue #3993).
- **SQL pollution**: переиспользование chained `*gorm.DB` без `Session()` протаскивает условия между запросами.
- **Производительность**: рефлексия на *каждый* запрос → ~2–2.5x медленнее pgx/raw на чтении, ~3x на скане 10k строк (efectn/go-orm-benchmarks).
- **AutoMigrate непригоден для продакшена**: не удаляет колонки, нет версионирования, допускает lossy-изменения типов, длинные блокировки таблиц (issues #5807, #5635). Стандартный совет — брать golang-migrate/goose/Atlas отдельно.
- Незагруженные relations — просто nil без ошибки (забыл `Preload` → тихо пустые данные); `gorm.DeletedAt` неявно добавляет `WHERE deleted_at IS NULL` во все запросы.
- Имена колонок строками (`db.Order("age desc")`) — опечатки ловятся только в рантайме.

### Bun (uptrace/bun, ~4.9k★, активен)

Преемник go-pg. SQL-first query builder.

**Сильные стороны**
- Builder зеркалит SQL 1:1 — запросы предсказуемы, всегда можно вставить raw-фрагмент.
- Рефлексия **один раз при регистрации модели** → ~5–10% над raw pgx на чтении.
- Обязательный `ctx` в каждом финишере; ошибки — обычные возвращаемые значения (`sql.ErrNoRows`).
- Настоящие версионные миграции (`bun/migrate`), bulk insert/update, `CopyFrom`, OpenTelemetry из коробки. Диалекты: PG/MySQL/MSSQL/SQLite/Oracle.

**Слабые стороны**
- **Relations**: has-many выполняется **двумя SELECT'ами, не JOIN'ом** (issue #604) — нельзя отфильтровать родителей по колонкам детей; has-many с pointer-родителем или nullable-колонками падает («has-many relation does not have base model with id», issues #513, #884, #950).
- Нулевая compile-time проверка: `Where("u.status = ?", ...)` — строки; relations конфигурируются строками в тегах `bun:"rel:has-many,join:id=user_id"`.
- Нет кодогена, маленькое комьюнити, пробелы в документации по сложным relations.

### sqlx (~17.7k★, **заморожен с августа 2024**)

Минимальное расширение `database/sql`: `Get`/`Select`/`StructScan`, именованные параметры, `In()`.

**Сильные стороны**: скорость ≈ raw, нулевая магия, безболезненное внедрение в легаси (типы встраивают stdlib), огромная установленная база.

**Слабые стороны**
- Весь SQL и вся обвязка — вручную; никаких relations, миграций, батчинга.
- **Хроническая боль — сканирование JOIN'ов**: `StructScan` только плоский; вложенные структуры → «scannable dest type struct with >1 columns» (issues #112, #618, открыты с ~2014); NULL в LEFT JOIN → `unsupported driver Scan pair: <nil> -> *string`.
- Maintenance-риск: коммитов нет с 2024 (issue #878 «Is project dead?»), комьюнити рекомендует sqlc вместо него.

### XORM (~6.6k★ архивное зеркало; живёт на gitea.com)

Жив фактически только благодаря Gitea. Переезд GitHub→Gitea развалил комьюнити; та же per-query рефлексия и те же zero-value footguns, что у GORM (принудительная запись нулевых значений — через `Cols()`), без экосистемы GORM. Рекомендуем рассматривать только в орбите Gitea.

---

## Часть 2. Codegen-based

### Ent (entgo.io, ~17.1k★, активен, ~620 открытых issues)

Schema-as-code фреймворк от Meta: сущности описываются в Go, `ent generate` создаёт типизированный клиент.

```go
func (User) Edges() []ent.Edge {
    return []ent.Edge{edge.To("pets", Pet.Type)}
}
// графовый обход с полной типобезопасностью:
pets := client.User.Query().Where(user.NameEQ("a8m")).QueryPets().AllX(ctx)
```

**Сильные стороны**
- Схема в Go, проходит code review, из неё растёт всё: валидация, хуки, privacy-политики (row-level авторизация в схеме!), GraphQL/gRPC-интеграции.
- Графовый обход relations — лучший в классе для сложных доменных моделей.
- **Лучшая история миграций в Go**: интеграция с Atlas — versioned-миграции по диффу схемы, lint, `atlas.sum`.
- Hooks + interceptors + privacy — редкий для Go набор middleware-слоёв.

**Слабые стороны**
- **Гигантский объём сгенерированного кода**: несколько сущностей → десятки тысяч строк; медленный `go build`, раздутые диффы.
- **Неоптимальный SQL**: избыточно сложные запросы даже для простых SELECT'ов (пост «Stop using entgo...please»); eager loading рёбер — серией запросов, не JOIN'ами.
- Крутая кривая обучения: edges vs FK, hooks vs interceptors, система шаблонов кодогена — отдельная ментальная модель поверх SQL.
- Трение при escape в raw SQL (CTE, window functions, JSONB) — через `Modify()` с борьбой против фреймворка.
- Растущая привязка к Atlas Cloud (часть фич за логином).

### sqlc (~18.0k★, очень активен; обогнал Ent по звёздам)

SQL-first: пишешь настоящий SQL с аннотациями, sqlc проверяет его против схемы и генерирует типизированные функции.

```sql
-- name: GetAuthor :one
SELECT * FROM authors WHERE id = $1;
```

**Сильные стороны**
- Настоящий SQL без DSL: CTE, window functions, `jsonb`, LATERAL — всё работает.
- Compile-time проверка самого SQL против схемы: опечатка в колонке — ошибка генерации, не рантайма.
- Почти нулевой overhead поверх pgx; маленький читаемый сгенерированный код.
- Дефолтная рекомендация комьюнити («the figurative Correct Answer» — brandur.org).

**Слабые стороны**
- **Динамические запросы — ахиллесова пята** (Discussion #364, Issue #3414, открыты годами). Опциональные фильтры вынуждают паттерн:

  ```sql
  WHERE (sqlc.narg('name')::text IS NULL OR name = sqlc.narg('name'))
    AND (sqlc.narg('min_age')::int IS NULL OR age >= sqlc.narg('min_age'))
  ```

  который **ломает использование индексов планировщиком**. Стандартный выход — тащить второй query builder (squirrel/Jet) для «динамических 10%», т.е. две системы запросов в одном кодбейзе.
- Регенерация на каждое изменение запроса; миграций нет (BYO); дрейф между миграциями и `schema.sql` — вручную.
- JOIN'ы во вложенные структуры — неуклюжий `sqlc.embed()`; nullable-колонки → шум `pgtype.Text`/`sql.NullString`.
- PG-поддержка сильно впереди MySQL/SQLite.

### SQLBoiler (~7.0k★, **maintenance mode с конца 2024**)

Database-first: интроспекция живой БД → полная ORM под конкретную схему. Исторически топ бенчмарков, зрелые relations. Но README прямо говорит: новые фичи не принимаются, issues обычно не решаются, смотрите **Bob** или sqlc. Для greenfield в 2026 — дисквалификация. Query-mods строками (`qm.Where("age > ?", 21)`) — без compile-time проверки. Наследник **Bob** (type-safe query mods, ~1.7k★) — куда идёт энергия, но ещё молод.

### Jet (go-jet, ~3.7k★, активен, ~42 открытых issue)

Database-first генератор **типобезопасного SQL builder DSL** (не ORM).

```go
stmt := SELECT(Actor.AllColumns, Film.AllColumns).
    FROM(Actor.INNER_JOIN(FilmActor, FilmActor.ActorID.EQ(Actor.ActorID))).
    WHERE(Film.Length.GT(Int(120)))
var dest []struct{ model.Actor; Films []model.Film }
err := stmt.Query(db, &dest) // автоматический маппинг во вложенные структуры
```

**Сильные стороны**
- **Решает ровно то, что не может sqlc**: условия — значения, композиция `if filter != nil { conds = conds.AND(...) }` с полной типобезопасностью (сравнение int-колонки со строкой — ошибка компиляции).
- Автоматическое сканирование сложных JOIN'ов во вложенные графы структур одним запросом.
- Почти полная поверхность SQL (CTE, window, set operations, locking); PG/MySQL/MariaDB/SQLite.

**Слабые стороны**
- Многословность: `Int(120)`, `String("x")` — обёртки повсюду.
- **Сканер результатов работает только с `database/sql`, не с pgx** — теряется флагманская фича на стандартном для комьюнити драйвере.
- Маленькое комьюнити, фактически один мейнтейнер (bus-factor), кодоген требует живой БД.

### GORM Gen (gorm.io/gen, ~2.6k★)

Кодоген-DAO поверх GORM: `u.WithContext(ctx).Where(u.Age.Gt(18)).Find()` — типобезопасно. Но внутри — весь рантайм GORM (рефлексия, callbacks, его профиль производительности); два слоя магии при отладке; дублирование моделей конфликтует с clean architecture; документация тонкая. Side-project GORM.

---

## Сводная матрица

| | GORM | Bun | sqlx | Ent | sqlc | SQLBoiler | Jet |
|---|---|---|---|---|---|---|---|
| Звёзды (2026) | 39.9k | 4.9k | 17.7k | 17.1k | 18.0k | 7.0k | 3.7k |
| Статус | активен | активен | **заморожен** | активен | очень активен | **maintenance** | активен |
| Источник истины | Go-структуры | Go-структуры | — | Go-схема | SQL-файлы | живая БД | живая БД |
| Типобезопасные запросы | нет (gen/cli — да) | нет | нет | да | да (сам SQL) | частично | да |
| Динамические запросы | да | да | вручную | хорошо | **плохо** | ок | **отлично** |
| Оverhead vs raw | 2–3x | 5–15% | ~0 | средний, SQL неоптимален | ~0 | ~0 | низкий |
| Миграции | AutoMigrate (не прод) | версионные | нет | **Atlas (лучшие)** | BYO | BYO | BYO |
| Change tracking / UoW | нет | нет | нет | нет | нет | нет | нет |

## Где сидит консенсус комьюнити (2025–2026)

- **Дефолтный совет r/golang и HN для Postgres: «просто возьми sqlc + pgx»** (+ golang-migrate/Atlas/goose). Логика: культура Go ценит явность, SQL — сам по себе абстракция, pgx кратно быстрее рефлексивных стеков.
- Сентимент «avoid ORMs» целится в *runtime*-ORM (прежде всего GORM); Ent получает частичный пропуск за типобезопасность, но критикуется за вес и качество SQL.
- **Трещина в консенсусе — динамическая фильтрация**: повторяющийся ответ «sqlc для 90% + Jet/squirrel/Bob для динамических 10%» означает две системы запросов. Jet — ответ «один инструмент», но с оговорками по pgx и комьюнити.
- Смерть sqlx и SQLBoiler усилила миграцию к sqlc/Bob; при этом GORM остаётся самым используемым на практике — **консенсус и adoption расходятся**.
- **Ни один инструмент в экосистеме не даёт change tracking / Unit of Work** — см. [gap-анализ против EF Core](efcore-gap-analysis.md).

## Источники

[Why GORM Is Overrated (jsnfwlr, 2025)](https://jsnfwlr.com/blog/2025/03/30/why-gorm-is-overrated/) · [GORM #6232](https://github.com/go-gorm/gorm/issues/6232) · [GORM #5807](https://github.com/go-gorm/gorm/issues/5807) · [GORM #3993](https://github.com/go-gorm/gorm/issues/3993) · [GORM Generics](https://gorm.io/docs/the_generics_way.html) · [go-gorm/gen](https://github.com/go-gorm/gen) · [go-gorm/cli](https://github.com/go-gorm/cli) · [Bun #513](https://github.com/uptrace/bun/issues/513), [#604](https://github.com/uptrace/bun/issues/604), [#884](https://github.com/uptrace/bun/issues/884), [#950](https://github.com/uptrace/bun/issues/950) · [sqlx #112](https://github.com/jmoiron/sqlx/issues/112), [#162](https://github.com/jmoiron/sqlx/issues/162), [#618](https://github.com/jmoiron/sqlx/issues/618), [#878](https://github.com/jmoiron/sqlx/issues/878) · [efectn/go-orm-benchmarks](https://github.com/efectn/go-orm-benchmarks) · [dasroot: GORM/sqlx/pgx compared (2025)](https://dasroot.net/posts/2025/12/go-database-patterns-gorm-sqlx-pgx-compared/) · [encore.dev Go ORMs](https://encore.dev/articles/go-orms) · [brandur.org/sqlc](https://brandur.org/sqlc) · [brandur: sqlc 2024 check-in](https://brandur.org/fragments/sqlc-2024) · [sqlc #364](https://github.com/sqlc-dev/sqlc/discussions/364), [#3414](https://github.com/sqlc-dev/sqlc/issues/3414) · [dizzy.zone: sqlc dynamic queries](https://dizzy.zone/2024/07/03/SQLC-dynamic-queries/) · [HN о sqlc](https://news.ycombinator.com/item?id=45480816) · [Stop using entgo…please](https://dev.to/shandoncodes/stop-using-entgoplease-5gm5) · [entgo versioned migrations](https://entgo.io/docs/versioned-migrations/) · [SQLBoiler maintenance #903](https://github.com/aarondl/sqlboiler/discussions/903) · [go-jet](https://github.com/go-jet/jet) · [awesome-go-sql: jet notes](https://pkg.go.dev/github.com/veqryn/awesome-go-sql/cmd/jet) · [gorm.io/gen](https://gorm.io/gen/index.html) · [xorm на Gitea](https://gitea.com/xorm/xorm)
