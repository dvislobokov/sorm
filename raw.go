package sorm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Raw is the escape hatch into raw SQL with scanning into entities via meta.
// Matching is strict by column name: any mismatch yields a *ScanError with
// the lists, not silently skipped fields.
func Raw[E any](db DB, sql string, args ...any) RawQuery[E] {
	m := metaFor[E]()
	byName := make(map[string]int, len(m.SelectCols))
	for i, c := range m.SelectCols {
		byName[c] = i
	}
	return RawQuery[E]{
		db: db, sql: sql, args: args,
		dests: func(cols []string) ([]int, *ScanError) {
			return matchColumns(cols, byName, m.SelectCols)
		},
		targets: func(e *E) []any { return m.Scan(e) },
	}
}

// RawAs is raw SQL with scanning into an arbitrary struct R
// (aggregates, CTEs, window functions — anything that is not the shape of an entity).
// Mapping: the `sorm:"col"` tag or snake_case of the field name. The scan plan
// is built once per type and cached.
func RawAs[R any](db DB, sql string, args ...any) RawQuery[R] {
	plan, err := structPlanFor(reflect.TypeFor[R]())
	if err != nil {
		return RawQuery[R]{err: err}
	}
	return RawQuery[R]{
		db: db, sql: sql, args: args,
		dests: func(cols []string) ([]int, *ScanError) {
			return matchColumns(cols, plan.byName, plan.names)
		},
		targets: func(r *R) []any {
			v := reflect.ValueOf(r).Elem()
			out := make([]any, len(plan.fields))
			for i, fi := range plan.fields {
				out[i] = v.Field(fi).Addr().Interface()
			}
			return out
		},
	}
}

type RawQuery[T any] struct {
	db   DB
	sql  string
	args []any
	name string
	err  error
	// dests: result columns -> target indexes; targets: targets in plan order.
	dests   func(resultCols []string) ([]int, *ScanError)
	targets func(*T) []any
}

// Named labels the query for instrumentation (sorm.query.name).
func (q RawQuery[T]) Named(name string) RawQuery[T] {
	q.name = name
	return q
}

func (q RawQuery[T]) All(ctx context.Context) ([]*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	ctx = named(ctx, q.name)
	rows, err := q.db.Query(ctx, q.sql, q.args...)
	if err != nil {
		return nil, fmt.Errorf("sorm: raw: %w", err)
	}
	defer rows.Close()

	idxs, scanErr := q.dests(rows.Columns())
	if scanErr != nil {
		return nil, scanErr
	}

	out := []*T{}
	for rows.Next() {
		e := new(T)
		all := q.targets(e)
		row := make([]any, len(idxs))
		for i, idx := range idxs {
			row[i] = all[idx]
		}
		if err := rows.Scan(row...); err != nil {
			return nil, fmt.Errorf("sorm: raw scan: %w", err)
		}
		if err := afterLoad(ctx, any(e)); err != nil {
			return nil, fmt.Errorf("sorm: after load: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q RawQuery[T]) One(ctx context.Context) (*T, error) {
	all, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNotFound
	}
	return all[0], nil
}

// matchColumns enforces strict matching: every result column must have a
// target, and every target a column.
func matchColumns(resultCols []string, byName map[string]int, known []string) ([]int, *ScanError) {
	idxs := make([]int, 0, len(resultCols))
	used := make(map[string]bool, len(resultCols))
	var missing []string
	for _, c := range resultCols {
		idx, ok := byName[c]
		if !ok {
			missing = append(missing, c)
			continue
		}
		idxs = append(idxs, idx)
		used[c] = true
	}
	var extra []string
	for _, c := range known {
		if !used[c] {
			extra = append(extra, c)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		return nil, &ScanError{Missing: missing, Extra: extra}
	}
	return idxs, nil
}

// --- scan plan for an arbitrary struct (RawAs/Project) ---

type structPlan struct {
	names  []string       // column names in field order
	byName map[string]int // name -> ordinal of the field in fields
	fields []int          // struct field indexes
}

var structPlans sync.Map // reflect.Type -> *structPlan

func structPlanFor(t reflect.Type) (*structPlan, error) {
	if p, ok := structPlans.Load(t); ok {
		return p.(*structPlan), nil
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("sorm: RawAs/Project target must be a struct, got %s", t)
	}
	p := &structPlan{byName: map[string]int{}}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, ok := f.Tag.Lookup("sorm")
		if name == "-" {
			continue
		}
		if !ok || name == "" {
			name = snakeCase(f.Name)
		}
		if _, dup := p.byName[name]; dup {
			return nil, fmt.Errorf("sorm: %s: duplicate column %q", t, name)
		}
		p.byName[name] = len(p.fields)
		p.names = append(p.names, name)
		p.fields = append(p.fields, i)
	}
	if len(p.fields) == 0 {
		return nil, fmt.Errorf("sorm: %s has no scannable fields", t)
	}
	structPlans.Store(t, p)
	return p, nil
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 && (isLowerByte(s[i-1]) || (i+1 < len(s) && isLowerByte(s[i+1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isLowerByte(c byte) bool { return c >= 'a' && c <= 'z' }
