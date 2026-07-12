// Package models — the sorm schema for the benchmarks.
package models

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

type BenchUser struct {
	ID     int64  `sorm:"pk,auto"`
	Name   string
	Email  string `sorm:"unique"`
	Age    int
	Active bool
}
