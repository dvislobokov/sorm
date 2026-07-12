package sorm_test

import (
	"context"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// Строковый (UUID) PK, назначаемый клиентом: insert без RETURNING,
// identity map и дифф по строковому ключу, FK на int64-родителя.
func TestStringPKSQLite(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	s := sorm.NewSession(db)
	owner := &models.User{Email: "k@b.c", Name: "K", Active: true}
	key := &models.ApiKey{ID: "3f1b6c1e-8a30-4b7e-9f2e-000000000001", User: owner, Label: "ci"}
	sorm.Add(s, owner)
	sorm.Add(s, key)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if key.UserID != owner.ID {
		t.Fatalf("FK-fixup со строковым PK ребёнка: %d != %d", key.UserID, owner.ID)
	}

	// Track по строковому PK + частичный UPDATE.
	s2 := sorm.NewSession(db)
	k := gen.ApiKey
	loaded, err := sorm.Track[models.ApiKey](s2).Where(k.ID.Eq(key.ID)).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Label = "prod"
	if err := s2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	fresh, err := sorm.Query[models.ApiKey](db).Where(k.ID.Eq(key.ID)).One(ctx)
	if err != nil || fresh.Label != "prod" {
		t.Fatalf("label=%q err=%v", fresh.Label, err)
	}

	// Identity map по строковому ключу.
	a, _ := sorm.Track[models.ApiKey](s2).Where(k.ID.Eq(key.ID)).One(ctx)
	b, _ := sorm.Track[models.ApiKey](s2).Where(k.Label.Eq("prod")).One(ctx)
	if a != b {
		t.Fatal("identity map по строковому PK не сработал")
	}

	// Пустой PK у не-auto сущности — валидационная ошибка до БД? Пока — ошибка
	// вставки родителем FK или самой БД; фиксируем текущее поведение Remove-валидации.
	s3 := sorm.NewSession(db)
	sorm.Remove(s3, &models.ApiKey{}) // нулевой PK, не отслеживается
	if err := s3.SaveChanges(ctx); err == nil {
		t.Fatal("Remove без PK должен падать валидацией")
	}
}
