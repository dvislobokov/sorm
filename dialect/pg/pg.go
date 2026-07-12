// Package pg implements the PostgreSQL dialect.
package pg

import (
	"strconv"
	"strings"
)

type Dialect struct{}

func (Dialect) Name() string             { return "postgres" }
func (Dialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (Dialect) ReturningSupported() bool { return true }

func (Dialect) QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
