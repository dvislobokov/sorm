package sorm_test

import (
	"context"
	"testing"
	"time"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// Auto-timestamps: autoCreate stamps on insert (a manual value wins),
// autoUpdate stamps on insert and on every effective update — and only then.

func TestAutoTimestampsInsert(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()
	c := gen.NewContext(db)

	author := &models.User{Email: "ts@a.b", Name: "T", Active: true, CreatedAt: nowNoZero()}
	post := &models.Post{Author: author, Title: "stamped", Body: "b"}
	manual := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	postManual := &models.Post{Author: author, Title: "manual", Body: "b", CreatedAt: manual}
	c.Users.Add(author)
	c.Posts.Add(post, postManual)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	if post.CreatedAt.IsZero() || post.UpdatedAt.IsZero() {
		t.Fatalf("insert must stamp both: created=%v updated=%v", post.CreatedAt, post.UpdatedAt)
	}
	if !post.CreatedAt.Equal(post.UpdatedAt) {
		t.Fatal("one flush — one timestamp for created and updated")
	}
	if !postManual.CreatedAt.Equal(manual) {
		t.Fatalf("manual CreatedAt must win, got %v", postManual.CreatedAt)
	}

	// And it is actually persisted, not only in memory.
	got, err := c.Posts.NoTracking().Where(gen.Post.Title.Eq("stamped")).One(ctx)
	if err != nil || got.CreatedAt.IsZero() {
		t.Fatalf("persisted CreatedAt is zero (err=%v)", err)
	}
}

func TestAutoTimestampsUpdate(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()
	c := gen.NewContext(db)

	author := &models.User{Email: "tsu@a.b", Name: "T", Active: true, CreatedAt: nowNoZero()}
	post := &models.Post{Author: author, Title: "v1", Body: "b"}
	c.Users.Add(author)
	c.Posts.Add(post)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	created, stamped := post.CreatedAt, post.UpdatedAt

	// No-op flush: nothing changed — UpdatedAt must stay put.
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if !post.UpdatedAt.Equal(stamped) {
		t.Fatal("no-op SaveChanges must not touch UpdatedAt")
	}

	// Effective update: UpdatedAt moves, CreatedAt does not.
	time.Sleep(5 * time.Millisecond) // sqlite stores µs — ensure a distinct stamp
	post.Title = "v2"
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if !post.UpdatedAt.After(stamped) {
		t.Fatalf("UpdatedAt must advance: was %v, now %v", stamped, post.UpdatedAt)
	}
	if !post.CreatedAt.Equal(created) {
		t.Fatal("update must not touch CreatedAt")
	}

	// Persisted value matches.
	got, err := sorm.Query[models.Post](db).Where(gen.Post.Title.Eq("v2")).One(ctx)
	if err != nil || !got.UpdatedAt.Equal(post.UpdatedAt.Truncate(time.Microsecond)) && !got.UpdatedAt.Equal(post.UpdatedAt) {
		t.Fatalf("persisted UpdatedAt mismatch: %v vs %v (err=%v)", got.UpdatedAt, post.UpdatedAt, err)
	}
}
