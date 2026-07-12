package codegen_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dvislobokov/sorm/internal/codegen"
	"github.com/dvislobokov/sorm/internal/parse"
)

// Golden test: generation from internal/testmodels must match the committed
// internal/testmodels/sormgen byte for byte. On an intentional generator
// change: `go run ./cmd/sorm gen ./internal/testmodels` and commit.
func TestGoldenTestmodels(t *testing.T) {
	modelsDir := filepath.Join("..", "testmodels")

	schema, err := parse.Load(modelsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Entities) != 7 {
		t.Fatalf("entities = %d, want 7", len(schema.Entities))
	}

	files, err := codegen.Generate(schema)
	if err != nil {
		t.Fatal(err)
	}

	goldenDir := filepath.Join(modelsDir, "sormgen")
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		onDisk[e.Name()] = true
	}

	for name, got := range files {
		want, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			t.Errorf("%s: generated but missing on disk (run `go run ./cmd/sorm gen ./internal/testmodels`)", name)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s: generated output differs from committed golden file", name)
		}
		delete(onDisk, name)
	}
	for name := range onDisk {
		t.Errorf("%s: on disk but not generated anymore", name)
	}
}

func TestSchemaShape(t *testing.T) {
	modelsDir := filepath.Join("..", "testmodels")
	schema, err := parse.Load(modelsDir)
	if err != nil {
		t.Fatal(err)
	}

	var user parse.Entity
	for _, e := range schema.Entities {
		if e.Name == "User" {
			user = e
		}
	}
	if user.Table != "users" {
		t.Errorf("table = %q, want users", user.Table)
	}
	if user.PK().Col != "id" || !user.PK().Auto {
		t.Errorf("pk = %+v", user.PK())
	}
	if user.VersionIndex < 0 || user.Fields[user.VersionIndex].Col != "version" {
		t.Errorf("version field not detected")
	}
	if len(user.Relations) != 3 || user.Relations[0].Kind != "hasMany" || user.Relations[0].Target != "Post" ||
		user.Relations[1].Kind != "many2many" || user.Relations[1].JoinTable != "user_tags" ||
		user.Relations[2].Kind != "hasOne" || user.Relations[2].Target != "Profile" {
		t.Errorf("relations = %+v", user.Relations)
	}

	// snake_case: CreatedAt → created_at, AuthorID → author_id
	wantCols := map[string]string{"CreatedAt": "created_at", "DeletedAt": "deleted_at"}
	for _, f := range user.Fields {
		if want, ok := wantCols[f.GoName]; ok && f.Col != want {
			t.Errorf("%s → %q, want %q", f.GoName, f.Col, want)
		}
	}
}
