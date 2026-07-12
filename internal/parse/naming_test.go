package parse

import "testing"

func TestRename(t *testing.T) {
	cases := []struct{ naming, in, want string }{
		{NamingSnake, "CreatedAt", "created_at"},
		{NamingSnake, "UserID", "user_id"},
		{NamingCamel, "CreatedAt", "createdAt"},
		{NamingCamel, "UserID", "userId"},
		{NamingCamel, "ID", "id"},
		{NamingPascal, "CreatedAt", "CreatedAt"},
		{NamingPascal, "UserID", "UserId"},
	}
	for _, c := range cases {
		if got := Rename(c.naming, c.in); got != c.want {
			t.Errorf("Rename(%s, %s) = %s, want %s", c.naming, c.in, got, c.want)
		}
	}
}

func TestRenamePlural(t *testing.T) {
	cases := []struct{ naming, in, want string }{
		{NamingSnake, "User", "users"},
		{NamingSnake, "ApiKey", "api_keys"},
		{NamingSnake, "Category", "categories"},
		{NamingSnake, "Box", "boxes"},
		{NamingCamel, "ApiKey", "apiKeys"},
		{NamingCamel, "User", "users"},
		{NamingPascal, "ApiKey", "ApiKeys"},
		{NamingPascal, "Category", "Categories"},
	}
	for _, c := range cases {
		if got := RenamePlural(c.naming, c.in); got != c.want {
			t.Errorf("RenamePlural(%s, %s) = %s, want %s", c.naming, c.in, got, c.want)
		}
	}
}

// The strategy changes derived identifiers across the whole schema;
// explicit col:/table: overrides win untouched.
func TestLoadWithNaming(t *testing.T) {
	s, err := Load("../testmodels", WithNaming(NamingCamel))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Entity{}
	for _, e := range s.Entities {
		byName[e.Name] = e
	}
	ak := byName["ApiKey"]
	if ak.Table != "apiKeys" {
		t.Fatalf("ApiKey table = %s, want apiKeys", ak.Table)
	}
	post := byName["Post"]
	var created, authorID string
	for _, f := range post.Fields {
		switch f.GoName {
		case "CreatedAt":
			created = f.Col
		case "AuthorID":
			authorID = f.Col
		}
	}
	if created != "createdAt" || authorID != "authorId" {
		t.Fatalf("cols: created=%s authorId=%s", created, authorID)
	}

	if _, err := Load("../testmodels", WithNaming("kebab")); err == nil {
		t.Fatal("unknown naming must be rejected")
	}
}
