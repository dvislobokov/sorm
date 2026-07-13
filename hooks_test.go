package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// Comment implements BeforeSave (trim + veto marker) and AfterLoad
// (sets the non-column Loaded flag).

func TestHooks(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()
	c := gen.NewContext(db)

	author := &models.User{Email: "hooks@a.b", Name: "H", Active: true, CreatedAt: nowNoZero()}
	post := &models.Post{Author: author, Title: "p"}
	c.Users.Add(author)
	c.Posts.Add(post)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// BeforeSave on insert: mutation (trim) is what gets persisted.
	cm := &models.Comment{Post: post, Body: "  padded  "}
	c.Comments.Add(cm)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if cm.Body != "padded" {
		t.Fatalf("hook mutation lost: %q", cm.Body)
	}
	got, err := c.Comments.NoTracking().Where(gen.Comment.ID.Eq(cm.ID)).One(ctx)
	if err != nil || got.Body != "padded" {
		t.Fatalf("persisted %q (err=%v)", got.Body, err)
	}

	// AfterLoad fires on materialization (NoTracking query above included).
	if !got.Loaded {
		t.Fatal("AfterLoad must set Loaded")
	}

	// Veto on insert aborts the flush before any SQL.
	bad := &models.Comment{Post: post, Body: "!!veto!!"}
	c2 := gen.NewContext(db)
	c2.Comments.Add(bad)
	err = c2.SaveChanges(ctx)
	if err == nil || !strings.Contains(err.Error(), "comment veto on insert") {
		t.Fatalf("want insert veto, got %v", err)
	}
	if n, _ := c.Comments.NoTracking().Where(gen.Comment.Body.Eq("!!veto!!")).Count(ctx); n != 0 {
		t.Fatal("vetoed insert must not reach the database")
	}

	// BeforeSave on update: fires only for effective changes, mutation
	// joins the diff.
	tracked, err := c.Comments.Where(gen.Comment.ID.Eq(cm.ID)).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// No-op flush: hook must NOT veto (body unchanged, no diff).
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	tracked.Body = "  edited  "
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	again, _ := c.Comments.NoTracking().Where(gen.Comment.ID.Eq(cm.ID)).One(ctx)
	if again.Body != "edited" {
		t.Fatalf("update hook mutation: %q", again.Body)
	}

	// Veto on update.
	tracked2, _ := c.Comments.Where(gen.Comment.ID.Eq(cm.ID)).One(ctx)
	tracked2.Body = "!!veto!!"
	err = c.SaveChanges(ctx)
	if err == nil || !strings.Contains(err.Error(), "comment veto on update") {
		t.Fatalf("want update veto, got %v", err)
	}
	tracked2.Body = "recovered"
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// Veto on delete.
	tracked2.Body = "!!veto!!" // marker set in memory only
	c.Comments.Remove(tracked2)
	err = c.SaveChanges(ctx)
	if err == nil || !strings.Contains(err.Error(), "comment veto on delete") {
		t.Fatalf("want delete veto, got %v", err)
	}

	// AfterLoad in Iter and Raw.
	for e, err := range sorm.Query[models.Comment](db).Iter(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if !e.Loaded {
			t.Fatal("Iter must fire AfterLoad")
		}
	}
	raws, err := sorm.Raw[models.Comment](db, `SELECT * FROM comments`).All(ctx)
	if err != nil || len(raws) == 0 || !raws[0].Loaded {
		t.Fatalf("Raw must fire AfterLoad (err=%v)", err)
	}
}
