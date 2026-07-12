// Package my implements the MySQL/MariaDB dialect.
package my

import "strings"

type Dialect struct{}

func (Dialect) Name() string             { return "mysql" }
func (Dialect) Placeholder(int) string   { return "?" }
func (Dialect) ReturningSupported() bool { return false } // auto-PK via LastInsertId

func (Dialect) QuoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}
