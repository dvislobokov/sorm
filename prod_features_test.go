package sorm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// --- ConstraintError: типизированные ошибки БД на всех диалектах ---

func TestConstraintErrorsSQLite(t *testing.T) { runConstraintScenario(t, sqliteDB(t)) }
func TestConstraintErrorsMySQL(t *testing.T)  { runConstraintScenario(t, mysqlDB(t)) }
func TestConstraintErrorsPG(t *testing.T)     { runConstraintScenario(t, testPool(t)) }

func runConstraintScenario(t *testing.T, db sorm.DB) {
	ctx := context.Background()

	now := time.Now()
	s := sorm.NewSession(db)
	sorm.Add(s, &models.User{Email: "dup@b.c", Name: "One", Active: true, CreatedAt: now})
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// Дубликат unique email → ConstraintUnique, а не сырая ошибка драйвера.
	s2 := sorm.NewSession(db)
	sorm.Add(s2, &models.User{Email: "dup@b.c", Name: "Two", Active: true, CreatedAt: now})
	err := s2.SaveChanges(ctx)
	if !sorm.IsUniqueViolation(err) {
		t.Fatalf("ожидали unique violation, получили %v", err)
	}
	var ce *sorm.ConstraintError
	if !errors.As(err, &ce) || ce.Kind != sorm.ConstraintUnique {
		t.Fatalf("ce = %+v", ce)
	}
}

// --- RunInTx: commit, rollback, сессия поверх транзакции ---

func TestRunInTxSQLite(t *testing.T) { runTxScenario(t, sqliteDB(t)) }
func TestRunInTxPG(t *testing.T)     { runTxScenario(t, testPool(t)) }

func runTxScenario(t *testing.T, db sorm.DB) {
	ctx := context.Background()

	// Commit: сессия внутри RunInTx делает flush в ту же транзакцию.
	err := sorm.RunInTx(ctx, db, func(tx sorm.Tx) error {
		s := sorm.NewSession(tx)
		sorm.Add(s, &models.User{Email: "tx@b.c", Name: "Tx", Active: true})
		return s.SaveChanges(ctx) // flush без вложенной транзакции
	})
	if err != nil {
		t.Fatal(err)
	}
	n, _ := sorm.Query[models.User](db).Where(gen.User.Email.Eq("tx@b.c")).Count(ctx)
	if n != 1 {
		t.Fatalf("после commit users=%d", n)
	}

	// Rollback: ошибка из fn откатывает всё.
	boom := errors.New("boom")
	err = sorm.RunInTx(ctx, db, func(tx sorm.Tx) error {
		s := sorm.NewSession(tx)
		sorm.Add(s, &models.User{Email: "rollback@b.c", Name: "RB", Active: true})
		if err := s.SaveChanges(ctx); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	n, _ = sorm.Query[models.User](db).Where(gen.User.Email.Eq("rollback@b.c")).Count(ctx)
	if n != 0 {
		t.Fatalf("после rollback users=%d, want 0", n)
	}
}

// --- BelongsTo: Include и Is ---

func TestBelongsToIsSQL(t *testing.T) {
	p := gen.Post
	assertPostSQL(t,
		sorm.Query[models.Post](nil).Where(p.Author.Is(u.Active.Eq(true))),
		`EXISTS (SELECT 1 FROM "users" WHERE "id" = "posts"."author_id" AND "active" = $1)`)
}

func assertPostSQL(t *testing.T, q sorm.QueryBuilder[models.Post], wantFragment string) {
	t.Helper()
	sql, _ := q.ToSQL()
	if !strings.Contains(sql, wantFragment) {
		t.Errorf("SQL %s\nне содержит %s", sql, wantFragment)
	}
}

func TestBelongsToIncludeSQLite(t *testing.T) {
	db := sqliteDB(t)
	ctx := context.Background()

	s := sorm.NewSession(db)
	alice := &models.User{Email: "a@b.c", Name: "Alice", Active: true}
	bob := &models.User{Email: "b@b.c", Name: "Bob", Active: false}
	sorm.Add(s, alice, bob)
	sorm.Add(s,
		&models.Post{Author: alice, Title: "t1", Body: ""},
		&models.Post{Author: alice, Title: "t2", Body: ""},
		&models.Post{Author: bob, Title: "t3", Body: ""},
	)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	p := gen.Post
	posts, err := sorm.Query[models.Post](db).
		With(p.Author.Include()).
		OrderBy(p.ID.Asc()).
		All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 3 || posts[0].Author == nil || posts[0].Author.Name != "Alice" ||
		posts[2].Author == nil || posts[2].Author.Name != "Bob" {
		t.Fatalf("belongsTo include: %+v", posts)
	}
	// Один и тот же родитель — один указатель (раскладка по PK).
	if posts[0].Author != posts[1].Author {
		t.Fatal("родитель Alice должен быть одним объектом")
	}

	// Is: посты активных авторов.
	activePosts, err := sorm.Query[models.Post](db).Where(p.Author.Is(u.Active.Eq(true))).All(ctx)
	if err != nil || len(activePosts) != 2 {
		t.Fatalf("Is: %d постов (err=%v), want 2", len(activePosts), err)
	}
}

// --- Instrument: перехват SQL ---

func TestInstrument(t *testing.T) {
	base := sqliteDB(t)
	ctx := context.Background()

	var ops []sorm.Op
	db := sorm.Instrument(base, func(ctx context.Context, op sorm.Op, next func(context.Context) error) error {
		ops = append(ops, op)
		return next(ctx)
	})

	s := sorm.NewSession(db)
	sorm.Add(s, &models.User{Email: "i@b.c", Name: "I", Active: true})
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sorm.Query[models.User](db).Where(u.Active.Eq(true)).All(ctx); err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, op.Kind)
	}
	joined := strings.Join(kinds, ",")
	// SaveChanges: begin → batch → commit; затем query.
	if !strings.Contains(joined, "begin,batch,commit") || !strings.Contains(joined, "query") {
		t.Fatalf("kinds = %v", kinds)
	}
	for _, op := range ops {
		if op.Kind == "batch" && !strings.Contains(op.SQL, "INSERT INTO") {
			t.Fatalf("batch op.SQL = %q", op.SQL)
		}
		if op.Kind == "query" && !strings.Contains(op.SQL, "SELECT") {
			t.Fatalf("query op.SQL = %q", op.SQL)
		}
	}
}
