// Package models is the persistence model of the chat example. It shows
// every sorm mapping feature in one schema: auto and client-assigned
// (UUID) keys, optimistic concurrency, auto-timestamps, typed JSON
// documents and schemaless maps, native PG arrays, a custom
// Valuer/Scanner scalar, composite and custom indexes, all four relation
// kinds and a nullable self-referencing FK.
package models

//go:generate go run github.com/dvislobokov/sorm/cmd/sorm gen .

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/dvislobokov/sorm"
)

// UserStatus is a named string type: kept as its own type in predicates
// (User.Status.Eq(models.StatusBanned) — no raw strings).
type UserStatus string

const (
	StatusActive UserStatus = "active"
	StatusBanned UserStatus = "banned"
)

// Cents is a custom scalar (driver.Valuer + sql.Scanner): money in minor
// units stored as BIGINT. The SQL type comes from the `type:` tag.
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

// UserPrefs is a struct-shaped JSON document: the generator emits typed
// accessors (gen.User.PrefsDoc.Theme.Eq("dark"), .Notify.Push.IsTrue()).
type UserPrefs struct {
	Theme  string      `json:"theme"`
	Lang   string      `json:"lang"`
	Notify NotifyPrefs `json:"notify"`
}

type NotifyPrefs struct {
	Email bool `json:"email"`
	Push  bool `json:"push"`
}

// User — pk/auto, unique, named type, nullable pointer, typed JSON,
// schemaless JSON map, PG array, custom scalar, version, auto-timestamps.
type User struct {
	ID       int64      `sorm:"pk,auto"`
	Email    string     `sorm:"unique"`
	Name     string
	Bio      *string
	Status   UserStatus
	Prefs    *UserPrefs     `sorm:"json"`        // nullable JSONB
	Meta     map[string]any `sorm:"json"`        // schemaless JSONB
	Roles    []string       `sorm:"array"`       // native text[]
	Balance  Cents          `sorm:"type:bigint"` // custom scalar
	Version  int64          `sorm:"version"`
	CreatedAt time.Time     `sorm:"autoCreate"`
	UpdatedAt time.Time     `sorm:"autoUpdate"`

	Tokens []*ApiToken `sorm:"hasMany:UserID"`
	Rooms  []*Room     `sorm:"many2many:room_members"`
}

// ApiToken — client-assigned uuid.UUID primary key (uuid.New() before Add).
type ApiToken struct {
	ID        uuid.UUID `sorm:"pk"`
	UserID    int64     `sorm:"fk:User.ID"`
	User      *User     `sorm:"belongsTo:UserID"`
	Label     string
	ExpiresAt *time.Time
	CreatedAt time.Time `sorm:"autoCreate"`
}

// Room — belongsTo owner, many2many members, hasMany messages.
type Room struct {
	ID        int64   `sorm:"pk,auto"`
	Slug      string  `sorm:"unique"`
	Title     string
	Topic     *string
	OwnerID   int64   `sorm:"fk:User.ID"`
	Owner     *User   `sorm:"belongsTo:OwnerID"`
	CreatedAt time.Time `sorm:"autoCreate"`

	Members  []*User    `sorm:"many2many:room_members"`
	Messages []*Message `sorm:"hasMany:RoomID"`
}

// MessagePayload — a typed JSON document on a message.
type MessagePayload struct {
	Kind        string       `json:"kind"` // "text" | "image" | "system"
	Mentions    []string     `json:"mentions,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Attachment struct {
	URL  string `json:"url"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// Message — composite index (room_id, created_at) via a shared tag name,
// nullable self-referencing FK (threads), soft-delete-style DeletedAt.
type Message struct {
	ID        int64  `sorm:"pk,auto"`
	RoomID    int64  `sorm:"fk:Room.ID,index:idx_messages_room_created"`
	Room      *Room  `sorm:"belongsTo:RoomID"`
	AuthorID  int64  `sorm:"fk:User.ID"`
	Author    *User  `sorm:"belongsTo:AuthorID"`
	Text      string
	Payload   *MessagePayload `sorm:"json"`
	ReplyToID *int64          `sorm:"fk:Message.ID"` // nullable self-FK
	ReplyTo   *Message        `sorm:"belongsTo:ReplyToID"`
	Version   int64           `sorm:"version"`
	CreatedAt time.Time       `sorm:"autoCreate,index:idx_messages_room_created"`
	UpdatedAt time.Time       `sorm:"autoUpdate"`
	DeletedAt *time.Time
}

// AuditLog — append-only journal: schemaless JSON details, nullable actor.
type AuditLog struct {
	ID       int64          `sorm:"pk,auto"`
	ActorID  *int64         `sorm:"fk:User.ID"`
	Action   string         `sorm:"index"`
	Entity   string
	EntityID int64
	Details  map[string]any `sorm:"json"`
	At       time.Time      `sorm:"autoCreate"`
}

// Indexes — a custom index inexpressible via tags: newest-first scans.
func (AuditLog) Indexes() []sorm.IndexDef {
	return []sorm.IndexDef{
		{Name: "idx_audit_at_desc", Parts: []sorm.IndexPart{
			{Column: "at", Desc: true},
			{Column: "id"},
		}},
	}
}
