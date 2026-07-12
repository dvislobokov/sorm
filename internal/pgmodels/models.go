// Package pgmodels is a PostgreSQL-only test schema: native array
// columns are rejected by the DDL generator on other dialects, so they
// live apart from the portable testmodels.
package pgmodels

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

type Article struct {
	ID    int64 `sorm:"pk,auto"`
	Title string
	// Native arrays: text[] and bigint[]; nil slice ⇒ NULL.
	Tags []string `sorm:"array"`
	Nums []int64  `sorm:"array"`
}
