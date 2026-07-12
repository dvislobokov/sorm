// Package service is the business layer: it composes repositories,
// makes multi-write flows atomic with sorm.RunInTx (the audit entry
// commits or rolls back together with the action) and logs via srog.
package service

import (
	"context"
	"time"

	"github.com/dvislobokov/srog"
	"github.com/google/uuid"

	"github.com/dvislobokov/sorm"

	"github.com/dvislobokov/sorm/examples/chat/internal/models"
	"github.com/dvislobokov/sorm/examples/chat/internal/repository"
)

type Chat struct {
	db    sorm.DB
	log   *srog.Logger
	Users repository.Users
	Rooms repository.Rooms
	Msgs  repository.Messages
	Audit repository.Audit
}

func New(db sorm.DB, log *srog.Logger) *Chat {
	return &Chat{
		db:    db,
		log:   log.Named("chat"),
		Users: repository.NewUsers(db),
		Rooms: repository.NewRooms(db),
		Msgs:  repository.NewMessages(db),
		Audit: repository.NewAudit(db),
	}
}

// Register creates a user with an API token and an audit entry — all in
// one transaction (the token via navigation, the audit via With(tx)).
func (s *Chat) Register(ctx context.Context, email, name string, roles []string) (*models.User, *models.ApiToken, error) {
	u := &models.User{
		Email:  email,
		Name:   name,
		Status: models.StatusActive,
		Roles:  roles,
		Prefs:  &models.UserPrefs{Theme: "light", Lang: "en"},
		Meta:   map[string]any{"source": "api"},
	}
	token := &models.ApiToken{ID: uuid.New(), Label: "default"}

	err := sorm.RunInTx(ctx, s.db, func(tx sorm.Tx) error {
		if err := s.Users.With(tx).Register(ctx, u, token); err != nil {
			return err
		}
		return s.Audit.With(tx).Record(ctx, &models.AuditLog{
			ActorID: &u.ID, Action: "user.register", Entity: "user", EntityID: u.ID,
			Details: map[string]any{"email": email},
		})
	})
	if err != nil {
		return nil, nil, err
	}
	s.log.Information("user {Email} registered as #{UserID}", email, u.ID)
	return u, token, nil
}

// Post writes a message and its audit entry atomically.
func (s *Chat) Post(ctx context.Context, slug string, authorID int64, text string, payload *models.MessagePayload, replyTo *int64) (*models.Message, error) {
	room, err := s.Rooms.BySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	msg := &models.Message{
		RoomID:    room.ID,
		AuthorID:  authorID,
		Text:      text,
		Payload:   payload,
		ReplyToID: replyTo,
	}
	err = sorm.RunInTx(ctx, s.db, func(tx sorm.Tx) error {
		if err := s.Msgs.With(tx).Post(ctx, msg); err != nil {
			return err
		}
		return s.Audit.With(tx).Record(ctx, &models.AuditLog{
			ActorID: &authorID, Action: "message.post", Entity: "message", EntityID: msg.ID,
			Details: map[string]any{"room": slug},
		})
	})
	if err != nil {
		return nil, err
	}
	s.log.Information("message #{MessageID} posted to {Room} by #{UserID}", msg.ID, slug, authorID)
	return msg, nil
}

// Edit changes the text; a concurrent edit comes back as *sorm.ConflictError.
func (s *Chat) Edit(ctx context.Context, id int64, actorID int64, text string) (*models.Message, error) {
	var msg *models.Message
	err := sorm.RunInTx(ctx, s.db, func(tx sorm.Tx) error {
		var err error
		if msg, err = s.Msgs.With(tx).Edit(ctx, id, text); err != nil {
			return err
		}
		return s.Audit.With(tx).Record(ctx, &models.AuditLog{
			ActorID: &actorID, Action: "message.edit", Entity: "message", EntityID: id,
			Details: map[string]any{"len": len(text)},
		})
	})
	return msg, err
}

// Delete soft-deletes a message.
func (s *Chat) Delete(ctx context.Context, id, actorID int64) error {
	return sorm.RunInTx(ctx, s.db, func(tx sorm.Tx) error {
		n, err := s.Msgs.With(tx).SoftDelete(ctx, id, time.Now())
		if err != nil {
			return err
		}
		if n == 0 {
			return sorm.ErrNotFound
		}
		return s.Audit.With(tx).Record(ctx, &models.AuditLog{
			ActorID: &actorID, Action: "message.delete", Entity: "message", EntityID: id,
		})
	})
}

// Ban blocks a user (set-based UPDATE — no entity loaded).
func (s *Chat) Ban(ctx context.Context, id, actorID int64) error {
	err := sorm.RunInTx(ctx, s.db, func(tx sorm.Tx) error {
		n, err := s.Users.With(tx).Ban(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return sorm.ErrNotFound
		}
		return s.Audit.With(tx).Record(ctx, &models.AuditLog{
			ActorID: &actorID, Action: "user.ban", Entity: "user", EntityID: id,
		})
	})
	if err == nil {
		s.log.Warning("user #{UserID} banned by #{ActorID}", id, actorID)
	}
	return err
}
