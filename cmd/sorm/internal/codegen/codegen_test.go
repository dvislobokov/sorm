package codegen_test

import (
	"os"
	"path/filepath"
	"testing"

	"sorm/cmd/sorm/internal/codegen"
	"sorm/cmd/sorm/internal/parse"
)

// Golden-тест: генерация из internal/testmodels должна бит-в-бит совпадать
// с закоммиченным internal/testmodels/sormgen. При осознанном изменении
// генератора: `go run ./cmd/sorm gen ./internal/testmodels` и закоммитить.
func TestGoldenTestmodels(t *testing.T) {
	modelsDir := filepath.Join("..", "..", "..", "..", "internal", "testmodels")

	schema, err := parse.Load(modelsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(schema.Entities))
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
	modelsDir := filepath.Join("..", "..", "..", "..", "internal", "testmodels")
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
	if len(user.Relations) != 1 || user.Relations[0].Kind != "hasMany" || user.Relations[0].Target != "Post" {
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
