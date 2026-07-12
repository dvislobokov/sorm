package sorm

import "time"

// Helpers for generated snapshots: correct copying and comparison of
// nullable fields. A naive == on pointers would compare addresses, on
// time.Time — the monotonic component (phantom UPDATEs), on []byte in an
// interface — panic.

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

// CloneTimePtr copies a *time.Time, dropping the monotonic component.
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
