// Package pg — диалект PostgreSQL.
package pg

import (
	"strconv"
	"strings"
)

type Dialect struct{}

func (Dialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

func (Dialect) QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
