// Package lite implements the SQLite dialect.
package lite

import "strings"

type Dialect struct{}

func (Dialect) Name() string           { return "sqlite" }
func (Dialect) Placeholder(int) string { return "?" }

// SQLite supports RETURNING (3.35+), but through database/sql the
// LastInsertId path shared with MySQL is more reliable — a smaller
// matrix of driver behaviors.
func (Dialect) ReturningSupported() bool { return false }

func (Dialect) QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
