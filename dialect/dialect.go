// Package dialect определяет минимальную поверхность различий между СУБД.
// MVP реализует только PostgreSQL (dialect/pg); MySQL и SQLite добавляются
// реализацией этого интерфейса без изменения рантайма.
package dialect

// Dialect отвечает за текстовые различия SQL; стратегия батчирования живёт
// в драйверных адаптерах.
type Dialect interface {
	// Name — имя диалекта: "postgres", "mysql", "sqlite".
	Name() string
	// Placeholder возвращает плейсхолдер n-го аргумента (нумерация с 1): $1 или ?.
	Placeholder(n int) string
	// QuoteIdent экранирует идентификатор (таблицу, колонку).
	QuoteIdent(s string) string
	// ReturningSupported — INSERT ... RETURNING доступен (PG; MySQL — нет,
	// SQLite — намеренно LastInsertId-путь, общий с MySQL).
	ReturningSupported() bool
}
