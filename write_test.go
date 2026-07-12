package sorm_test

import (
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
)

func TestUpdateToSQL(t *testing.T) {
	sql, args, err := sorm.Update[models.User](nil).
		Set(u.Active.Set(false), u.Name.Set("archived")). // Set(false) — полноценное присваивание
		Where(u.Age.Lt(18)).
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// version инкрементируется автоматически — открытые сессии поймают конфликт.
	want := `UPDATE "users" SET "active" = $1, "name" = $2, "version" = "version" + 1 WHERE "age" < $3`
	if sql != want {
		t.Errorf("got:  %s\nwant: %s", sql, want)
	}
	if len(args) != 3 || args[0] != false || args[1] != "archived" || args[2] != 18 {
		t.Errorf("args = %v", args)
	}
}

func TestUpdateWithoutWhereGuard(t *testing.T) {
	_, _, err := sorm.Update[models.User](nil).Set(u.Active.Set(true)).ToSQL()
	if err == nil || !strings.Contains(err.Error(), "AllRows") {
		t.Fatalf("ожидали guard-ошибку, получили %v", err)
	}
	// Явное разрешение — работает.
	sql, _, err := sorm.Update[models.User](nil).Set(u.Active.Set(true)).AllRows().ToSQL()
	if err != nil || !strings.HasPrefix(sql, `UPDATE "users" SET`) {
		t.Fatalf("AllRows: %v / %s", err, sql)
	}
}

func TestUpdateWithoutSet(t *testing.T) {
	_, _, err := sorm.Update[models.User](nil).Where(u.Age.Gt(1)).ToSQL()
	if err == nil {
		t.Fatal("ожидали ошибку update без Set")
	}
}

func TestDeleteSQL(t *testing.T) {
	sql, args, err := sorm.Delete[models.User](nil).Where(u.Active.Eq(false)).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `DELETE FROM "users" WHERE "active" = $1`
	if sql != want || len(args) != 1 {
		t.Errorf("got %s %v", sql, args)
	}

	_, _, err = sorm.Delete[models.User](nil).ToSQL()
	if err == nil {
		t.Fatal("ожидали guard-ошибку delete без Where")
	}
}
