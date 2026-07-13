package sorm

import (
	"fmt"
	"reflect"
	"sync"
	"time"
)

// Meta describes an entity to the runtime. In production code it is generated
// by `sorm gen`; handwritten meta is fine for tests and transition periods.
// All functions are per-field, no reflection — the only runtime reflection
// is a single type lookup in the registry when building a query.
type Meta[E any] struct {
	Table      string
	PK         string // PK column name
	Auto       bool   // PK is DB-generated (identity) → INSERT ... RETURNING
	VersionCol string // "" — no optimistic concurrency

	SelectCols []string // order matches Scan
	InsertCols []string // without auto-PK; order matches InsertValues

	// Scan returns pointers to fields in SelectCols order.
	Scan func(*E) []any
	// InsertValues returns values in InsertCols order.
	InsertValues func(*E) []any
	// ValuesFor returns column values by SelectCols indexes (bridge diff → UPDATE).
	ValuesFor func(*E, []int) []any
	// Snapshot/Diff — snapshot tracking (PR #4). The snapshot is a typed
	// generated struct, boxed at the meta boundary.
	Snapshot func(*E) any
	Diff     func(any, *E) []int
	// SetPK sets the auto-PK after RETURNING.
	SetPK func(*E, int64)
	// PKValue — the PK value (identity map key).
	PKValue func(*E) any
	// GetVersion/SetVersion — only when VersionCol != "".
	GetVersion func(*E) int64
	SetVersion func(*E, int64)
	// SoftDeleteCol — the column of `sorm:"softDelete"` (a *time.Time);
	// "" disables soft deletion. When set: queries filter the column
	// IS NULL, session Remove and set-based Delete become UPDATEs.
	SoftDeleteCol string
	// SetDeleted stamps the soft-delete field (only when SoftDeleteCol != "").
	SetDeleted func(*E, time.Time)
	// TouchCreate/TouchUpdate — auto-timestamps (sorm:"autoCreate"/"autoUpdate").
	// Nil when the entity has no such fields. TouchCreate stamps zero-valued
	// autoCreate fields on INSERT (a manually set value wins); TouchUpdate
	// stamps autoUpdate fields on INSERT and on every effective UPDATE.
	TouchCreate func(*E, time.Time)
	TouchUpdate func(*E, time.Time)
	// Refs — belongsTo navigations of this entity: edges for per-instance
	// toposort and FK fixup of new graphs.
	Refs []Ref[E]
	// RefTables — tables referenced by FKs (deletion order).
	RefTables []string
}

// Ref — a many-to-one navigation reference generated for an FK column.
type Ref[E any] struct {
	FKCol   string
	NotNull bool
	// Nav — pointer to the parent as any (nil-safe: no typed-nil leaks).
	Nav func(*E) any
	// NavPK — the parent's PK; valid only when Nav != nil.
	NavPK func(*E) any
	// SetFK sets the FK from the parent's PK (fixup after the parent is inserted).
	SetFK func(*E, any)
	// FKIsZero — the FK column was not set manually.
	FKIsZero func(*E) bool
}

var registry sync.Map // reflect.Type -> *Meta[E] (as any)

// Register registers an entity's meta. Called from the generated package's init().
func Register[E any](m Meta[E]) {
	registry.Store(reflect.TypeFor[E](), &m)
}

// MetaOf returns the registered meta of an entity — for tests and
// advanced scenarios (instrumentation, custom executors).
// Panics if no meta is registered.
func MetaOf[E any]() *Meta[E] { return metaFor[E]() }

func metaFor[E any]() *Meta[E] {
	v, ok := registry.Load(reflect.TypeFor[E]())
	if !ok {
		panic(fmt.Sprintf(
			"sorm: no Meta registered for %v — import the generated sormgen package (or run `sorm gen`)",
			reflect.TypeFor[E](),
		))
	}
	return v.(*Meta[E])
}
