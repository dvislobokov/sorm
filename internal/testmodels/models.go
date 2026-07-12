// Package testmodels — тестовая схема, покрывающая все виды полей,
// которые умеет генератор: базовые типы, nullable-указатели, time.Time,
// []byte, версию и навигации.
package testmodels

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

import (
	"time"

	"github.com/dvislobokov/sorm"
)

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
	Posts     []*Post  `sorm:"hasMany:AuthorID"`
	Tags      []*Tag   `sorm:"many2many:user_tags"`
	Profile   *Profile `sorm:"hasOne:UserID"`
}

// Profile — hasOne-сторона: FK у ребёнка, навигация-указатель у родителя.
type Profile struct {
	ID     int64  `sorm:"pk,auto"`
	UserID int64  `sorm:"fk:User.ID,uniqueIndex:uq_profiles_user"`
	Bio    string
}

// Tag — сторона many2many (join-таблица user_tags генерируется неявно).
type Tag struct {
	ID    int64  `sorm:"pk,auto"`
	Label string `sorm:"unique"`
}

// Indexes — кастомные индексы, невыразимые тегами (DESC-сортировка;
// так же задаются выражения, WHERE и типы вроде gin/fulltext).
func (User) Indexes() []sorm.IndexDef {
	return []sorm.IndexDef{
		{Name: "idx_users_created_desc", Parts: []sorm.IndexPart{
			{Column: "created_at", Desc: true},
			{Column: "id"},
		}},
	}
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
	AuthorID int64  `sorm:"fk:User.ID,index:idx_posts_author_title"`
	Author   *User  `sorm:"belongsTo:AuthorID"`
	Title    string `sorm:"index:idx_posts_author_title"` // композитный (author_id, title)
	Body     string
	Views    int    `sorm:"index"` // одноколоночный idx_posts_views
}
