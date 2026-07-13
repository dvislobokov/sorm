package sorm

import "context"

// Entity lifecycle hooks — optional interfaces on model types, detected
// with a plain interface assertion (no reflection, no codegen):
//
//	func (m *Message) BeforeSave(ctx context.Context, op sorm.SaveOp) error {
//	    if op == sorm.SaveInsert && m.Text == "" {
//	        return errors.New("empty message")   // vetoes the whole flush
//	    }
//	    m.Text = strings.TrimSpace(m.Text)       // mutations are persisted
//	    return nil
//	}
//
// Scope and guarantees:
//   - BeforeSave fires during SaveChanges planning — BEFORE any SQL is
//     built: for every insert and delete, and for updates only when the
//     entity actually changed (the diff is re-taken afterwards, so hook
//     mutations are written). An error aborts the flush; nothing reaches
//     the database.
//   - Hooks run before auto-timestamps and version init, so those still
//     apply to hook-mutated values.
//   - AfterLoad fires for every entity row a query materializes (All,
//     One, Iter, Raw/RawAs when T implements it) — including rows that
//     resolve to an already-tracked pointer. An error fails the query.
//   - Set-based statements (Update/Delete/Upsert builders) BYPASS hooks —
//     same rule as EF Core's ExecuteUpdate/ExecuteDelete.

// SaveOp tells a BeforeSave hook which operation is being planned.
type SaveOp int

const (
	SaveInsert SaveOp = iota
	SaveUpdate
	SaveDelete
)

func (o SaveOp) String() string {
	switch o {
	case SaveInsert:
		return "insert"
	case SaveUpdate:
		return "update"
	default:
		return "delete"
	}
}

// BeforeSaver is implemented by entities that want a say before being
// inserted, updated or deleted by a session flush.
type BeforeSaver interface {
	BeforeSave(ctx context.Context, op SaveOp) error
}

// AfterLoader is implemented by entities that post-process themselves
// after materialization (computed fields, decryption, ...).
type AfterLoader interface {
	AfterLoad(ctx context.Context) error
}

// beforeSave invokes the hook when the entity implements it.
func beforeSave(ctx context.Context, e any, op SaveOp) error {
	if h, ok := e.(BeforeSaver); ok {
		return h.BeforeSave(ctx, op)
	}
	return nil
}

// afterLoad invokes the hook when the entity implements it.
func afterLoad(ctx context.Context, e any) error {
	if h, ok := e.(AfterLoader); ok {
		return h.AfterLoad(ctx)
	}
	return nil
}
