// Package testmodels — тестовая схема, покрывающая все виды полей,
// которые умеет генератор: базовые типы, nullable-указатели, time.Time,
// []byte, версию и навигации.
package testmodels

//go:generate go run sorm/cmd/sorm gen .

import "time"

type User struct {
	ID        int64   `sorm:"pk,auto"`
	Email     string  `sorm:"unique"`
	Name      string
	Nickname  *string
	Active    bool
	Age       int
	Balance   float64
	Avatar    []byte
	CreatedAt time.Time
	DeletedAt *time.Time
	Version   int64   `sorm:"version"`
	Posts     []*Post `sorm:"hasMany:AuthorID"`
}

// ApiKey — сущность со строковым (UUID) PK, назначаемым клиентом:
// путь без auto/RETURNING.
type ApiKey struct {
	ID     string `sorm:"pk,type:varchar(36)"`
	UserID int64  `sorm:"fk:User.ID"`
	User   *User  `sorm:"belongsTo:UserID"`
	Label  string
}

type Post struct {
	ID       int64  `sorm:"pk,auto"`
	AuthorID int64  `sorm:"fk:User.ID"`
	Author   *User  `sorm:"belongsTo:AuthorID"`
	Title    string
	Body     string
}
