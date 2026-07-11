// Package dialect определяет минимальную поверхность различий между СУБД.
// MVP реализует только PostgreSQL (dialect/pg); MySQL и SQLite добавляются
// реализацией этого интерфейса без изменения рантайма.
package dialect

// Dialect отвечает только за текстовые различия SQL. Стратегия батчирования
// и RETURNING-семантика живут в драйверных адаптерах.
type Dialect interface {
	// Placeholder возвращает плейсхолдер n-го аргумента (нумерация с 1): $1 или ?.
	Placeholder(n int) string
	// QuoteIdent экранирует идентификатор (таблицу, колонку).
	QuoteIdent(s string) string
}
