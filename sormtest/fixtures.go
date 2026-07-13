package sormtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dvislobokov/sorm"
)

// Load seeds YAML fixtures. Top-level keys are table names, values are
// row lists with column names as keys:
//
//	users:
//	  - {id: 1, email: alice@a.b, name: Alice, active: true,
//	     created_at: 2026-01-01T00:00:00Z, version: 1}
//	posts:
//	  - {id: 1, author_id: 1, title: hi, body: "", views: 0,
//	     created_at: 2026-01-01T00:00:00Z, updated_at: 2026-01-01T00:00:00Z}
//
// Tables are inserted parents-first — the order comes from the FK graph
// of the registered TableDefs, not from the file. Rows are raw: every
// NOT NULL column must be present (fixtures bypass auto-timestamps and
// hooks by design — a fixture states facts, it does not run logic).
// Map/slice values are marshaled to JSON for json columns.
func Load(t testing.TB, db sorm.DB, paths ...string) {
	t.Helper()
	ctx := context.Background()

	// Parents-first table ranks from the registered FK graph.
	rank := tableRanks()

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("sormtest: fixture %s: %v", path, err)
		}
		var doc map[string][]map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("sormtest: fixture %s: %v", path, err)
		}

		tables := make([]string, 0, len(doc))
		for tbl := range doc {
			if _, known := rank[tbl]; !known {
				t.Fatalf("sormtest: fixture %s: unknown table %q (is the sormgen package imported?)", path, tbl)
			}
			tables = append(tables, tbl)
		}
		sort.Slice(tables, func(i, j int) bool {
			if rank[tables[i]] != rank[tables[j]] {
				return rank[tables[i]] < rank[tables[j]]
			}
			return tables[i] < tables[j]
		})

		for _, tbl := range tables {
			for _, row := range doc[tbl] {
				if err := insertRow(ctx, db, tbl, row); err != nil {
					t.Fatalf("sormtest: fixture %s, table %s: %v", path, tbl, err)
				}
			}
		}
	}
}

// tableRanks orders tables parents-first by the FK references of the
// registered TableDefs (0 = no parents).
func tableRanks() map[string]int {
	refs := map[string][]string{}
	for _, def := range sorm.Tables() {
		for _, c := range def.Columns {
			if c.RefTable != "" && c.RefTable != def.Name {
				refs[def.Name] = append(refs[def.Name], c.RefTable)
			}
		}
		if _, ok := refs[def.Name]; !ok {
			refs[def.Name] = nil
		}
	}
	rank := map[string]int{}
	var depth func(tbl string, seen map[string]bool) int
	depth = func(tbl string, seen map[string]bool) int {
		if r, ok := rank[tbl]; ok {
			return r
		}
		if seen[tbl] {
			return 0
		}
		seen[tbl] = true
		max := 0
		for _, parent := range refs[tbl] {
			if d := depth(parent, seen) + 1; d > max {
				max = d
			}
		}
		rank[tbl] = max
		return max
	}
	for tbl := range refs {
		depth(tbl, map[string]bool{})
	}
	return rank
}

func insertRow(ctx context.Context, db sorm.DB, table string, row map[string]any) error {
	cols := make([]string, 0, len(row))
	for c := range row {
		cols = append(cols, c)
	}
	sort.Strings(cols) // deterministic statements

	d := db.Dialect()
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(d.QuoteIdent(table))
	b.WriteString(" (")
	args := make([]any, 0, len(cols))
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.QuoteIdent(c))
		args = append(args, fixtureValue(row[c]))
	}
	b.WriteString(") VALUES (")
	for i := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.Placeholder(i + 1))
	}
	b.WriteString(")")
	if _, err := db.Exec(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("%w", err)
	}
	return nil
}

// fixtureValue adapts YAML values for the driver: maps and slices become
// JSON documents, everything else passes through (yaml already yields
// time.Time for timestamps).
func fixtureValue(v any) any {
	switch v.(type) {
	case map[string]any, []any:
		b, err := json.Marshal(v)
		if err != nil {
			return v
		}
		return string(b)
	}
	return v
}
