// Package my — диалект MySQL/MariaDB.
package my

import "strings"

type Dialect struct{}

func (Dialect) Name() string             { return "mysql" }
func (Dialect) Placeholder(int) string   { return "?" }
func (Dialect) ReturningSupported() bool { return false } // auto-PK через LastInsertId

func (Dialect) QuoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}
