package sorm_test

import (
	"context"
	"testing"

	"sorm"
	models "sorm/internal/testmodels"
	gen "sorm/internal/testmodels/sormgen"
)

func TestHasOneSQLite(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()
	u := gen.User

	s := sorm.NewSession(db)
	alice := &models.User{Email: "a@b.c", Name: "Alice", Active: true}
	bob := &models.User{Email: "b@b.c", Name: "Bob", Active: true}
	sorm.Add(s, alice, bob)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	s2 := sorm.NewSession(db)
	sorm.Add(s2, &models.Profile{UserID: alice.ID, Bio: "gopher"})
	if err := s2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// Include: у Alice профиль есть, у Bob — nil.
	users, err := sorm.Query[models.User](db).
		With(u.Profile.Include()).
		OrderBy(u.Name.Asc()).
		All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if users[0].Profile == nil || users[0].Profile.Bio != "gopher" {
		t.Fatalf("alice profile: %+v", users[0].Profile)
	}
	if users[1].Profile != nil {
		t.Fatalf("bob profile должен быть nil: %+v", users[1].Profile)
	}

	// Any / None.
	with, err := sorm.Query[models.User](db).Where(u.Profile.Any()).All(ctx)
	if err != nil || len(with) != 1 || with[0].Name != "Alice" {
		t.Fatalf("Any: %v (err=%v)", with, err)
	}
	without, err := sorm.Query[models.User](db).Where(u.Profile.None()).All(ctx)
	if err != nil || len(without) != 1 || without[0].Name != "Bob" {
		t.Fatalf("None: %v (err=%v)", without, err)
	}
}

// Вложенный Include: посты → авторы → профили авторов, в один With.
func TestNestedInclude(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()
	u, p := gen.User, gen.Post

	s := sorm.NewSession(db)
	alice := &models.User{Email: "a@b.c", Name: "Alice", Active: true}
	sorm.Add(s, alice)
	sorm.Add(s,
		&models.Post{Author: alice, Title: "b-second", Body: ""},
		&models.Post{Author: alice, Title: "a-first", Body: ""},
	)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	s2 := sorm.NewSession(db)
	sorm.Add(s2, &models.Profile{UserID: alice.ID, Bio: "nested"})
	if err := s2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// users → Posts (с сортировкой детей) ; posts → Author → Profile.
	users, err := sorm.Query[models.User](db).
		With(u.Posts.Include(
			p.Title.Asc(),                            // Order[C] как опция
			p.Author.Include(u.Profile.Include()),    // ThenInclude двух уровней
		)).
		All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || len(users[0].Posts) != 2 {
		t.Fatalf("users/posts: %+v", users)
	}
	if users[0].Posts[0].Title != "a-first" {
		t.Fatalf("Order[C]-опция не сработала: %q", users[0].Posts[0].Title)
	}
	author := users[0].Posts[0].Author
	if author == nil || author.Profile == nil || author.Profile.Bio != "nested" {
		t.Fatalf("вложенный Include: author=%+v", author)
	}
	// identity map не задействован (без Track), но раскладка по PK даёт
	// один объект автора на оба поста.
	if users[0].Posts[0].Author != users[0].Posts[1].Author {
		t.Fatal("оба поста должны ссылаться на один объект автора")
	}
}