# webapp — todo-API на sorm

Полный пример: `net/http`-хендлеры поверх sorm, PostgreSQL, три версионные
миграции, сгенерированные инструментом sorm, Dockerfile и Compose.

Что демонстрирует:

- **Unit of Work в хендлерах** — `POST /tasks/{id}/toggle`: `Track` → обычная
  мутация структуры → `SaveChanges`; конкурентное изменение ловится
  optimistic concurrency и превращается в `409 Conflict`.
- **Eager loading** — `GET /users` возвращает пользователей с задачами
  (split query, без N+1).
- **Динамическая композиция фильтров** — `GET /tasks?done=false&min_priority=2`
  собирает WHERE по частям; `done=false` — полноценное условие (zero-value
  не отбрасывается).
- **Миграции при старте** — приложение само применяет неприменённые файлы
  из `migrations/` (`migrate.Up`), история — в таблице `sorm_migrations`.

## Запуск

```bash
cd examples/webapp
docker compose up --build
```

Compose поднимает PostgreSQL (с healthcheck) и приложение; при старте
приложение применит три миграции и начнёт слушать `:8080`.

## Проверка

```bash
curl -s -X POST localhost:8080/users -d '{"name":"Alice","email":"alice@example.com"}'
curl -s -X POST localhost:8080/tasks -d '{"user_id":1,"title":"написать README","priority":2}'
curl -s -X POST localhost:8080/tasks -d '{"user_id":1,"title":"выпить кофе","priority":5}'

curl -s localhost:8080/users                          # пользователи с задачами
curl -s "localhost:8080/tasks?done=false&min_priority=3"

curl -s -X POST localhost:8080/tasks/1/toggle         # Unit of Work
curl -s "localhost:8080/tasks?done=true"
```

## Как были сгенерированы миграции

Файлы в `migrations/` — не рукописные: это реальные диффы схемы на трёх
шагах её эволюции. Для диффа sorm нужна **пустая scratch-БД** (replay
существующих миграций + сравнение с моделями). Поднимите её сами и
передайте DSN — sorm ничего не запускает за вас:

```bash
docker run -d --name scratch -e POSTGRES_PASSWORD=postgres -p 15433:5432 postgres:17-alpine
```

Дальше цикл «изменил модели → сгенерировал дифф» (из корня репозитория;
на каждый дифф нужна свежая пустая база — `CREATE DATABASE scratchN`):

```bash
# шаг 1: в models.go только User
go run ./cmd/sorm migrate diff -dialect postgres \
  -dir examples/webapp/migrations \
  -dev-dsn 'postgres://postgres:postgres@localhost:15433/scratch1' \
  init ./examples/webapp/models
# → 20260711200554_init.sql (CREATE TABLE users)

# шаг 2: добавили Task (hasMany/belongsTo, FK)
# → 20260711200614_add_tasks.sql (CREATE TABLE tasks + FK)

# шаг 3: добавили поле Task.Priority
# → 20260711200639_add_priority.sql (ALTER TABLE tasks ADD COLUMN priority)
```

После изменения моделей не забудьте перегенерировать типизированный слой:

```bash
go run ./cmd/sorm gen ./examples/webapp/models   # или go generate ./...
```

Те же операции доступны из кода (`sorm/migrate`): `Diff`, `Up`, `Pending` —
приложение в `main.go` использует `migrate.Up`.

## Структура

```
webapp/
├── main.go            HTTP-сервер: миграции при старте + хендлеры
├── models/models.go   схема (источник истины)
├── models/sormgen/    сгенерировано `sorm gen` (в VCS)
├── migrations/        сгенерировано `sorm migrate diff` (в VCS)
├── Dockerfile         multi-stage: golang:1.25 → distroless
├── compose.yaml       postgres + app
└── README.md
```

## Эволюция схемы в дальнейшем

1. Меняете `models/models.go`.
2. `go run ./cmd/sorm gen ./examples/webapp/models`.
3. `go run ./cmd/sorm migrate diff -dialect postgres -dev-dsn <пустая scratch-БД> <имя> ./examples/webapp/models`.
4. Ревью нового `.sql` в PR.
5. Деплой: приложение применит его при старте (или `sorm migrate up -dsn <прод>`).
