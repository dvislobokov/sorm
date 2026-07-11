package sorm

import (
	"errors"
	"fmt"
)

// ErrNotFound — единственная семантика «не найдено» во всём API (One, будущий Find по PK).
var ErrNotFound = errors.New("sorm: not found")

// ScanError — несоответствие колонок результата и полей назначения (Raw/RawAs/Project).
type ScanError struct {
	Missing []string // колонки результата, для которых нет назначения
	Extra   []string // ожидались, но отсутствуют в результате
}

func (e *ScanError) Error() string {
	return fmt.Sprintf("sorm: scan mismatch: missing destinations for %v, absent columns %v", e.Missing, e.Extra)
}
