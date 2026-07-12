package sorm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// The generated Context: tracked reads, identity map, Add/Remove via
// sets, Find, NoTracking, and RunInTx atomicity.

func TestContextTrackedReadAndSave(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	c.Users.Add(&models.User{Email: "ctx@a.b", Name: "Old", Active: true, CreatedAt: nowNoZero()})
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// New unit of work: read through the set → tracked, mutate, save.
	c2 := gen.NewContext(db)
	u, err := c2.Users.Where(gen.User.Email.Eq("ctx@a.b")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.Name = "New"
	if err := c2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := gen.NewContext(db).Users.Where(gen.User.Email.Eq("ctx@a.b")).One(ctx)
	if err != nil || got.Name != "New" {
		t.Fatalf("tracked update not persisted: %+v (err=%v)", got, err)
	}
}

func TestContextIdentityMap(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	c.Users.Add(&models.User{Email: "id@a.b", Name: "A", Active: true, CreatedAt: nowNoZero()})
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	c2 := gen.NewContext(db)
	a, err := c2.Users.Where(gen.User.Email.Eq("id@a.b")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.Name = "local change"
	b, err := c2.Users.Where(gen.User.Email.Eq("id@a.b")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("same row must map to the same pointer")
	}
	if b.Name != "local change" {
		t.Fatal("database data must not overwrite local changes")
	}
}

func TestContextFind(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	u := &models.User{Email: "find@a.b", Name: "F", Active: true, CreatedAt: nowNoZero()}
	c.Users.Add(u)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	c2 := gen.NewContext(db)
	got, err := c2.Users.Find(ctx, u.ID)
	if err != nil || got.Email != "find@a.b" {
		t.Fatalf("Find by PK: %+v (err=%v)", got, err)
	}
	// Second Find hits the identity map — same pointer, and int works too.
	again, err := c2.Users.Find(ctx, int(u.ID))
	if err != nil || again != got {
		t.Fatalf("Find must return the tracked pointer (err=%v)", err)
	}
	if _, err := c2.Users.Find(ctx, int64(999999)); !errors.Is(err, sorm.ErrNotFound) {
		t.Fatalf("missing row: want ErrNotFound, got %v", err)
	}
}

func TestContextNoTracking(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	c.Users.Add(&models.User{Email: "nt@a.b", Name: "Same", Active: true, CreatedAt: nowNoZero()})
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	c2 := gen.NewContext(db)
	u, err := c2.Users.NoTracking().Where(gen.User.Email.Eq("nt@a.b")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.Name = "mutated"
	if err := c2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := c2.Users.NoTracking().Where(gen.User.Email.Eq("nt@a.b")).One(ctx)
	if got.Name != "Same" {
		t.Fatal("NoTracking entity must not be flushed")
	}
}

func TestContextRemove(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	c.Users.Add(&models.User{Email: "rm@a.b", Name: "R", Active: true, CreatedAt: nowNoZero()})
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	c2 := gen.NewContext(db)
	u, err := c2.Users.Where(gen.User.Email.Eq("rm@a.b")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c2.Users.Remove(u)
	if err := c2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := c2.Users.NoTracking().Where(gen.User.Email.Eq("rm@a.b")).Count(ctx); n != 0 {
		t.Fatal("entity not deleted")
	}
}

// One session, several sequential SaveChanges: a flushed entity must not
// be re-inserted, re-updated or re-deleted by the next flush.
func TestContextMultipleSaveChanges(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	u := &models.User{Email: "multi@a.b", Name: "S1", Active: true, CreatedAt: nowNoZero()}
	c.Users.Add(u)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	// Flush 2: no pending changes — must be a no-op, not a duplicate INSERT.
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	// Flush 3: the inserted entity is tracked now — mutation becomes UPDATE.
	u.Name = "S3"
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	// Flush 4: delete it; flush 5 must not re-run the DELETE.
	c.Users.Remove(u)
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := c.Users.NoTracking().Count(ctx); n != 0 {
		t.Fatalf("want empty table, got %d rows", n)
	}
}

func TestContextRunInTxRollback(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	boom := errors.New("boom")
	err := c.RunInTx(ctx, func(txc *gen.Context) error {
		txc.Users.Add(&models.User{Email: "tx1@a.b", Name: "T1", Active: true, CreatedAt: nowNoZero()})
		if err := txc.SaveChanges(ctx); err != nil {
			return err
		}
		// The first SaveChanges is flushed but must roll back with the tx.
		txc.Users.Add(&models.User{Email: "tx2@a.b", Name: "T2", Active: true, CreatedAt: nowNoZero()})
		if err := txc.SaveChanges(ctx); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if n, _ := c.Users.NoTracking().Count(ctx); n != 0 {
		t.Fatalf("rollback must discard both flushes, %d rows left", n)
	}
}

func TestContextRunInTxCommit(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	c := gen.NewContext(db)
	err := c.RunInTx(ctx, func(txc *gen.Context) error {
		txc.Users.Add(&models.User{Email: "cm1@a.b", Name: "C1", Active: true, CreatedAt: nowNoZero()})
		if err := txc.SaveChanges(ctx); err != nil {
			return err
		}
		u, err := txc.Users.Where(gen.User.Email.Eq("cm1@a.b")).One(ctx)
		if err != nil {
			return err
		}
		u.Name = "C1 updated"
		return txc.SaveChanges(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Users.NoTracking().Where(gen.User.Email.Eq("cm1@a.b")).One(ctx)
	if err != nil || got.Name != "C1 updated" {
		t.Fatalf("commit lost data: %+v (err=%v)", got, err)
	}
}
