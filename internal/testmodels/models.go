// Package testmodels is a test schema covering every field kind the
// generator supports: basic types, nullable pointers, time.Time,
// []byte, a version field, and navigations.
package testmodels

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/dvislobokov/sorm"
)

// Cents is a custom scalar (driver.Valuer + sql.Scanner): money in minor
// units stored as BIGINT. Exercises KindScalar columns — the type is not
// required to be Go-comparable; snapshots go through sorm.ScalarSnapshot.
type Cents struct {
	Units int64
}

func (c Cents) Value() (driver.Value, error) { return c.Units, nil }

func (c *Cents) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		c.Units = 0
	case int64:
		c.Units = v
	case []byte:
		_, err := fmt.Sscan(string(v), &c.Units)
		return err
	default:
		return fmt.Errorf("Cents: cannot scan %T", src)
	}
	return nil
}

type User struct {
	ID        int64  `sorm:"pk,auto"`
	Email     string `sorm:"unique"`
	Name      string
	Nickname  *string
	Active    bool
	Age       int
	Balance   float64
	Avatar    []byte
	CreatedAt time.Time
	DeletedAt *time.Time `sorm:"softDelete"`
	Version   int64    `sorm:"version"`
	Posts     []*Post  `sorm:"hasMany:AuthorID"`
	Tags      []*Tag   `sorm:"many2many:user_tags"`
	Profile   *Profile `sorm:"hasOne:UserID"`
}

// Profile is the hasOne side: the FK lives on the child, the parent holds a pointer navigation.
// It also carries the JSON columns: a typed struct (nullable) and a schemaless map.
type Profile struct {
	ID     int64 `sorm:"pk,auto"`
	UserID int64 `sorm:"fk:User.ID,uniqueIndex:uq_profiles_user"`
	Bio    string
	Prefs  *ProfilePrefs  `sorm:"json"` // nullable JSONB/JSON/TEXT
	Meta   map[string]any `sorm:"json"`
}

// ProfilePrefs is a plain (non-entity) struct stored as a JSON document.
// Its fields get typed generated accessors (Profile.PrefsDoc.*).
type ProfilePrefs struct {
	Theme  string      `json:"theme"`
	Limit  int         `json:"limit"`
	Beta   bool        `json:"beta"`
	Labels []string    `json:"labels,omitempty"`
	Notify PrefsNotify `json:"notify"`
}

// PrefsNotify is a nested JSON object (accessors nest too).
type PrefsNotify struct {
	Email bool   `json:"email"`
	Chan  string `json:"chan"`
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
	Views    int        `sorm:"index"` // single-column idx_posts_views
	Comments []*Comment `sorm:"hasMany:PostID"`
	// Auto-timestamps: CreatedAt stamps on insert (manual value wins),
	// UpdatedAt on insert and every effective update.
	CreatedAt time.Time `sorm:"autoCreate"`
	UpdatedAt time.Time `sorm:"autoUpdate"`
}

// Device exercises native uuid.UUID support: a client-assigned UUID PK
// (uuid.New() before Add) and a nullable UUID column.
type Device struct {
	ID      uuid.UUID `sorm:"pk"`
	OwnerID int64     `sorm:"fk:User.ID"`
	Owner   *User     `sorm:"belongsTo:OwnerID"`
	Token   *uuid.UUID
	Name    string
	// Price exercises a custom Valuer/Scanner scalar; the SQL type is
	// mandatory for scalars (not statically derivable).
	Price Cents `sorm:"type:bigint"`
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
