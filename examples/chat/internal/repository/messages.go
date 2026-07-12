package repository

import (
	"context"
	"time"

	"github.com/dvislobokov/sorm"

	"github.com/dvislobokov/sorm/examples/chat/internal/models"
	gen "github.com/dvislobokov/sorm/examples/chat/internal/models/sormgen"
)

// Messages uses the low-level API (no generated Context): sessions where
// tracking matters, plain queries and projections where it does not.
type Messages struct {
	db sorm.DB
}

func NewMessages(db sorm.DB) Messages       { return Messages{db: db} }
func (r Messages) With(db sorm.DB) Messages { return Messages{db: db} }

func (r Messages) Post(ctx context.Context, msg *models.Message) error {
	s := sorm.NewSession(r.db)
	sorm.Add(s, msg)
	return s.SaveChanges(ctx)
}

// Page returns a room page newest-first with authors and threaded
// replies eagerly loaded; the (room_id, created_at) composite index
// backs the scan. Soft-deleted messages are filtered out.
func (r Messages) Page(ctx context.Context, roomID int64, before time.Time, limit int) ([]*models.Message, error) {
	m := gen.Message
	return sorm.Query[models.Message](r.db).
		Where(
			m.RoomID.Eq(roomID),
			m.CreatedAt.Lt(before),
			m.DeletedAt.IsNull(),
		).
		With(
			m.Author.Include(),
			m.ReplyTo.Include(), // self-referencing belongsTo
		).
		OrderBy(m.CreatedAt.Desc()).
		Limit(limit).
		Named("messages.page").
		All(ctx)
}

// Edit — tracked read + mutate; a concurrent edit surfaces as
// *sorm.ConflictError thanks to the version column.
func (r Messages) Edit(ctx context.Context, id int64, text string) (*models.Message, error) {
	s := sorm.NewSession(r.db)
	msg, err := sorm.Track[models.Message](s).
		Where(gen.Message.ID.Eq(id), gen.Message.DeletedAt.IsNull()).
		One(ctx)
	if err != nil {
		return nil, err
	}
	msg.Text = text
	return msg, s.SaveChanges(ctx)
}

// SoftDelete — set-based UPDATE: no SELECT roundtrip.
func (r Messages) SoftDelete(ctx context.Context, id int64, at time.Time) (int64, error) {
	m := gen.Message
	return sorm.Update[models.Message](r.db).
		Set(m.DeletedAt.Set(at)).
		Where(m.ID.Eq(id), m.DeletedAt.IsNull()).
		Named("messages.soft-delete").
		Exec(ctx)
}

// RoomStat is a typed projection target.
type RoomStat struct {
	RoomID   int64 `sorm:"room_id"`
	Messages int64 `sorm:"n"`
	LastAt   time.Time `sorm:"last_at"`
}

// Stats — GROUP BY projection: message count and last activity per room.
func (r Messages) Stats(ctx context.Context, roomID int64) (*RoomStat, error) {
	m := gen.Message
	return sorm.Project[RoomStat](
		sorm.From[models.Message](r.db).
			Where(m.RoomID.Eq(roomID), m.DeletedAt.IsNull()).
			GroupBy(m.RoomID),
		sorm.Field(m.RoomID),
		sorm.As(sorm.CountAll[models.Message](), "n"),
		sorm.As(sorm.Max[models.Message](m.CreatedAt), "last_at"),
	).One(ctx)
}
