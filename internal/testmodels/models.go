// Package testmodels is a test schema covering every field kind the
// generator supports: basic types, nullable pointers, time.Time,
// []byte, a version field, and navigations.
package testmodels

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

import (
	"time"

	"github.com/google/uuid"

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
// It also carries the JSON columns: a typed struct (nullable) and a schemaless map.
type Profile struct {
	ID     int64          `sorm:"pk,auto"`
	UserID int64          `sorm:"fk:User.ID,uniqueIndex:uq_profiles_user"`
	Bio    string
	Prefs  *ProfilePrefs  `sorm:"json"` // nullable JSONB/JSON/TEXT
	Meta   map[string]any `sorm:"json"`
}

// ProfilePrefs is a plain (non-entity) struct stored as a JSON document.
type ProfilePrefs struct {
	Theme  string `json:"theme"`
	Limit  int    `json:"limit"`
	Labels []string `json:"labels,omitempty"`
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
	ID       int64      `sorm:"pk,auto"`
	AuthorID int64      `sorm:"fk:User.ID,index:idx_posts_author_title"`
	Author   *User      `sorm:"belongsTo:AuthorID"`
	Title    string     `sorm:"index:idx_posts_author_title"` // composite (author_id, title)
	Body     string
	Views    int        `sorm:"index"` // single-column idx_posts_views
	Comments []*Comment `sorm:"hasMany:PostID"`
}

// Device exercises native uuid.UUID support: a client-assigned UUID PK
// (uuid.New() before Add) and a nullable UUID column.
type Device struct {
	ID      uuid.UUID  `sorm:"pk"`
	OwnerID int64      `sorm:"fk:User.ID"`
	Owner   *User      `sorm:"belongsTo:OwnerID"`
	Token   *uuid.UUID
	Name    string
}

// Comment is the nullable-FK side: PostID may be NULL (a detached comment).
// Exercises pointer-FK navigation: value-keyed relation maps and
// address-of FK fixup on insert.
type Comment struct {
	ID     int64  `sorm:"pk,auto"`
	PostID *int64 `sorm:"fk:Post.ID"`
	Post   *Post  `sorm:"belongsTo:PostID"`
	Body   string
}
