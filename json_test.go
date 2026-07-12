package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

func TestJSONSQLite(t *testing.T) { runJSONScenario(t, sqliteDB(t), "sqlite") }
func TestJSONPG(t *testing.T)     { runJSONScenario(t, testPool(t), "postgres") }
func TestJSONMySQL(t *testing.T)  { runJSONScenario(t, mysqlDB(t), "mysql") }

func runJSONScenario(t *testing.T, db sorm.DB, dialect string) {
	ctx := context.Background()
	p := gen.Profile

	// Seed: a user and two profiles — one with rich JSON, one with NULL/empty.
	s := sorm.NewSession(db)
	alice := &models.User{Email: "a@b.c", Name: "Alice", Active: true, CreatedAt: nowNoZero()}
	bob := &models.User{Email: "b@b.c", Name: "Bob", Active: true, CreatedAt: nowNoZero()}
	sorm.Add(s, alice, bob)
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	s2 := sorm.NewSession(db)
	rich := &models.Profile{
		UserID: alice.ID,
		Bio:    "rich",
		Prefs: &models.ProfilePrefs{
			Theme: "dark", Limit: 10, Beta: true,
			Labels: []string{"go", "db"},
			Notify: models.PrefsNotify{Email: true, Chan: "slack"},
		},
		Meta: map[string]any{"tier": "pro", "flags": map[string]any{"beta": "on"}},
	}
	empty := &models.Profile{UserID: bob.ID, Bio: "empty"} // Prefs nil, Meta nil → NULL
	sorm.Add(s2, rich, empty)
	if err := s2.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}

	// 1. Roundtrip: struct and map come back equal.
	got, err := sorm.Query[models.Profile](db).Where(p.Bio.Eq("rich")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prefs == nil || got.Prefs.Theme != "dark" || got.Prefs.Limit != 10 || len(got.Prefs.Labels) != 2 {
		t.Fatalf("struct roundtrip: %+v", got.Prefs)
	}
	if got.Meta["tier"] != "pro" {
		t.Fatalf("map roundtrip: %+v", got.Meta)
	}

	// 2. NULL semantics: nil pointer and nil map stored as SQL NULL.
	nulls, err := sorm.Query[models.Profile](db).Where(p.Prefs.IsNull(), p.Meta.IsNull()).All(ctx)
	if err != nil || len(nulls) != 1 || nulls[0].Bio != "empty" {
		t.Fatalf("IsNull: %v (err=%v)", nulls, err)
	}

	// 3. Path predicates on all dialects.
	byTheme, err := sorm.Query[models.Profile](db).Where(p.Prefs.Path("theme").Eq("dark")).All(ctx)
	if err != nil || len(byTheme) != 1 || byTheme[0].Bio != "rich" {
		t.Fatalf("Path.Eq: %v (err=%v)", byTheme, err)
	}
	// Note: path extraction compares string values portably; booleans/numbers
	// come back as native SQL values on SQLite but text on PG/MySQL.
	nested, err := sorm.Query[models.Profile](db).Where(p.Meta.Path("flags.beta").Eq("on")).Count(ctx)
	if err != nil || nested != 1 {
		t.Fatalf("nested path: %d (err=%v)", nested, err)
	}
	none, err := sorm.Query[models.Profile](db).Where(p.Prefs.Path("theme").Eq("light")).Count(ctx)
	if err != nil || none != 0 {
		t.Fatalf("path mismatch must yield 0: %d (err=%v)", none, err)
	}

	// 4. HasKey on all dialects.
	withTier, err := sorm.Query[models.Profile](db).Where(p.Meta.HasKey("tier")).Count(ctx)
	if err != nil || withTier != 1 {
		t.Fatalf("HasKey: %d (err=%v)", withTier, err)
	}

	// 5. Contains: PG and MySQL; a clear build error on SQLite.
	containsQ := sorm.Query[models.Profile](db).Where(p.Meta.Contains(map[string]any{"tier": "pro"}))
	n, err := containsQ.Count(ctx)
	if dialect == "sqlite" {
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("sqlite Contains must be a build error, got n=%d err=%v", n, err)
		}
	} else if err != nil || n != 1 {
		t.Fatalf("Contains: %d (err=%v)", n, err)
	}

	// 5b. Typed accessors: compiler-checked names, dialect-correct types.
	byThemeT, err := sorm.Query[models.Profile](db).Where(p.PrefsDoc.Theme.Eq("dark")).Count(ctx)
	if err != nil || byThemeT != 1 {
		t.Fatalf("typed Theme.Eq: %d (err=%v)", byThemeT, err)
	}
	// Numeric comparison is portable — the reason typed accessors exist:
	// Path() would compare text and break on SQLite vs PG.
	byLimit, err := sorm.Query[models.Profile](db).Where(p.PrefsDoc.Limit.Gt(5)).Count(ctx)
	if err != nil || byLimit != 1 {
		t.Fatalf("typed Limit.Gt: %d (err=%v)", byLimit, err)
	}
	if n, err := sorm.Query[models.Profile](db).Where(p.PrefsDoc.Limit.Gt(100)).Count(ctx); err != nil || n != 0 {
		t.Fatalf("typed Limit.Gt(100): %d (err=%v)", n, err)
	}
	// Booleans, portable across dialects (1/0 vs true/false handled per dialect).
	beta, err := sorm.Query[models.Profile](db).Where(p.PrefsDoc.Beta.IsTrue()).Count(ctx)
	if err != nil || beta != 1 {
		t.Fatalf("typed Beta.IsTrue: %d (err=%v)", beta, err)
	}
	// Nested objects nest the accessors.
	nestedT, err := sorm.Query[models.Profile](db).
		Where(p.PrefsDoc.Notify.Email.IsTrue(), p.PrefsDoc.Notify.Chan.Eq("slack")).
		Count(ctx)
	if err != nil || nestedT != 1 {
		t.Fatalf("typed nested accessors: %d (err=%v)", nestedT, err)
	}
	// Array containment (PG/MySQL; build error on SQLite).
	arrQ := sorm.Query[models.Profile](db).Where(p.PrefsDoc.Labels.Contains("go"))
	an, err := arrQ.Count(ctx)
	if dialect == "sqlite" {
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("sqlite array Contains must be a build error, got n=%d err=%v", an, err)
		}
	} else if err != nil || an != 1 {
		t.Fatalf("typed Labels.Contains: %d (err=%v)", an, err)
	}

	// 6. Diff: nested mutation → UPDATE; no mutation → no-op.
	s3 := sorm.NewSession(db)
	tracked, err := sorm.Track[models.Profile](s3).Where(p.Bio.Eq("rich")).One(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.SaveChanges(ctx); err != nil { // untouched → no-op
		t.Fatal(err)
	}
	tracked.Prefs.Theme = "light"
	tracked.Meta["tier"] = "free"
	if err := s3.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	re, err := sorm.Query[models.Profile](db).Where(p.Prefs.Path("theme").Eq("light")).One(ctx)
	if err != nil || re.Meta["tier"] != "free" {
		t.Fatalf("json diff update: %+v (err=%v)", re, err)
	}

	// 7. Set-based: typed Set and SetNull on a json column.
	if _, err := sorm.Update[models.Profile](db).
		Set(p.Meta.Set(map[string]any{"reset": true})).
		Where(p.Bio.Eq("rich")).
		Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := sorm.Query[models.Profile](db).Where(p.Meta.HasKey("reset")).Count(ctx); n != 1 {
		t.Fatalf("json Set: HasKey(reset) = %d", n)
	}
	if _, err := sorm.Update[models.Profile](db).
		Set(p.Prefs.SetNull()).
		Where(p.Bio.Eq("rich")).
		Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := sorm.Query[models.Profile](db).Where(p.Prefs.IsNull()).Count(ctx); n != 2 {
		t.Fatalf("json SetNull: %d NULL prefs, want 2", n)
	}
}

// SQL rendering per dialect (no execution).
func TestJSONPathSQL(t *testing.T) {
	p := gen.Profile
	sql, args := sorm.Query[models.Profile](nil). // nil db → postgres dialect
							Where(p.Prefs.Path("a.b").Eq("v")).
							ToSQL()
	if !strings.Contains(sql, `"prefs" #>> '{a,b}' = $1`) {
		t.Fatalf("pg path sql: %s", sql)
	}
	if len(args) != 1 || args[0] != "v" {
		t.Fatalf("args: %v", args)
	}

	// Invalid path segment surfaces on execution, not as broken SQL.
	_, err := sorm.Query[models.Profile](sqliteDB(t)).
		Where(p.Prefs.Path("a'; DROP TABLE x --").Eq("v")).
		All(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid json path segment") {
		t.Fatalf("expected path validation error, got %v", err)
	}
}
