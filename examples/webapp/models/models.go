// Package models — схема todo-приложения.
// История эволюции лежит в ../migrations: init → add_tasks → add_priority;
// каждый файл сгенерирован `sorm migrate diff` (см. README).
package models

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

import "time"

type User struct {
	ID      int64   `sorm:"pk,auto"`
	Name    string
	Email   string  `sorm:"unique"`
	Version int64   `sorm:"version"`
	Tasks   []*Task `sorm:"hasMany:UserID"`
}

type Task struct {
	ID        int64     `sorm:"pk,auto"`
	UserID    int64     `sorm:"fk:User.ID"`
	User      *User     `sorm:"belongsTo:UserID"`
	Title     string
	Done      bool
	Priority  int
	CreatedAt time.Time
	Version   int64     `sorm:"version"`
}
