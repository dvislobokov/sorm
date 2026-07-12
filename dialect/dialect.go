// Package dialect defines the minimal surface of differences between databases.
// The MVP implements only PostgreSQL (dialect/pg); MySQL and SQLite are added
// by implementing this interface without changing the runtime.
package dialect

// Dialect covers textual SQL differences; the batching strategy lives
// in the driver adapters.
type Dialect interface {
	// Name is the dialect name: "postgres", "mysql", "sqlite".
	Name() string
	// Placeholder returns the placeholder for the n-th argument (1-based): $1 or ?.
	Placeholder(n int) string
	// QuoteIdent quotes an identifier (table, column).
	QuoteIdent(s string) string
	// ReturningSupported reports whether INSERT ... RETURNING is available
	// (PG: yes; MySQL: no; SQLite: intentionally uses the LastInsertId path
	// shared with MySQL).
	ReturningSupported() bool
}
