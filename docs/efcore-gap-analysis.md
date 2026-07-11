# Gap-анализ: EF Core против Go-экосистемы

*Возможности EF Core 8–10 против Go-библиотек (GORM, Ent, Bun, sqlc, SQLBoiler, Bob, go-jet, Atlas, goose/golang-migrate) на середину 2026. Оценка реализуемости — для Go 1.23+/1.24: generics, range-over-func итераторы, **нет** expression trees и runtime-кодогенерации.*

## Почему EF Core — эталон

EF Core — это *замкнутый цикл*: одна C#-модель управляет запросами (LINQ → SQL), записью (change tracking → батчированный DML) и эволюцией схемы (диф модели → миграции). Всё это стоит на двух механизмах, которых нет в Go: **expression trees** (компилятор превращает лямбды в данные, которые провайдер транслирует в SQL) и **stateful DbContext** (identity map + snapshot change tracker). Ни то, ни другое буквально не воспроизводимо в Go — но, как доказывают Rust (Diesel, SeaORM) и TypeScript (Prisma, Drizzle, MikroORM), большинство *результатов* достижимы через кодогенерацию.

## Таблица разрывов

| # | Возможность EF Core | Лучший Go-аналог сегодня | Разрыв | Реализуемо в Go? Ближайший дизайн |
|---|---|---|---|---|
| 1 | LINQ: compile-time-проверенные, свободно композируемые запросы → SQL | Ent (генерируемые предикаты), go-jet и Bob (типизированные builders), sqlc (типизированный код из raw SQL) | **Большой.** Никто не транслирует обычный Go-код в SQL; композиция проекций/подзапросов слабее | Да, кроме «пиши обычный Go». Кодоген типизированного DSL — проверенный путь (Diesel, Drizzle, Ent). Статический транслятор Go-замыканий (`go/ssa`) теоретически возможен, но никто не сделал |
| 2 | Change tracking, Unit of Work, `SaveChanges` с авто-диффом, `AsNoTracking` | По сути ничего. GORM `Save` пишет все поля; `Updates` требует явных map; freerware/work — ручная оболочка UoW | **Крупнейший разрыв.** Нет identity map, нет snapshot-диффа, нет автоупорядоченного flush | Да. Generics + кодоген делают snapshot-дифф дешёвым: генерируем `Snapshot()`/`Diff(old)` на сущность, держим `map[key]snapshot` в объекте-сессии. Прокси невозможны (нет runtime IL), но дефолт самого EF — snapshot, не прокси |
| 3 | Миграции: диф модели, скаффолдинг кода миграций, идемпотентные скрипты | **Почти паритет через Atlas** (декларативный диф + версионные файлы, lint, dev-database); Ent интегрирует нативно. GORM AutoMigrate — только аддитивный, небезопасен | Малый — Atlas местами *превосходит* EF (CI-lint, drift detection) | Решено. Единственная область, где Go уже на уровне или лучше EF Core |
| 4 | Навигационные свойства, Include/ThenInclude, lazy/explicit loading, split queries | Ent `WithPets(func(q){q.WithToys()})` = Include/ThenInclude как split queries; Bun/GORM `Relation`/`Preload` | Средний. Eager — нормально; **lazy loading невозможен** (нужны прокси); выбор JOIN vs split почти нигде не настраивается | Eager: решено. Lazy: только explicit loading (`user.QueryPets().All(ctx)`, есть в Ent). Терпимо — lazy loading и в EF считается антипаттерном |
| 5 | Глобальные фильтры запросов (soft delete, multi-tenancy) | GORM `DeletedAt` (только soft delete, захардкожен); Ent interceptors + privacy — реально сопоставимо; Bun `?soft_deletes` | Малый-средний. Ent покрывает; остальные — только soft delete | Да — middleware/interceptors на уровне builder'а, естественный Go-дизайн |
| 6 | Owned types, value converters, shadow properties | Конвертеры: `sql.Scanner`/`driver.Valuer` идиоматичны; owned ≈ Ent-embedded/JSON или Bun `embed:`. Shadow properties: нет | Малый для конвертеров; shadow properties концептуально невозможны (структуры Go фиксированы) — обход: генерируемые скрытые поля | Да, кроме shadow properties, ценность которых без трекера мала |
| 7 | Compiled queries / кэш планов запросов | EF кэширует трансляцию по форме expression tree; Go-builders рендерят SQL каждый раз. Код sqlc/Ent фактически «прекомпилирован» | Малый на практике — сборка строк дёшева; `database/sql` кэширует prepared statements | Да: кодоген уже даёт эквивалент; `sync.Map` отрендеренного SQL — тривиально |
| 8 | Батчированный `SaveChanges` (один roundtrip, упорядочен, FK-aware) | GORM/Bun батчируют только *insert* (`CreateInBatches`, bulk VALUES); никто не батчирует гетерогенные insert/update/delete с упорядочением по зависимостям | **Большой** — зависит от разрыва №2 | Да, как только есть дифф: топосорт по FK-графу + multi-statement batch или `pgx.Batch`. Вся информация доступна на этапе кодогена |
| 9 | Interceptors / диагностика | Ent hooks (мутации) + interceptors (запросы); GORM callbacks; Bun query hooks; OTel-плагины везде | Малый. Покрытие уже (EF перехватывает и соединения, и транзакции, и SaveChanges), но паттерн устоялся | Решено по духу |
| 10 | Optimistic concurrency tokens | GORM-плагин optimisticlock; Ent вручную; Bun вручную `WHERE version = ?` | Средний — работает, но opt-in и без enforcement; EF кидает `DbUpdateConcurrencyException` автоматически на каждый tracked update | Да: генерируемые UPDATE всегда добавляют version-предикат + проверку rows-affected. Бесшовность требует change tracking |
| 11 | Транзакции + execution strategies (авторетраи transient-ошибок) | Retry-хелперы pgx, cenkalti/backoff вручную; ни одна ORM не даёт EF-стиль `EnableRetryOnFailure` с осознанием границ транзакции | Средний | Да, легко: `RunInTx(ctx, opts{Retry: ...})` + классификация transient-ошибок по драйверу. Чисто вопрос зрелости библиотек |
| 12 | Set-based bulk (`ExecuteUpdate`/`ExecuteDelete`) | Полностью покрыто: любой builder делает `UPDATE ... WHERE` нативно | **Нет разрыва.** Go никогда не был entity-центричным — это всегда был дефолт | Решено — по иронии, EF добавил в v7 то, что в Go было всегда |
| 13 | JSON-колонки: маппинг, типизированные запросы внутрь, частичные апдейты (EF10) | Ent JSON-поля с типизированными предикатами (`sqljson`), GORM `datatypes.JSON`, Bun `json:` | Средний. Маппинг ок; *типизированный drill-in и частичные JSON-апдейты* слабее и строково-типизированы | Да: кодоген может выдавать типизированные аксессоры на поле JSON-схемы (стиль Drizzle); Ent `sqljson` — полпути |
| 14 | Temporal tables (`TemporalAsOf` и т.д.) | Ничего первоклассного; Atlas может DDL; запросы — raw SQL; enthistory/gorm-loggable эмулируют историю | Средний, нишевый | Да: аддитивные методы (`AsOf(time)` → `FOR SYSTEM_TIME AS OF`) — препятствий нет, нет спроса |

## Три разрыва, которые действительно имеют значение

### 1. Язык запросов (LINQ) — структурный, но хорошо замещаемый

EF транслирует *обычную семантику C#* — `.Where(o => o.Customer.Orders.Any(x => x.Total > 100))` — потому что компилятор отдаёт провайдеру expression tree. Go-замыкания — непрозрачный машинный код; аналога `Expression<Func<...>>` нет, и рефлексия не заглядывает внутрь тела функции.

Ответ экосистемы, сходящийся с Rust и TypeScript, — **кодогенерация по схеме**:

- **Ent** генерирует типизированные предикаты и шаги графового обхода — ближе всех к композируемости LINQ.
- **go-jet / Bob** генерируют typed SQL AST builders в духе Drizzle/Diesel: `SELECT(User.Name).WHERE(User.Age.GT(Int(18)))` — проверенные компилятором колонки и типы сравнений.
- **sqlc** инвертирует задачу (SQL → типизированный Go); отличная типобезопасность, нулевая композируемость.

Это в точности повторяет решения языков без expression trees: Diesel кодирует запрос в системе типов через трейты и кодоген; Drizzle — TS-inference над объектом схемы; Prisma генерирует клиент из DSL-файла. **Вердикт:** compile-time-проверенные запросы полностью достижимы и в основном существуют; недостижимо — *писать запросы как обычный Go по «как будто in-memory» коллекциям* и свободная композиция проекций в произвольные формы (отсутствие анонимных типов в Go бьёт именно сюда — каждой проекции нужна именованная/генерируемая структура).

### 2. Change tracking + Unit of Work — настоящая дыра

Крупнейший подлинный разрыв. Ни одна мейнстримная Go-библиотека не даёт: загрузил граф, свободно мутировал структуры, вызвал `SaveChanges` — получил минимальный, упорядоченный, батчированный набор DML в одной транзакции/roundtrip'е. `Save` в GORM переписывает все колонки; частичные апдейты требуют ручного перечисления полей — классический источник багов, который EF устраняет. Go-комьюнити полурационализирует это культурно («репозиторий знает, что изменилось»), но это разрыв возможностей, а не только философии.

**Это реализуемо.** Дефолтный трекер EF — snapshot-based, без прокси и IL:

- Кодоген (в стиле Ent) выдаёт per-entity snapshot-структуры и быстрый пополевой `Diff` (без рефлексии в рантайме).
- Generic-сессия `Tracker[T]` держит identity map `map[PK]*T` плюс снапшоты; `Flush(ctx)` топологически сортирует по FK-зависимостям и выдаёт батчированные statement'ы (`pgx.Batch` даёт один roundtrip на Postgres).
- `AsNoTracking` тривиально становится дефолтом, tracking — opt-in: возможно, дефолт даже лучше, чем у EF.
- Альтернатива — `ActiveModel` из SeaORM (каждое поле — ячейка `Unchanged/Set`), без сессий вовсе, хорошо ложится на генерируемые сеттеры; MikroORM доказывает, что и полная session-модель работает в рантайме без IL.

Невозможно: прозрачные lazy-прокси и перехват прямых записей в поля (в Go нет property-аксессоров). Мутация — либо через генерируемые сеттеры (стиль SeaORM), либо O(полей) сравнение снапшотов при flush (стиль EF — ровно так EF и работает). **Никто не построил это в Go — самая ценная незанятая территория.**

### 3. Батчинг SaveChanges, concurrency, retries — следствия №2

Батчинг гетерогенной записи, автоматический enforcement concurrency-токенов и детекция конфликтов в стиле `DbUpdateConcurrencyException` становятся механикой, как только есть трекер: дифф знает изменённые колонки и исходное значение версии; rows-affected ≠ ожидаемому → ошибка конфликта с данными из БД для разрешения. Execution strategies (retry с повтором транзакции) — маленькая независимая от трекера библиотека, которую просто никто не стандартизировал.

## Где Go уже на уровне или впереди

- **Миграции**: Atlas — движок диффа модели с версионным скаффолдингом, идемпотентными lint-проверенными планами, drift detection и «dev database» для валидации — по ряду пунктов надмножество `dotnet ef migrations add`.
- **Bulk set-based операции**: нативны для любого Go query-слоя; EF догнал только в v7.
- **Interceptors/hooks/диагностика**: стек hooks+interceptors+privacy в Ent — достойный аналог EF interceptors и даже глобальных фильтров.
- **Сырая производительность и явность**: пути sqlc/pgx обгоняют EF-стиль материализации на горячих участках.

## Итоговый scorecard

| Возможность | Статус в Go |
|---|---|
| Типизированный query DSL (кодоген) | ✅ Хорошо (Ent, Jet, Bob, sqlc) |
| Трансляция «обычного кода» в SQL | ❌ Невозможно без кодогена/статанализа; никто не сделал |
| Change tracking / авто-дифф SaveChanges | ❌ Нет; **реализуемо** через генерируемый snapshot-дифф + generic tracker |
| Identity map / Unit of Work | ❌ Нет; реализуемо |
| Батчированный гетерогенный flush, 1 roundtrip | ❌ Нет (только inserts); реализуемо через pgx.Batch + топосорт FK |
| Миграции по диффу модели | ✅ Паритет+ (Atlas) |
| Eager loading / Include-цепочки / split queries | ✅ Хорошо (Ent, Bun) |
| Lazy-прокси | ❌ Невозможно; explicit loading есть |
| Глобальные фильтры запросов | 🟡 Ent — да; остальные — только soft delete |
| Value converters / owned types | ✅ / 🟡 Scanner/Valuer идиоматичны; owned — через JSON/embedding |
| Shadow properties | ❌ Невозможно как таковые; без трекера малоценны |
| Compiled/кэшированные запросы | 🟡 Неактуально — кодоген и есть прекомпиляция |
| Optimistic concurrency | 🟡 Плагины/вручную; автоматика требует трекера |
| Retry execution strategies | 🟡 Только DIY; легко построить |
| ExecuteUpdate/Delete | ✅ Нативно везде |
| JSON-маппинг + типизированный drill-in | 🟡 Маппинг да; типизированные JSON-предикаты частично (Ent sqljson) |
| Temporal tables | ❌ Только raw SQL; легко, но не построено |

**Итог:** Go никогда не воспроизведёт *механизм* EF Core (expression trees, runtime-прокси), но может воспроизвести **~85% поверхности возможностей** через кодогенерацию в стиле Ent — и для запросов, загрузки и миграций уже воспроизвёл. Единственный целостный отсутствующий продукт — **генерируемый snapshot-диффящий change tracker с батчированным, concurrency-aware flush** (синтез «Ent + SeaORM ActiveModel + pgx.Batch»). Всё необходимое — generics для трекера, кодоген для zero-reflection диффов, batch-протоколы драйверов — есть в Go 1.24 уже сегодня; это просто никто не собрал, отчасти потому, что SQL-first-культура комьюнити этого не требовала.

## Источники

[What's New in EF Core 10 — Microsoft Learn](https://learn.microsoft.com/en-us/ef/core/what-is-new/ef-core-10.0/whatsnew) · [EF Core 10 ExecuteUpdate improvements](https://codesimple.dev/blogPost/5f897318-2559-4d5a-b488-945c8eb6362a/ef-core-10-major-improvements-on-executeupdate) · [Partial JSON updates in EF 10](https://jaliyaudagedara.blogspot.com/2025/10/ef-core-100-support-for-partially.html) · [Comparing the best Go ORMs — Encore](https://encore.cloud/resources/go-orms) · [Ent versioned migrations / Atlas](https://entgo.io/docs/versioned-migrations/) · [Bob ORM](https://github.com/stephenafamo/bob) · [sqlc vs GORM vs sqlx (2026)](https://reintech.io/blog/sqlc-vs-gorm-vs-sqlx-go-database-libraries-compared-2026) · [freerware/work (Go UoW)](https://github.com/freerware/work) · [Repositories, transactions, and UoW in Go — rednafi](https://rednafi.com/go/repo-txn-uow/) · [MikroORM Unit of Work](https://mikro-orm.io/docs/unit-of-work)
