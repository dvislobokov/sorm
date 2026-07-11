package sorm_test

import (
	"slices"
	"testing"
	"time"

	"sorm"
	models "sorm/internal/testmodels"
)

// Тесты сгенерированных Snapshot/Diff — в том числе ловушки, найденные ревью
// дизайна: monotonic-компонента time.Time, мутация []byte in place, указатели.

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
		CreatedAt: time.Now(), // содержит monotonic-компоненту
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
		t.Fatalf("немутированная сущность даёт дифф %v (фантомные UPDATE)", ch)
	}
}

func TestDiffTimeMonotonic(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)
	// «Та же» временная точка без monotonic — как после чтения из БД.
	u.CreatedAt = u.CreatedAt.Round(0).UTC()
	if ch := m.Diff(snap, u); len(ch) != 0 {
		t.Fatalf("эквивалентное время в другом представлении дало дифф %v", ch)
	}
}

func TestDiffDetectsChanges(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)

	u.Email = "new@b.c"        // индекс 1
	*u.Nickname = "other"      // индекс 3 — мутация через указатель
	u.Avatar[0] = 9            // индекс 7 — мутация []byte in place
	deleted := time.Now()
	u.DeletedAt = &deleted     // индекс 9 — nil → значение

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
	u.ID = 42       // PK не диффится
	u.Version = 7   // версией управляет рантайм
	if ch := m.Diff(snap, u); len(ch) != 0 {
		t.Fatalf("PK/Version попали в дифф: %v", ch)
	}
}

func TestDiffNilPointerTransitions(t *testing.T) {
	u := newUser()
	m := sorm.MetaOf[models.User]()
	snap := m.Snapshot(u)
	u.Nickname = nil // значение → nil
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
