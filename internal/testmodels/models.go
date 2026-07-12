// Package testmodels is a test schema covering every field kind the
// generator supports: basic types, nullable pointers, time.Time,
// []byte, a version field, and navigations.
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

// Profile is the hasOne side: the FK lives on the child, the parent holds a pointer navigation.
type Profile struct {
	ID     int64  `sorm:"pk,auto"`
	UserID int64  `sorm:"fk:User.ID,uniqueIndex:uq_profiles_user"`
	Bio    string
}

// Tag is a many2many side (the user_tags join table is generated implicitly).
type Tag struct {
	ID    int64  `sorm:"pk,auto"`
	Label string `sorm:"unique"`
}

// Indexes defines custom indexes inexpressible via tags (DESC ordering;
// expressions, WHERE, and types like gin/fulltext are set the same way).
func (User) Indexes() []sorm.IndexDef {
	return []sorm.IndexDef{
		{Name: "idx_users_created_desc", Parts: []sorm.IndexPart{
			{Column: "created_at", Desc: true},
			{Column: "id"},
		}},
	}
}

// ApiKey is an entity with a client-assigned string (UUID) PK:
// the path without auto/RETURNING.
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
	Title    string `sorm:"index:idx_posts_author_title"` // composite (author_id, title)
	Body     string
	Views    int    `sorm:"index"` // single-column idx_posts_views
}
