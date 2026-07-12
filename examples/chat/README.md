# Chat — the sorm showcase

A small chat service that exercises the whole of sorm in a realistically
layered application:

```
examples/chat
├── cmd/chat/main.go        wiring: sconf → srog → pgx pool → InSchema →
│                           migrate.Apply + migrate.Seed → Echo
├── appsettings.yaml        configuration (overridable via CHAT_* env vars)
└── internal
    ├── config/             typed settings (sconf: YAML + env + flags)
    ├── models/             sorm entities + generated sormgen package
    ├── repository/         data access — two styles side by side:
    │   ├── users.go        via the generated Context (EF DbContext style)
    │   ├── rooms.go        via the Context + many2many Link/Unlink
    │   ├── messages.go     via low-level Query/Session/Project
    │   └── audit.go        append-only journal, transaction-bound
    ├── service/            business logic: RunInTx composes repositories,
    │                       audit commits atomically with the action
    └── transport/httpapi/  Echo handlers; sorm typed errors → HTTP codes
```

## What it demonstrates

| Feature | Where |
|---|---|
| Dedicated DB schema (`chat`) via `sorm.InSchema` | `cmd/chat/main.go` |
| Declarative migrations scoped to the schema | `migrate.Apply(..., WithSchema)` |
| One-time seed (`#general` room) | `migrate.Seed` in `main.go` |
| Generated `Context` (tracked reads, `Find`, `NoTracking`) | `repository/users.go` |
| Low-level `Query`/`Session`/`Track` | `repository/messages.go` |
| `RunInTx` + `With(tx)` repositories (atomic audit) | `service/chat.go` |
| Typed JSON documents + generated accessors (`PrefsDoc.Theme.Eq`) | `models.UserPrefs`, `repository/users.go` |
| Schemaless JSON maps | `User.Meta`, `AuditLog.Details` |
| Native PG arrays (`Roles.Has("moderator")`) | `models.User`, `repository/users.go` |
| Custom Valuer/Scanner scalar (`Cents`) | `models.Cents` |
| uuid.UUID primary key | `models.ApiToken` |
| Optimistic concurrency → HTTP 409 | `Message.Version`, `httpapi.httpError` |
| Auto-timestamps | `autoCreate`/`autoUpdate` on all entities |
| Composite + custom (DESC) indexes | `Message`, `AuditLog.Indexes()` |
| many2many, hasMany, belongsTo, nullable self-FK (threads) | `models.go` |
| GROUP BY projection (room stats) | `repository/messages.go` |
| Set-based UPDATE (ban, soft delete) | `users.go`, `messages.go` |
| Query naming for observability | `.Named("messages.page")` |

## Run

```bash
docker compose up -d db          # or any PostgreSQL with a "chat" database
go run ./cmd/chat
```

Try it:

```bash
curl -s localhost:8080/api/users -d '{"email":"neo@io","name":"Neo","roles":["moderator"]}' -H 'Content-Type: application/json'
curl -s localhost:8080/api/rooms -d '{"slug":"dev","title":"Dev talk","owner_id":2}' -H 'Content-Type: application/json'
curl -s localhost:8080/api/rooms/dev/messages -d '{"author_id":2,"text":"hello","payload":{"kind":"text","mentions":["system"]}}' -H 'Content-Type: application/json'
curl -s 'localhost:8080/api/rooms/dev/messages?limit=10'
curl -s localhost:8080/api/rooms/dev/stats
curl -s localhost:8080/api/audit
```

Configuration is layered (later wins): `appsettings.yaml` → `CHAT_*` env
vars (`CHAT_DB__DSN`, `CHAT_HTTP__ADDR`, `CHAT_LOG__LEVEL`) → CLI flags.
