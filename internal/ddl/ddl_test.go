package ddl_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/internal/ddl"
	"github.com/dvislobokov/sorm/internal/parse"
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
			t.Errorf("%s: users must come before posts (fk)", d)
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
		`"body" TEXT NOT NULL`, // type:text tag
	} {
		if !strings.Contains(pg, want) {
			t.Errorf("postgres: missing %q", want)
		}
	}

	my, _ := ddl.Generate(s, "mysql")
	for _, want := range []string{
		"`id` BIGINT AUTO_INCREMENT PRIMARY KEY",
		"`email` VARCHAR(255) NOT NULL UNIQUE",
		"`created_at` DATETIME(6) NOT NULL",
	} {
		if !strings.Contains(my, want) {
			t.Errorf("mysql: missing %q", want)
		}
	}

	lite, _ := ddl.Generate(s, "sqlite")
	if !strings.Contains(lite, `"id" INTEGER PRIMARY KEY AUTOINCREMENT`) {
		t.Error("sqlite: auto-PK must be INTEGER PRIMARY KEY AUTOINCREMENT")
	}

	if _, err := ddl.Generate(s, "oracle"); err == nil {
		t.Error("unknown dialect must return an error")
	}
}

func TestCustomIndexDDL(t *testing.T) {
	s := loadTestSchema(t)

	// The parser only sees User's Indexes() method as a flag; it is executed
	// by generated code only, so exotic-index DDL is built directly here.
	for _, e := range s.Entities {
		if e.Name == "User" && !e.HasIndexesMethod {
			t.Error("HasIndexesMethod not detected")
		}
	}

	pg, err := ddl.Generate(s, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	// the tag-defined composite index is present
	if !strings.Contains(pg, `CREATE INDEX "idx_posts_author_title" ON "posts" ("author_id", "title");`) {
		t.Errorf("composite index missing:\n%s", pg)
	}
}

func TestExoticIndexRendering(t *testing.T) {
	cases := []struct {
		dialect string
		ix      sorm.IndexDef
		want    string
		wantErr bool
	}{
		{"postgres",
			sorm.IndexDef{Name: "i_fts", Type: "gin",
				Parts: []sorm.IndexPart{{Expr: "to_tsvector('russian', title)"}}},
			`CREATE INDEX "i_fts" ON "posts" USING GIN ((to_tsvector('russian', title)));`, false},
		{"mysql",
			sorm.IndexDef{Name: "i_ft", Type: "fulltext", Columns: []string{"title"}},
			"CREATE FULLTEXT INDEX `i_ft` ON `posts` (`title`);", false},
		{"postgres",
			sorm.IndexDef{Name: "i_part", Where: "views > 0",
				Parts: []sorm.IndexPart{{Column: "views", Desc: true}}},
			`CREATE INDEX "i_part" ON "posts" ("views" DESC) WHERE views > 0;`, false},
		{"mysql",
			sorm.IndexDef{Name: "i_bad", Columns: []string{"views"}, Where: "views > 0"},
			"", true},
		{"sqlite",
			sorm.IndexDef{Name: "i_bad2", Columns: []string{"views"}, Type: "gin"},
			"", true},
	}
	for _, c := range cases {
		got, err := ddl.IndexDDL("posts", c.ix, c.dialect)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s/%s: expected an error", c.dialect, c.ix.Name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s/%s: %v", c.dialect, c.ix.Name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got:  %s\n want: %s", c.ix.Name, got, c.want)
		}
	}
}
