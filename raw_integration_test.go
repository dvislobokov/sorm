package sorm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

func TestRawIntoEntity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)

	users, err := sorm.Raw[models.User](pool,
		`SELECT * FROM users WHERE age >= $1`, 18).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Name != "Alice" {
		t.Fatalf("users = %+v", users)
	}
}

func TestRawStrictMismatch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)

	// Частичный SELECT в сущность — не тихо полупустой объект, а ScanError.
	_, err := sorm.Raw[models.User](pool, `SELECT id, name FROM users`).All(ctx)
	var se *sorm.ScanError
	if !errors.As(err, &se) {
		t.Fatalf("ожидали ScanError, получили %v", err)
	}
	if len(se.Extra) == 0 {
		t.Fatalf("ScanError.Extra пуст: %v", se)
	}
}

type ageStat struct {
	Age int
	N   int64 `sorm:"n"`
}

func TestRawAsAggregate(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)
	mustExec(t, pool, `INSERT INTO users (email, name, active, age, balance, created_at, version)
		VALUES ('b@b.c', 'Bob', true, 30, 0, now(), 1), ('c@b.c', 'Cid', true, 40, 0, now(), 1)`)

	stats, err := sorm.RawAs[ageStat](pool,
		`SELECT age, count(*) AS n FROM users GROUP BY age ORDER BY age`).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].Age != 30 || stats[0].N != 2 || stats[1].Age != 40 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestSetBasedUpdateDelete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAlice(t, pool)

	// Set-based UPDATE бампает version → открытая сессия ловит конфликт.
	s := sorm.NewSession(pool)
	tracked, err := sorm.Track[models.User](s).Where(gen.User.Email.Eq("a@b.c")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}

	n, err := sorm.Update[models.User](pool).
		Set(gen.User.Active.Set(false)).
		Where(gen.User.Age.Gte(18)).
		Exec(ctx)
	if err != nil || n != 1 {
		t.Fatalf("update: n=%d err=%v", n, err)
	}

	tracked.Name = "stale write"
	err = s.SaveChanges(ctx)
	var conflict *sorm.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("сессия не поймала конфликт после set-based update: %v", err)
	}

	n, err = sorm.Delete[models.User](pool).Where(gen.User.Active.Eq(false)).Exec(ctx)
	if err != nil || n != 1 {
		t.Fatalf("delete: n=%d err=%v", n, err)
	}
}
