package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

func TestManyToManySQLite(t *testing.T) { runM2M(t, sqliteDB(t)) }
func TestManyToManyPG(t *testing.T)     { runM2M(t, testPool(t)) }
func TestManyToManyMySQL(t *testing.T)  { runM2M(t, mysqlDB(t)) }

func runM2M(t *testing.T, db sorm.DB) {
	ctx := context.Background()
	u := gen.User

	// Сущности обеих сторон.
	s := sorm.NewSession(db)
	alice := &models.User{Email: "a@b.c", Name: "Alice", Active: true, CreatedAt: nowNoZero()}
	bob := &models.User{Email: "b@b.c", Name: "Bob", Active: true, CreatedAt: nowNoZero()}
	goTag := &models.Tag{Label: "go"}
	dbTag := &models.Tag{Label: "db"}
	rustTag := &models.Tag{Label: "rust"}
	sorm.Add(s, alice, bob)
	sorm.Add(s, goTag, dbTag, rustTag)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// Link: явное связывание.
	if err := u.Tags.Link(ctx, db, alice, goTag, dbTag); err != nil {
		t.Fatal(err)
	}
	if err := u.Tags.Link(ctx, db, bob, rustTag); err != nil {
		t.Fatal(err)
	}

	// Повторный Link — типизированный конфликт (композитный PK join-таблицы).
	if err := u.Tags.Link(ctx, db, alice, goTag); !sorm.IsUniqueViolation(err) {
		t.Fatalf("повторный Link: ожидали unique violation, получили %v", err)
	}

	// Include: раскладка по родителям, пустая связь — пустой слайс.
	users, err := sorm.Query[models.User](db).
		With(u.Tags.Include()).
		OrderBy(u.Name.Asc()).
		All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || len(users[0].Tags) != 2 || len(users[1].Tags) != 1 {
		t.Fatalf("include: alice=%d bob=%d", len(users[0].Tags), len(users[1].Tags))
	}
	labels := []string{users[0].Tags[0].Label, users[0].Tags[1].Label}
	if !((labels[0] == "go" && labels[1] == "db") || (labels[0] == "db" && labels[1] == "go")) {
		t.Fatalf("alice tags: %v", labels)
	}

	// Include с фильтром детей.
	filtered, err := sorm.Query[models.User](db).
		With(u.Tags.Include(gen.Tag.Label.Eq("go"))).
		OrderBy(u.Name.Asc()).
		All(ctx)
	if err != nil || len(filtered[0].Tags) != 1 || len(filtered[1].Tags) != 0 {
		t.Fatalf("filtered include: %v (err=%v)", filtered, err)
	}
	if filtered[1].Tags == nil {
		t.Fatal("загруженная пустая m2m-связь должна быть пустым слайсом")
	}

	// Any: пользователи с тегом go.
	withGo, err := sorm.Query[models.User](db).Where(u.Tags.Any(gen.Tag.Label.Eq("go"))).All(ctx)
	if err != nil || len(withGo) != 1 || withGo[0].Name != "Alice" {
		t.Fatalf("any: %v (err=%v)", withGo, err)
	}

	// Unlink.
	if err := u.Tags.Unlink(ctx, db, alice, dbTag); err != nil {
		t.Fatal(err)
	}
	after, err := sorm.Query[models.User](db).
		Where(u.Name.Eq("Alice")).
		With(u.Tags.Include()).
		One(ctx)
	if err != nil || len(after.Tags) != 1 || after.Tags[0].Label != "go" {
		t.Fatalf("после unlink: %v (err=%v)", after.Tags, err)
	}
}

func TestManyToManyAnySQL(t *testing.T) {
	u := gen.User
	sql, _ := sorm.Query[models.User](nil).Where(u.Tags.Any(gen.Tag.Label.Eq("go"))).ToSQL()
	want := `EXISTS (SELECT 1 FROM "user_tags" WHERE "user_id" = "users"."id"` +
		` AND "tag_id" IN (SELECT "id" FROM "tags" WHERE "label" = $1))`
	if !strings.Contains(sql, want) {
		t.Fatalf("sql:\n%s\nнет фрагмента:\n%s", sql, want)
	}
}
