# Бенчмарки: sorm vs GORM vs Ent vs raw database/sql

Отдельный Go-модуль — зависимости GORM/Ent не попадают в go.mod библиотеки.

СУБД — SQLite in-memory на pure-Go драйверах (без cgo и сети): меряется
overhead библиотек, а не база. Одинаковая таблица, одинаковые данные.

## Запуск

```bash
cd benchmarks
go generate ./models                                  # sormgen
go run -mod=mod entgo.io/ent/cmd/ent generate ./ent/schema  # ent client
go test -bench . -benchmem
```

## Результаты (Windows, go1.25, SQLite in-memory, 2026-07-12)

| Сценарий | sorm | GORM | Ent | raw |
|---|---|---|---|---|
| **Query 1000 строк**, мкс/оп | **565** | 1039 | 706 | 517 |
| — аллокаций/оп | **8 788** | 13 788 | 13 821 | 7 770 |
| **Insert 100 строк** (bulk), мкс/оп | **466** | 470 | 550 | — |
| **Update одного поля**, мкс/оп | **7.3** | 10.8 | 17.1 | — |
| — аллокаций/оп | **39** | 56 | 107 | — |

Выводы:

- **Чтение**: sorm в пределах ~9% от raw `database/sql` (кодоген-сканеры без
  рефлексии), в 1.8× быстрее GORM и 1.25× быстрее Ent, с наименьшим числом
  аллокаций среди ORM.
- **Bulk insert**: паритет с GORM (оба используют multi-row VALUES), быстрее
  Ent. При этом сценарий sorm — полный Unit of Work (`Add` × 100 →
  `SaveChanges`: снапшоты, топосорт, транзакция), а не голый `Create`.
- **Точечный update**: sorm быстрее обоих — дифф по снапшоту дешевле
  (26 нс, 0 аллокаций), UPDATE несёт только изменённые колонки.

Сценарий UpdateOne у sorm дополнительно включает optimistic concurrency
(version-предикат) — у GORM/Ent в этих замерах его нет.

Замечания по честности: у каждой библиотеки — её идиоматичный API
(gorm.Create batch, ent.CreateBulk, sorm.Session); логгер GORM отключён;
один сид и одна форма таблицы для всех.
