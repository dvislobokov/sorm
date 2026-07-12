package sorm_test

import (
	"slices"
	"testing"
	"time"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
)

// Tests for generated Snapshot/Diff — including traps found during design
// review: the time.Time monotonic component, in-place []byte mutation, pointers.

func newUser() *models.User {
	nick := "nick"
	return &models.User{
		ID:        1,
		Email:     "a@b.c",
		Name:      "Alice",
		Nickname:  &nick,
		Active:    true,
		Age:       30,
		Balance:   99.5,
		Avatar:    []byte{1, 2, 3},
		CreatedAt: time.Now(), // carries a monotonic component
		Version:   1,
	}
}

func diffOf(u *models.User) []int {
	m := sorm.MetaOf[models.User]()
	return m.Diff(m.Snapshot(u), u)
}

func TestDiffCleanEntity(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)
	if ch := m.Diff(snap, u); len(ch) != 0 {
		t.Fatalf("unmodified entity produced diff %v (phantom UPDATEs)", ch)
	}
}

func TestDiffTimeMonotonic(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)
	// The "same" instant without the monotonic clock — as after a DB read.
	u.CreatedAt = u.CreatedAt.Round(0).UTC()
	if ch := m.Diff(snap, u); len(ch) != 0 {
		t.Fatalf("equivalent time in a different representation produced diff %v", ch)
	}
}

func TestDiffDetectsChanges(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)

	u.Email = "new@b.c"        // index 1
	*u.Nickname = "other"      // index 3 — mutation through a pointer
	u.Avatar[0] = 9            // index 7 — in-place []byte mutation
	deleted := time.Now()
	u.DeletedAt = &deleted     // index 9 — nil → value

	got := m.Diff(snap, u)
	want := []int{1, 3, 7, 9}
	if !slices.Equal(got, want) {
		t.Fatalf("diff = %v, want %v", got, want)
	}
}

func TestDiffIgnoresPKAndVersion(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)
	u.ID = 42       // PK is not diffed
	u.Version = 7   // the version is managed by the runtime
	if ch := m.Diff(snap, u); len(ch) != 0 {
		t.Fatalf("PK/Version ended up in the diff: %v", ch)
	}
}

func TestDiffNilPointerTransitions(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)
	u.Nickname = nil // value → nil
	got := m.Diff(snap, u)
	if !slices.Equal(got, []int{3}) {
		t.Fatalf("diff = %v, want [3]", got)
	}
}

func TestValuesForMatchesDiffIndexes(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)
	u.Email = "changed@b.c"
	u.Age = 31

	idxs := m.Diff(snap, u)
	vals := m.ValuesFor(u, idxs)
	if len(vals) != 2 || vals[0] != "changed@b.c" || vals[1] != 31 {
		t.Fatalf("ValuesFor(%v) = %v", idxs, vals)
	}
}
