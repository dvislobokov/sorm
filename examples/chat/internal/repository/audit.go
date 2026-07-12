package repository

import (
	"context"

	"github.com/dvislobokov/sorm"

	"github.com/dvislobokov/sorm/examples/chat/internal/models"
	gen "github.com/dvislobokov/sorm/examples/chat/internal/models/sormgen"
)

// Audit is an append-only journal. Record is meant to be called on a
// transaction-bound copy (With(tx)) so the audit entry commits or rolls
// back together with the action it describes.
type Audit struct {
	db sorm.DB
}

func NewAudit(db sorm.DB) Audit       { return Audit{db: db} }
func (r Audit) With(db sorm.DB) Audit { return Audit{db: db} }

func (r Audit) Record(ctx context.Context, entry *models.AuditLog) error {
	s := sorm.NewSession(r.db)
	sorm.Add(s, entry)
	return s.SaveChanges(ctx)
}

// Tail — the latest entries, newest first (backed by the custom
// idx_audit_at_desc index declared in models.AuditLog.Indexes).
func (r Audit) Tail(ctx context.Context, limit int) ([]*models.AuditLog, error) {
	a := gen.AuditLog
	return sorm.Query[models.AuditLog](r.db).
		OrderBy(a.At.Desc(), a.ID.Desc()).
		Limit(limit).
		Named("audit.tail").
		All(ctx)
}
