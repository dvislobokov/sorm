// Package lite — диалект SQLite.
package lite

import "strings"

type Dialect struct{}

func (Dialect) Name() string           { return "sqlite" }
func (Dialect) Placeholder(int) string { return "?" }

// SQLite умеет RETURNING (3.35+), но через database/sql надёжнее общий с
// MySQL путь LastInsertId — меньше матрица поведения драйверов.
func (Dialect) ReturningSupported() bool { return false }

func (Dialect) QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
