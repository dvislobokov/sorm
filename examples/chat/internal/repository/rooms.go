package repository

import (
	"context"

	"github.com/dvislobokov/sorm"

	"github.com/dvislobokov/sorm/examples/chat/internal/models"
	gen "github.com/dvislobokov/sorm/examples/chat/internal/models/sormgen"
)

type Rooms struct {
	db sorm.DB
}

func NewRooms(db sorm.DB) Rooms         { return Rooms{db: db} }
func (r Rooms) With(db sorm.DB) Rooms   { return Rooms{db: db} }

func (r Rooms) Create(ctx context.Context, room *models.Room) error {
	c := gen.NewContext(r.db)
	c.Rooms.Add(room)
	if err := c.SaveChanges(ctx); err != nil {
		return err
	}
	// The owner is a member by definition (many2many link table).
	return gen.Room.Members.Link(ctx, r.db, room, room.Owner)
}

// BySlug loads the room with its owner (eager belongsTo).
func (r Rooms) BySlug(ctx context.Context, slug string) (*models.Room, error) {
	c := gen.NewContext(r.db)
	return c.Rooms.NoTracking().
		Where(gen.Room.Slug.Eq(slug)).
		With(gen.Room.Owner.Include()).
		One(ctx)
}

func (r Rooms) Join(ctx context.Context, room *models.Room, u *models.User) error {
	return gen.Room.Members.Link(ctx, r.db, room, u)
}

func (r Rooms) Leave(ctx context.Context, room *models.Room, u *models.User) error {
	return gen.Room.Members.Unlink(ctx, r.db, room, u)
}

// OfMember — rooms the user belongs to: EXISTS over the join table.
func (r Rooms) OfMember(ctx context.Context, u *models.User) ([]*models.Room, error) {
	return sorm.Query[models.Room](r.db).
		Where(gen.Room.Members.Any(gen.User.ID.Eq(u.ID))).
		OrderBy(gen.Room.Slug.Asc()).
		Named("rooms.of-member").
		All(ctx)
}
