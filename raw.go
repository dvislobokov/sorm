package sorm

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// Raw — escape в сырой SQL со сканированием в сущности через мету.
// Соответствие проверяется строго по именам колонок: любое расхождение —
// *ScanError со списками, а не тихо пропущенные поля.
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

// RawAs — сырой SQL со сканированием в произвольную структуру R
// (агрегаты, CTE, window functions — всё, что не форма сущности).
// Маппинг: тег `sorm:"col"` или snake_case имени поля. План сканирования
// строится один раз на тип и кэшируется.
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
	err  error
	// dests: колонки результата → индексы целей; targets: цели в порядке плана.
	dests   func(resultCols []string) ([]int, *ScanError)
	targets func(*T) []any
}

func (q RawQuery[T]) All(ctx context.Context) ([]*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	rows, err := q.db.Query(ctx, q.sql, q.args...)
	if err != nil {
		return nil, fmt.Errorf("sorm: raw: %w", err)
	}
	defer rows.Close()

	idxs, scanErr := q.dests(columnNames(rows))
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

func columnNames(rows pgx.Rows) []string {
	fds := rows.FieldDescriptions()
	out := make([]string, len(fds))
	for i, fd := range fds {
		out[i] = fd.Name
	}
	return out
}

// matchColumns — строгое соответствие: каждая колонка результата обязана
// иметь цель, каждая цель — колонку.
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

// --- план сканирования произвольной структуры (RawAs/Project) ---

type structPlan struct {
	names  []string       // имена колонок в порядке полей
	byName map[string]int // имя → порядковый номер поля в fields
	fields []int          // индексы полей структуры
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
