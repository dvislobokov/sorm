package sorm_test

import (
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
)

func TestUpdateToSQL(t *testing.T) {
	sql, args, err := sorm.Update[models.User](nil).WithDeleted().
		Set(u.Active.Set(false), u.Name.Set("archived")). // Set(false) is a first-class assignment
		Where(u.Age.Lt(18)).
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// version is incremented automatically — open sessions will catch the conflict.
	want := `UPDATE "users" SET "active" = $1, "name" = $2, "version" = "version" + 1 WHERE "age" < $3`
	if sql != want {
		t.Errorf("got:  %s\nwant: %s", sql, want)
	}
	if len(args) != 3 || args[0] != false || args[1] != "archived" || args[2] != 18 {
		t.Errorf("args = %v", args)
	}
}

func TestUpdateWithoutWhereGuard(t *testing.T) {
	_, _, err := sorm.Update[models.User](nil).WithDeleted().Set(u.Active.Set(true)).ToSQL()
	if err == nil || !strings.Contains(err.Error(), "AllRows") {
		t.Fatalf("expected a guard error, got %v", err)
	}
	// Explicit opt-in works.
	sql, _, err := sorm.Update[models.User](nil).WithDeleted().Set(u.Active.Set(true)).AllRows().ToSQL()
	if err != nil || !strings.HasPrefix(sql, `UPDATE "users" SET`) {
		t.Fatalf("AllRows: %v / %s", err, sql)
	}
}

func TestUpdateWithoutSet(t *testing.T) {
	_, _, err := sorm.Update[models.User](nil).WithDeleted().Where(u.Age.Gt(1)).ToSQL()
	if err == nil {
		t.Fatal("expected an error for update without Set")
	}
}

func TestDeleteSQL(t *testing.T) {
	sql, args, err := sorm.Delete[models.User](nil).Hard().Where(u.Active.Eq(false)).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `DELETE FROM "users" WHERE "active" = $1`
	if sql != want || len(args) != 1 {
		t.Errorf("got %s %v", sql, args)
	}

	_, _, err = sorm.Delete[models.User](nil).Hard().ToSQL()
	if err == nil {
		t.Fatal("expected a guard error for delete without Where")
	}
}
