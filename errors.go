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

// ScanError — несоответствие колонок результата и полей назначения (Raw/RawAs/Project).
type ScanError struct {
	Missing []string // колонки результата, для которых нет назначения
	Extra   []string // ожидались, но отсутствуют в результате
}

func (e *ScanError) Error() string {
	return fmt.Sprintf("sorm: scan mismatch: missing destinations for %v, absent columns %v", e.Missing, e.Extra)
}
