package sorm_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// Nullable (*int64) FK: value-keyed relation maps, address-of fixup,
// SetNull for set-based writes.
func TestNullableFKSQLite(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()
	u, p, c := gen.User, gen.Post, gen.Comment

	s := sorm.NewSession(db)
	author := &models.User{Email: "a@b.c", Name: "Alice", Active: true}
	post := &models.Post{Author: author, Title: "t", Body: ""}
	attached := &models.Comment{Post: post, Body: "attached"} // FK via navigation
	orphan := &models.Comment{Body: "orphan"}                 // NULL FK
	sorm.Add(s, author)
	sorm.Add(s, post)
	sorm.Add(s, attached, orphan)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if attached.PostID == nil || *attached.PostID != post.ID {
		t.Fatalf("pointer-FK fixup failed: %v", attached.PostID)
	}
	if orphan.PostID != nil {
		t.Fatalf("orphan got a PostID: %v", *orphan.PostID)
	}

	// belongsTo Include over a pointer FK: value-keyed map must match.
	// Note: the orphan has no insert dependencies, so it lands in an earlier
	// flush level and gets the smaller ID — look comments up by body.
	comments, err := sorm.Query[models.Comment](db).
		With(c.Post.Include()).
		All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byBody := map[string]*models.Comment{}
	for _, cm := range comments {
		byBody[cm.Body] = cm
	}
	if got := byBody["attached"]; got.Post == nil || got.Post.ID != post.ID {
		t.Fatalf("attached comment did not get its post: %+v", got.Post)
	}
	if byBody["orphan"].Post != nil {
		t.Fatal("orphan comment must keep a nil post")
	}

	// hasMany Include over the same pointer FK.
	posts, err := sorm.Query[models.Post](db).With(p.Comments.Include()).All(ctx)
	if err != nil || len(posts) != 1 || len(posts[0].Comments) != 1 {
		t.Fatalf("hasMany over nullable FK: %v (err=%v)", posts, err)
	}

	// Is predicate and IsNull.
	withPost, err := sorm.Query[models.Comment](db).Where(c.Post.Is(p.Title.Eq("t"))).All(ctx)
	if err != nil || len(withPost) != 1 {
		t.Fatalf("Is: %d (err=%v)", len(withPost), err)
	}
	orphans, err := sorm.Query[models.Comment](db).Where(c.PostID.IsNull()).All(ctx)
	if err != nil || len(orphans) != 1 || orphans[0].Body != "orphan" {
		t.Fatalf("IsNull: %v (err=%v)", orphans, err)
	}

	// SetNull: set-based detach.
	n, err := sorm.Update[models.Comment](db).
		Set(c.PostID.SetNull()).
		Where(c.ID.Eq(attached.ID)).
		Exec(ctx)
	if err != nil || n != 1 {
		t.Fatalf("SetNull update: n=%d err=%v", n, err)
	}
	detached, err := sorm.Query[models.Comment](db).Where(c.PostID.IsNull()).Count(ctx)
	if err != nil || detached != 2 {
		t.Fatalf("after SetNull: %d NULL FKs, want 2", detached)
	}

	// Session path: value -> nil transition through the diff.
	s2 := sorm.NewSession(db)
	id := post.ID
	reattach, err := sorm.Track[models.Comment](s2).Where(c.ID.Eq(orphan.ID)).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reattach.PostID = &id
	if err := s2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := sorm.Query[models.Comment](db).Where(c.PostID.IsNotNull()).Count(ctx); n != 1 {
		t.Fatalf("re-attach via session failed: %d", n)
	}
	_ = u
}

// Native uuid.UUID: client-assigned PK, predicates, identity map,
// nullable UUID column, SetNull.
func TestUUIDSQLite(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()
	u, d := gen.User, gen.Device

	s := sorm.NewSession(db)
	owner := &models.User{Email: "o@b.c", Name: "Owner", Active: true}
	token := uuid.New()
	dev := &models.Device{ID: uuid.New(), Owner: owner, Token: &token, Name: "laptop"}
	dev2 := &models.Device{ID: uuid.New(), Owner: owner, Name: "phone"} // Token NULL
	sorm.Add(s, owner)
	sorm.Add(s, dev, dev2)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if dev.OwnerID != owner.ID {
		t.Fatalf("FK fixup: %d != %d", dev.OwnerID, owner.ID)
	}

	// Typed predicates: Eq and In over uuid.UUID values.
	got, err := sorm.Query[models.Device](db).Where(d.ID.Eq(dev.ID)).One(ctx)
	if err != nil || got.Name != "laptop" || got.ID != dev.ID {
		t.Fatalf("Eq by uuid PK: %+v (err=%v)", got, err)
	}
	both, err := sorm.Query[models.Device](db).Where(d.ID.In(dev.ID, dev2.ID)).All(ctx)
	if err != nil || len(both) != 2 {
		t.Fatalf("In by uuid: %d (err=%v)", len(both), err)
	}

	// Nullable UUID column round-trip + IsNull.
	if got.Token == nil || *got.Token != token {
		t.Fatalf("uuid token round-trip: %v", got.Token)
	}
	noToken, err := sorm.Query[models.Device](db).Where(d.Token.IsNull()).All(ctx)
	if err != nil || len(noToken) != 1 || noToken[0].Name != "phone" {
		t.Fatalf("IsNull uuid: %v (err=%v)", noToken, err)
	}

	// Identity map keys by uuid value; diff catches uuid field changes.
	s2 := sorm.NewSession(db)
	a, _ := sorm.Track[models.Device](s2).Where(d.ID.Eq(dev.ID)).One(ctx)
	b, _ := sorm.Track[models.Device](s2).Where(d.Name.Eq("laptop")).One(ctx)
	if a != b {
		t.Fatal("identity map by uuid PK failed")
	}
	fresh := uuid.New()
	a.Token = &fresh
	if err := s2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	re, _ := sorm.Query[models.Device](db).Where(d.ID.Eq(dev.ID)).One(ctx)
	if re.Token == nil || *re.Token != fresh {
		t.Fatalf("uuid update: %v", re.Token)
	}

	// SetNull on the uuid column.
	if _, err := sorm.Update[models.Device](db).
		Set(d.Token.SetNull()).
		Where(d.ID.Eq(dev.ID)).
		Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := sorm.Query[models.Device](db).Where(d.Token.IsNull()).Count(ctx); n != 2 {
		t.Fatalf("SetNull uuid: %d NULL tokens, want 2", n)
	}

	// belongsTo from Device to User (int64 FK) still works alongside.
	withOwner, err := sorm.Query[models.Device](db).
		Where(d.Owner.Is(u.Email.Eq("o@b.c"))).
		Count(ctx)
	if err != nil || withOwner != 2 {
		t.Fatalf("Is over owner: %d (err=%v)", withOwner, err)
	}
}
