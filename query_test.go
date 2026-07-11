package sorm_test

import (
	"testing"

	"sorm"
	models "sorm/internal/testmodels"
	gen "sorm/internal/testmodels/sormgen"
)

// Тесты работают на коде, сгенерированном `sorm gen` из internal/testmodels.
var u = gen.User

const allCols = `"id", "email", "name", "nickname", "active", "age", "balance", "avatar", "created_at", "deleted_at", "version"`

func assertSQL(t *testing.T, q sorm.QueryBuilder[models.User], wantSQL string, wantArgs ...any) {
	t.Helper()
	gotSQL, gotArgs := q.ToSQL()
	if gotSQL != wantSQL {
		t.Errorf("SQL:\n got:  %s\n want: %s", gotSQL, wantSQL)
	}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Errorf("arg[%d]: got %v, want %v", i, gotArgs[i], wantArgs[i])
		}
	}
}

func TestSelectAll(t *testing.T) {
	assertSQL(t, sorm.Query[models.User](nil),
		`SELECT `+allCols+` FROM "users"`)
}

func TestWhereZeroValues(t *testing.T) {
	// Главный анти-GORM тест: false и 0 — полноценные условия.
	assertSQL(t,
		sorm.Query[models.User](nil).Where(u.Active.Eq(false), u.Age.Eq(0)),
		`SELECT `+allCols+` FROM "users" WHERE ("active" = $1 AND "age" = $2)`,
		false, 0)
}

func TestWhereComposition(t *testing.T) {
	q := sorm.Query[models.User](nil).
		Where(u.Active.Eq(true)).
		Where(sorm.Or(u.Age.Gte(18), u.Name.HasPrefix("adm"))).
		OrderBy(u.Name.Asc(), u.ID.Desc()).
		Limit(50).
		Offset(10)
	assertSQL(t, q,
		`SELECT `+allCols+` FROM "users"`+
			` WHERE ("active" = $1 AND ("age" >= $2 OR "name" LIKE $3))`+
			` ORDER BY "name", "id" DESC LIMIT 50 OFFSET 10`,
		true, 18, "adm%")
}

func TestBuilderImmutability(t *testing.T) {
	base := sorm.Query[models.User](nil).Where(u.Active.Eq(true))
	q1 := base.Where(u.Age.Gt(30))
	q2 := base.Where(u.Name.Eq("bob")).Limit(1)

	assertSQL(t, base,
		`SELECT `+allCols+` FROM "users" WHERE "active" = $1`, true)
	assertSQL(t, q1,
		`SELECT `+allCols+` FROM "users" WHERE ("active" = $1 AND "age" > $2)`, true, 30)
	assertSQL(t, q2,
		`SELECT `+allCols+` FROM "users" WHERE ("active" = $1 AND "name" = $2) LIMIT 1`, true, "bob")
}

func TestInEmpty(t *testing.T) {
	assertSQL(t, sorm.Query[models.User](nil).Where(u.Age.In()),
		`SELECT `+allCols+` FROM "users" WHERE FALSE`)
	assertSQL(t, sorm.Query[models.User](nil).Where(u.Age.NotIn()),
		`SELECT `+allCols+` FROM "users" WHERE TRUE`)
}

func TestIn(t *testing.T) {
	assertSQL(t, sorm.Query[models.User](nil).Where(u.ID.In(1, 2, 3)),
		`SELECT `+allCols+` FROM "users" WHERE "id" IN ($1, $2, $3)`,
		int64(1), int64(2), int64(3))
}

func TestLikeEscaping(t *testing.T) {
	// Литерал с % и _ экранируется, пользовательский Like — нет.
	assertSQL(t, sorm.Query[models.User](nil).Where(u.Name.Contains("50%_off")),
		`SELECT `+allCols+` FROM "users" WHERE "name" LIKE $1`,
		`%50\%\_off%`)
	assertSQL(t, sorm.Query[models.User](nil).Where(u.Name.Like("50%")),
		`SELECT `+allCols+` FROM "users" WHERE "name" LIKE $1`,
		"50%")
}

func TestBetweenAndNull(t *testing.T) {
	assertSQL(t, sorm.Query[models.User](nil).Where(u.Age.Between(18, 65), u.Nickname.IsNotNull()),
		`SELECT `+allCols+` FROM "users" WHERE ("age" BETWEEN $1 AND $2 AND "nickname" IS NOT NULL)`,
		18, 65)
}

func TestNot(t *testing.T) {
	assertSQL(t, sorm.Query[models.User](nil).Where(sorm.Not(u.Active.Eq(true))),
		`SELECT `+allCols+` FROM "users" WHERE NOT ("active" = $1)`, true)
}
