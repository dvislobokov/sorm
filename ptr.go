package sorm

import "time"

// Хелперы для генерируемых снапшотов: корректное копирование и сравнение
// nullable-полей. Наивный == по указателям сравнил бы адреса, по time.Time —
// monotonic-компоненту (фантомные UPDATE), по []byte в интерфейсе — паника.

func ClonePtr[V any](p *V) *V {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func PtrEq[V comparable](a, b *V) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// CloneTimePtr копирует *time.Time, отбрасывая monotonic-компоненту.
func CloneTimePtr(p *time.Time) *time.Time {
	if p == nil {
		return nil
	}
	t := p.Round(0)
	return &t
}

func TimePtrEq(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
