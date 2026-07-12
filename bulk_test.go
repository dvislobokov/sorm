package sorm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

func nowNoZero() time.Time { return time.Now() }

func TestBulkInsertSQLite(t *testing.T) { runBulkInsert(t, sqliteDB(t)) }
func TestBulkInsertPG(t *testing.T)     { runBulkInsert(t, testPool(t)) }
func TestBulkInsertMySQL(t *testing.T)  { runBulkInsert(t, mysqlDB(t)) }

// 1200 вставок одного типа в одном SaveChanges → 3 multi-row INSERT'а
// (лимит 500 строк на статимент), все PK проставлены и уникальны.
func runBulkInsert(t *testing.T, base sorm.DB) {
	ctx := context.Background()

	var insertStmts, insertRows int
	db := sorm.Instrument(base, func(ctx context.Context, op sorm.Op, next func(context.Context) error) error {
		if op.Kind == "batch" {
			for _, s := range op.Statements {
				if strings.HasPrefix(s, `INSERT INTO "users"`) || strings.HasPrefix(s, "INSERT INTO `users`") {
					insertStmts++
					insertRows += strings.Count(s, "(") - 1 // минус список колонок
				}
			}
		}
		return next(ctx)
	})

	const n = 1200
	s := sorm.NewSession(db)
	users := make([]*models.User, n)
	for i := range users {
		users[i] = &models.User{
			Email: fmt.Sprintf("bulk%d@b.c", i), Name: fmt.Sprintf("u%d", i), Active: true,
			CreatedAt: nowNoZero(),
		}
		sorm.Add(s, users[i])
	}
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	if insertStmts != 3 || insertRows != n {
		t.Fatalf("insert statements = %d (want 3), rows = %d (want %d)", insertStmts, insertRows, n)
	}

	seen := map[int64]bool{}
	for _, u := range users {
		if u.ID == 0 || seen[u.ID] {
			t.Fatalf("PK не проставлен или дублируется: %d", u.ID)
		}
		seen[u.ID] = true
	}

	cnt, err := sorm.Query[models.User](db).Count(ctx)
	if err != nil || cnt != n {
		t.Fatalf("в БД %d строк (err=%v), want %d", cnt, err, n)
	}

	// PK соответствуют строкам: точечная проверка порядка.
	u0, err := sorm.Query[models.User](db).Where(gen.User.ID.Eq(users[0].ID)).One(ctx)
	if err != nil || u0.Email != "bulk0@b.c" {
		t.Fatalf("id→строка не совпали: %+v (err=%v)", u0, err)
	}
	uLast, err := sorm.Query[models.User](db).Where(gen.User.ID.Eq(users[n-1].ID)).One(ctx)
	if err != nil || uLast.Email != fmt.Sprintf("bulk%d@b.c", n-1) {
		t.Fatalf("id→строка не совпали для последней: %+v (err=%v)", uLast, err)
	}
}
