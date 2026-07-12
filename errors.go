package sorm

import (
	"errors"
	"fmt"
)

// ErrNotFound — the single "not found" semantics across the entire API (One, future Find by PK).
var ErrNotFound = errors.New("sorm: not found")

// ErrCyclicGraph — a cycle between new entities during SaveChanges (e.g.
// mutual references between two Added objects). Not supported in the MVP.
var ErrCyclicGraph = errors.New("sorm: cyclic dependency between new entities")

// ConflictError — optimistic concurrency: an UPDATE/DELETE affected 0 rows,
// meaning the row was changed or deleted concurrently after loading.
type ConflictError struct {
	Table string
	PK    any
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("sorm: concurrency conflict on %s pk=%v (row changed or deleted since load)", e.Table, e.PK)
}

// ConstraintKind — the kind of violated DB constraint.
type ConstraintKind int

const (
	ConstraintUnique ConstraintKind = iota + 1
	ConstraintForeignKey
	ConstraintNotNull
	ConstraintCheck
)

func (k ConstraintKind) String() string {
	switch k {
	case ConstraintUnique:
		return "unique"
	case ConstraintForeignKey:
		return "foreign key"
	case ConstraintNotNull:
		return "not null"
	case ConstraintCheck:
		return "check"
	default:
		return "constraint"
	}
}

// ConstraintError — a DB constraint violation translated by the driver
// adapter into a typed error: a handler can tell "email taken" (409)
// from a failure (500) without parsing driver codes.
//
//	var ce *sorm.ConstraintError
//	if errors.As(err, &ce) && ce.Kind == sorm.ConstraintUnique { ... }
type ConstraintError struct {
	Kind       ConstraintKind
	Constraint string // constraint/column name, if the driver reported it
	Err        error
}

func (e *ConstraintError) Error() string {
	if e.Constraint != "" {
		return fmt.Sprintf("sorm: %s constraint violation (%s): %v", e.Kind, e.Constraint, e.Err)
	}
	return fmt.Sprintf("sorm: %s constraint violation: %v", e.Kind, e.Err)
}

func (e *ConstraintError) Unwrap() error { return e.Err }

// IsUniqueViolation — a shorthand check for a uniqueness violation.
func IsUniqueViolation(err error) bool {
	var ce *ConstraintError
	return errors.As(err, &ce) && ce.Kind == ConstraintUnique
}

// ScanError — a mismatch between result columns and destination fields (Raw/RawAs/Project).
type ScanError struct {
	Missing []string // result columns that have no destination
	Extra   []string // expected but absent from the result
}

func (e *ScanError) Error() string {
	return fmt.Sprintf("sorm: scan mismatch: missing destinations for %v, absent columns %v", e.Missing, e.Extra)
}
