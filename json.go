package sorm

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// JSON columns: a `sorm:"json"` field of any marshalable Go type is stored
// as JSONB (PostgreSQL), JSON (MySQL) or TEXT (SQLite, queryable via the
// json1 functions). The generated code uses the helpers below; JSONCol is
// the generated descriptor with dialect-aware content predicates.

// JSONValue wraps a value for writing into a JSON column: it marshals on
// Value(). A nil pointer/map/slice becomes SQL NULL.
func JSONValue(v any) driver.Valuer { return jsonValue{v} }

type jsonValue struct{ v any }

func (j jsonValue) Value() (driver.Value, error) {
	if j.v == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(j.v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if rv.IsNil() {
			return nil, nil
		}
	}
	b, err := json.Marshal(j.v)
	if err != nil {
		return nil, fmt.Errorf("sorm: marshal json column: %w", err)
	}
	return string(b), nil
}

// JSONScan wraps a destination for reading a JSON column: it unmarshals
// []byte/string; SQL NULL leaves the destination at its zero value.
func JSONScan[T any](dst *T) sql.Scanner { return &jsonScanner[T]{dst} }

type jsonScanner[T any] struct{ dst *T }

func (s *jsonScanner[T]) Scan(src any) error {
	var data []byte
	switch v := src.(type) {
	case nil:
		var zero T
		*s.dst = zero
		return nil
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("sorm: cannot scan %T into a json column", src)
	}
	// pgx binary jsonb may carry a version-prefix byte (0x01) before the payload.
	if len(data) > 0 && data[0] == 0x01 {
		data = data[1:]
	}
	if len(bytes.TrimSpace(data)) == 0 {
		var zero T
		*s.dst = zero
		return nil
	}
	if err := json.Unmarshal(data, s.dst); err != nil {
		return fmt.Errorf("sorm: unmarshal json column: %w", err)
	}
	return nil
}

// JSONSnapshot marshals a value for snapshot diffing (json.Marshal is
// deterministic: map keys are sorted). nil-ish values snapshot as nil.
func JSONSnapshot(v any) []byte {
	val, err := jsonValue{v}.Value()
	if err != nil || val == nil {
		return nil
	}
	return []byte(val.(string))
}

// JSONCol is the descriptor of a JSON column with content predicates.
// Dialect support: Path works everywhere; Contains — PostgreSQL (@>) and
// MySQL (JSON_CONTAINS), a build error on SQLite; HasKey works everywhere.
type JSONCol[E any] struct{ ref colRef }

func NewJSONCol[E any](table, name string) JSONCol[E] {
	return JSONCol[E]{colRef{table, name}}
}

func (c JSONCol[E]) ColName() string  { return c.ref.name }
func (c JSONCol[E]) colTable() string { return c.ref.table }
func (c JSONCol[E]) entityMark(*E)    {}

func (c JSONCol[E]) IsNull() Pred[E]    { return pred[E](nullNode{c.ref, false}) }
func (c JSONCol[E]) IsNotNull() Pred[E] { return pred[E](nullNode{c.ref, true}) }

// Set is a typed assignment for set-based Update (the value is marshaled).
func (c JSONCol[E]) Set(v any) Assign[E] { return Assign[E]{col: c.ref.name, val: JSONValue(v)} }

// SetNull assigns SQL NULL.
func (c JSONCol[E]) SetNull() Assign[E] { return Assign[E]{col: c.ref.name, val: nil} }

// Path addresses a nested value with dot notation ("a.b.c"). Extraction is
// textual: comparisons behave like string comparisons of the unquoted value.
// Segments must match [A-Za-z0-9_]+ — anything else is a build error
// surfaced when the query executes.
func (c JSONCol[E]) Path(path string) JSONPath[E] {
	return JSONPath[E]{ref: c.ref, path: path}
}

// Contains reports whether the column's JSON document contains v
// (PostgreSQL @>, MySQL JSON_CONTAINS). Not available on SQLite.
func (c JSONCol[E]) Contains(v any) Pred[E] {
	return pred[E](jsonContainsNode{ref: c.ref, val: v})
}

// HasKey reports whether the top-level document has the given key.
func (c JSONCol[E]) HasKey(key string) Pred[E] {
	return pred[E](jsonHasKeyNode{ref: c.ref, key: key})
}

// JSONPath is a dot-notation path into a JSON column.
type JSONPath[E any] struct {
	ref  colRef
	path string
}

func (p JSONPath[E]) Eq(v string) Pred[E]  { return pred[E](jsonPathCmpNode{p.ref, p.path, "=", v}) }
func (p JSONPath[E]) Neq(v string) Pred[E] { return pred[E](jsonPathCmpNode{p.ref, p.path, "<>", v}) }

func (p JSONPath[E]) In(vs ...string) Pred[E] {
	return pred[E](jsonPathInNode{p.ref, p.path, vs})
}

// IsNull is true when the path is absent or holds JSON null.
func (p JSONPath[E]) IsNull() Pred[E]    { return pred[E](jsonPathNullNode{p.ref, p.path, false}) }
func (p JSONPath[E]) IsNotNull() Pred[E] { return pred[E](jsonPathNullNode{p.ref, p.path, true}) }

// --- typed accessors (generated for struct-typed json columns) ---
//
// For a `sorm:"json"` field whose Go type is a struct, `sorm gen` emits a
// companion <Field>Doc descriptor with one typed accessor per JSON field:
//
//	q.Where(p.PrefsDoc.Theme.Eq("dark"))     // string — text extraction
//	q.Where(p.PrefsDoc.Limit.Gt(5))          // number — dialect-correct cast
//	q.Where(p.PrefsDoc.Beta.IsTrue())        // bool   — dialect-correct compare
//	q.Where(p.PrefsDoc.Labels.Contains("go"))// array  — containment (PG/MySQL)
//
// Unlike Path(...), numeric and boolean comparisons are portable across
// dialects, and field names are checked by the compiler.

// JSONStr is a typed accessor for a string JSON field.
type JSONStr[E any] struct {
	ref  colRef
	path string
}

func NewJSONStr[E any](table, col, path string) JSONStr[E] {
	return JSONStr[E]{colRef{table, col}, path}
}

func (j JSONStr[E]) Eq(v string) Pred[E]  { return pred[E](jsonPathCmpNode{j.ref, j.path, "=", v}) }
func (j JSONStr[E]) Neq(v string) Pred[E] { return pred[E](jsonPathCmpNode{j.ref, j.path, "<>", v}) }
func (j JSONStr[E]) Like(p string) Pred[E] {
	return pred[E](jsonPathCmpNode{j.ref, j.path, "LIKE", p})
}
func (j JSONStr[E]) In(vs ...string) Pred[E] { return pred[E](jsonPathInNode{j.ref, j.path, vs}) }
func (j JSONStr[E]) IsNull() Pred[E]         { return pred[E](jsonPathNullNode{j.ref, j.path, false}) }
func (j JSONStr[E]) IsNotNull() Pred[E]      { return pred[E](jsonPathNullNode{j.ref, j.path, true}) }

// JSONNum is a typed accessor for a numeric JSON field. Comparisons are
// numeric on every dialect: PG casts the text extraction to numeric,
// MySQL and SQLite compare the extracted value natively.
type JSONNum[E any, V int64 | float64] struct {
	ref  colRef
	path string
}

func NewJSONNum[E any, V int64 | float64](table, col, path string) JSONNum[E, V] {
	return JSONNum[E, V]{colRef{table, col}, path}
}

func (j JSONNum[E, V]) Eq(v V) Pred[E]  { return pred[E](jsonNumCmpNode{j.ref, j.path, "=", v}) }
func (j JSONNum[E, V]) Neq(v V) Pred[E] { return pred[E](jsonNumCmpNode{j.ref, j.path, "<>", v}) }
func (j JSONNum[E, V]) Gt(v V) Pred[E]  { return pred[E](jsonNumCmpNode{j.ref, j.path, ">", v}) }
func (j JSONNum[E, V]) Gte(v V) Pred[E] { return pred[E](jsonNumCmpNode{j.ref, j.path, ">=", v}) }
func (j JSONNum[E, V]) Lt(v V) Pred[E]  { return pred[E](jsonNumCmpNode{j.ref, j.path, "<", v}) }
func (j JSONNum[E, V]) Lte(v V) Pred[E] { return pred[E](jsonNumCmpNode{j.ref, j.path, "<=", v}) }
func (j JSONNum[E, V]) IsNull() Pred[E] { return pred[E](jsonPathNullNode{j.ref, j.path, false}) }
func (j JSONNum[E, V]) IsNotNull() Pred[E] {
	return pred[E](jsonPathNullNode{j.ref, j.path, true})
}

// JSONBool is a typed accessor for a boolean JSON field.
type JSONBool[E any] struct {
	ref  colRef
	path string
}

func NewJSONBool[E any](table, col, path string) JSONBool[E] {
	return JSONBool[E]{colRef{table, col}, path}
}

func (j JSONBool[E]) Eq(v bool) Pred[E] { return pred[E](jsonBoolCmpNode{j.ref, j.path, v}) }
func (j JSONBool[E]) IsTrue() Pred[E]   { return j.Eq(true) }
func (j JSONBool[E]) IsFalse() Pred[E]  { return j.Eq(false) }
func (j JSONBool[E]) IsNull() Pred[E]   { return pred[E](jsonPathNullNode{j.ref, j.path, false}) }
func (j JSONBool[E]) IsNotNull() Pred[E] {
	return pred[E](jsonPathNullNode{j.ref, j.path, true})
}

// JSONArr is a typed accessor for an array JSON field.
type JSONArr[E any] struct {
	ref  colRef
	path string
}

func NewJSONArr[E any](table, col, path string) JSONArr[E] {
	return JSONArr[E]{colRef{table, col}, path}
}

// Contains reports whether the array contains v (an element or a sub-array).
// PostgreSQL and MySQL; a build error on SQLite.
func (j JSONArr[E]) Contains(v any) Pred[E] {
	return pred[E](jsonArrContainsNode{j.ref, j.path, v})
}

func (j JSONArr[E]) IsNull() Pred[E] { return pred[E](jsonPathNullNode{j.ref, j.path, false}) }
func (j JSONArr[E]) IsNotNull() Pred[E] {
	return pred[E](jsonPathNullNode{j.ref, j.path, true})
}

// --- dialect-aware AST nodes ---

var jsonSegmentRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// writeJSONPathExpr renders the text-extraction expression for a path.
func writeJSONPathExpr(w *sqlWriter, ref colRef, path string) {
	segs := strings.Split(path, ".")
	for _, s := range segs {
		if !jsonSegmentRe.MatchString(s) {
			w.fail(fmt.Sprintf("sorm: invalid json path segment %q (want [A-Za-z0-9_]+)", s))
			return
		}
	}
	switch w.d.Name() {
	case "postgres":
		w.col(ref)
		w.raw(" #>> '{" + strings.Join(segs, ",") + "}'")
	case "mysql":
		w.raw("JSON_UNQUOTE(JSON_EXTRACT(")
		w.col(ref)
		w.raw(", '$." + strings.Join(segs, ".") + "'))")
	default: // sqlite
		w.raw("json_extract(")
		w.col(ref)
		w.raw(", '$." + strings.Join(segs, ".") + "')")
	}
}

type jsonPathCmpNode struct {
	ref  colRef
	path string
	op   string
	val  string
}

func (n jsonPathCmpNode) writeSQL(w *sqlWriter) {
	writeJSONPathExpr(w, n.ref, n.path)
	w.raw(" " + n.op + " ")
	w.arg(n.val)
}

type jsonPathInNode struct {
	ref  colRef
	path string
	vals []string
}

func (n jsonPathInNode) writeSQL(w *sqlWriter) {
	if len(n.vals) == 0 {
		w.raw("FALSE")
		return
	}
	writeJSONPathExpr(w, n.ref, n.path)
	w.raw(" IN (")
	for i, v := range n.vals {
		if i > 0 {
			w.raw(", ")
		}
		w.arg(v)
	}
	w.raw(")")
}

type jsonPathNullNode struct {
	ref  colRef
	path string
	not  bool
}

func (n jsonPathNullNode) writeSQL(w *sqlWriter) {
	writeJSONPathExpr(w, n.ref, n.path)
	if n.not {
		w.raw(" IS NOT NULL")
	} else {
		w.raw(" IS NULL")
	}
}

// jsonNumCmpNode — numeric comparison of an extracted JSON value.
type jsonNumCmpNode struct {
	ref  colRef
	path string
	op   string
	val  any
}

func (n jsonNumCmpNode) writeSQL(w *sqlWriter) {
	segs, ok := jsonSegments(w, n.path)
	if !ok {
		return
	}
	switch w.d.Name() {
	case "postgres":
		w.raw("(")
		w.col(n.ref)
		w.raw(" #>> '{" + strings.Join(segs, ",") + "}')::numeric")
	case "mysql":
		// JSON_EXTRACT (without UNQUOTE) yields a JSON number that MySQL
		// compares numerically with SQL numbers.
		w.raw("JSON_EXTRACT(")
		w.col(n.ref)
		w.raw(", '$." + strings.Join(segs, ".") + "')")
	default: // sqlite: json_extract returns a native INTEGER/REAL
		w.raw("json_extract(")
		w.col(n.ref)
		w.raw(", '$." + strings.Join(segs, ".") + "')")
	}
	w.raw(" " + n.op + " ")
	w.arg(n.val)
}

// jsonBoolCmpNode — boolean comparison of an extracted JSON value.
type jsonBoolCmpNode struct {
	ref  colRef
	path string
	val  bool
}

func (n jsonBoolCmpNode) writeSQL(w *sqlWriter) {
	segs, ok := jsonSegments(w, n.path)
	if !ok {
		return
	}
	switch w.d.Name() {
	case "postgres":
		w.raw("(")
		w.col(n.ref)
		w.raw(" #>> '{" + strings.Join(segs, ",") + "}')::boolean = ")
		w.arg(n.val)
	case "mysql":
		// compare JSON boolean with a JSON literal, avoiding int coercion traps
		w.raw("JSON_EXTRACT(")
		w.col(n.ref)
		w.raw(", '$." + strings.Join(segs, ".") + "') = CAST(")
		if n.val {
			w.arg("true")
		} else {
			w.arg("false")
		}
		w.raw(" AS JSON)")
	default: // sqlite stores JSON booleans as 1/0
		w.raw("json_extract(")
		w.col(n.ref)
		w.raw(", '$." + strings.Join(segs, ".") + "') = ")
		if n.val {
			w.arg(1)
		} else {
			w.arg(0)
		}
	}
}

// jsonArrContainsNode — containment inside a nested array.
type jsonArrContainsNode struct {
	ref  colRef
	path string
	val  any
}

func (n jsonArrContainsNode) writeSQL(w *sqlWriter) {
	segs, ok := jsonSegments(w, n.path)
	if !ok {
		return
	}
	switch w.d.Name() {
	case "postgres":
		w.col(n.ref)
		w.raw(" #> '{" + strings.Join(segs, ",") + "}' @> ")
		w.arg(JSONValue(n.val))
		w.raw("::jsonb")
	case "mysql":
		w.raw("JSON_CONTAINS(")
		w.col(n.ref)
		w.raw(", ")
		w.arg(JSONValue(n.val))
		w.raw(", '$." + strings.Join(segs, ".") + "')")
	default:
		w.fail("sorm: json array Contains is not supported on " + w.d.Name())
	}
}

// jsonSegments validates and splits a dot-notation path.
func jsonSegments(w *sqlWriter, path string) ([]string, bool) {
	segs := strings.Split(path, ".")
	for _, s := range segs {
		if !jsonSegmentRe.MatchString(s) {
			w.fail(fmt.Sprintf("sorm: invalid json path segment %q (want [A-Za-z0-9_]+)", s))
			return nil, false
		}
	}
	return segs, true
}

type jsonContainsNode struct {
	ref colRef
	val any
}

func (n jsonContainsNode) writeSQL(w *sqlWriter) {
	switch w.d.Name() {
	case "postgres":
		w.col(n.ref)
		w.raw(" @> ")
		w.arg(JSONValue(n.val))
		w.raw("::jsonb")
	case "mysql":
		w.raw("JSON_CONTAINS(")
		w.col(n.ref)
		w.raw(", ")
		w.arg(JSONValue(n.val))
		w.raw(")")
	default:
		w.fail("sorm: json Contains is not supported on " + w.d.Name())
	}
}

type jsonHasKeyNode struct {
	ref colRef
	key string
}

func (n jsonHasKeyNode) writeSQL(w *sqlWriter) {
	if !jsonSegmentRe.MatchString(n.key) {
		w.fail(fmt.Sprintf("sorm: invalid json key %q (want [A-Za-z0-9_]+)", n.key))
		return
	}
	switch w.d.Name() {
	case "postgres":
		// jsonb_exists avoids the `?` operator, which collides with
		// database/sql-style placeholders in some drivers/tools.
		w.raw("jsonb_exists(")
		w.col(n.ref)
		w.raw(", ")
		w.arg(n.key)
		w.raw(")")
	case "mysql":
		w.raw("JSON_CONTAINS_PATH(")
		w.col(n.ref)
		w.raw(", 'one', '$." + n.key + "')")
	default: // sqlite
		w.raw("json_type(")
		w.col(n.ref)
		w.raw(", '$." + n.key + "') IS NOT NULL")
	}
}
