package sorm

import (
	"errors"
	"fmt"
)

// ErrNotFound — единственная семантика «не найдено» во всём API (One, будущий Find по PK).
var ErrNotFound = errors.New("sorm: not found")

// ErrCyclicGraph — цикл между новыми сущностями при SaveChanges (например,
// взаимные ссылки двух Added-объектов). В MVP не поддерживается.
var ErrCyclicGraph = errors.New("sorm: cyclic dependency between new entities")

// ConflictError — optimistic concurrency: UPDATE/DELETE затронул 0 строк,
// значит строка изменена или удалена конкурентно после загрузки.
type ConflictError struct {
	Table string
	PK    any
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("sorm: concurrency conflict on %s pk=%v (row changed or deleted since load)", e.Table, e.PK)
}

// ConstraintKind — вид нарушенного ограничения БД.
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

// ConstraintError — нарушение ограничения БД, транслированное драйверным
// адаптером в типизированную ошибку: хендлер отличает «email занят» (409)
// от аварии (500) без разбора кодов драйвера.
//
//	var ce *sorm.ConstraintError
//	if errors.As(err, &ce) && ce.Kind == sorm.ConstraintUnique { ... }
type ConstraintError struct {
	Kind       ConstraintKind
	Constraint string // имя констрейнта/колонки, если драйвер его сообщил
	Err        error
}

func (e *ConstraintError) Error() string {
	if e.Constraint != "" {
		return fmt.Sprintf("sorm: %s constraint violation (%s): %v", e.Kind, e.Constraint, e.Err)
	}
	return fmt.Sprintf("sorm: %s constraint violation: %v", e.Kind, e.Err)
}

func (e *ConstraintError) Unwrap() error { return e.Err }

// IsUniqueViolation — краткая проверка на нарушение уникальности.
func IsUniqueViolation(err error) bool {
	var ce *ConstraintError
	return errors.As(err, &ce) && ce.Kind == ConstraintUnique
}

// ScanError — несоответствие колонок результата и полей назначения (Raw/RawAs/Project).
type ScanError struct {
	Missing []string // колонки результата, для которых нет назначения
	Extra   []string // ожидались, но отсутствуют в результате
}

func (e *ScanError) Error() string {
	return fmt.Sprintf("sorm: scan mismatch: missing destinations for %v, absent columns %v", e.Missing, e.Extra)
}
