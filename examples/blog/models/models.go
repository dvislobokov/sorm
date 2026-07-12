// Package models — схема примера: авторы и статьи.
// После изменения схемы: go generate ./...
package models

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

import "time"

type Author struct {
	ID       int64      `sorm:"pk,auto"`
	Name     string
	Email    string     `sorm:"unique"`
	Active   bool
	Rating   float64
	JoinedAt time.Time
	Version  int64      `sorm:"version"`
	Articles []*Article `sorm:"hasMany:AuthorID"`
}

type Article struct {
	ID          int64      `sorm:"pk,auto"`
	AuthorID    int64      `sorm:"fk:Author.ID"`
	Author      *Author    `sorm:"belongsTo:AuthorID"`
	Title       string
	Views       int
	PublishedAt *time.Time
}
