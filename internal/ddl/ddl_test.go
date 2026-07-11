package ddl_test

import (
	"path/filepath"
	"strings"
	"testing"

	"sorm/internal/ddl"
	"sorm/internal/parse"
)

func loadTestSchema(t *testing.T) *parse.Schema {
	t.Helper()
	s, err := parse.Load(filepath.Join("..", "testmodels"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParentsBeforeChildren(t *testing.T) {
	s := loadTestSchema(t)
	for _, d := range []string{"postgres", "mysql", "sqlite"} {
		sql, err := ddl.Generate(s, d)
		if err != nil {
			t.Fatal(err)
		}
		users := strings.Index(sql, "users")
		posts := strings.Index(sql, "posts")
		if users < 0 || posts < 0 || users > posts {
			t.Errorf("%s: users должна идти раньше posts (fk)", d)
		}
	}
}

func TestDialectSpecifics(t *testing.T) {
	s := loadTestSchema(t)

	pg, _ := ddl.Generate(s, "postgres")
	for _, want := range []string{
		`"id" BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY`,
		`"created_at" TIMESTAMPTZ NOT NULL`,
		`"avatar" BYTEA NULL`,
		`REFERENCES "users" ("id")`,
		`"body" TEXT NOT NULL`, // тег type:text
	} {
		if !strings.Contains(pg, want) {
			t.Errorf("postgres: нет %q", want)
		}
	}

	my, _ := ddl.Generate(s, "mysql")
	for _, want := range []string{
		"`id` BIGINT AUTO_INCREMENT PRIMARY KEY",
		"`email` VARCHAR(255) NOT NULL UNIQUE",
		"`created_at` DATETIME(6) NOT NULL",
	} {
		if !strings.Contains(my, want) {
			t.Errorf("mysql: нет %q", want)
		}
	}

	lite, _ := ddl.Generate(s, "sqlite")
	if !strings.Contains(lite, `"id" INTEGER PRIMARY KEY AUTOINCREMENT`) {
		t.Error("sqlite: auto-PK должен быть INTEGER PRIMARY KEY AUTOINCREMENT")
	}

	if _, err := ddl.Generate(s, "oracle"); err == nil {
		t.Error("неизвестный диалект должен давать ошибку")
	}
}
