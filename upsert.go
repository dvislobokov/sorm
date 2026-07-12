package sorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dvislobokov/sorm/dialect"
)

// Upsert is a set-based INSERT ... ON CONFLICT (PostgreSQL/SQLite) /
// ON DUPLICATE KEY (MySQL):
//
//	n, err := sorm.Upsert[models.User](db).
//	    Rows(u1, u2).
//	    OnConflict(gen.User.Email).             // conflict target (PG/SQLite)
//	    DoUpdate(gen.User.Name, gen.User.Age).  // set these from the new values
//	    Exec(ctx)
//
// Semantics and caveats:
//   - OnConflict is required on PostgreSQL/SQLite and ignored on MySQL
//     (MySQL fires on ANY unique key — that is the engine's rule).
//   - DoUpdate columns are updated from the values being inserted
//     (excluded.col / VALUES(col)); the version column of optimistic
//     concurrency is bumped automatically on the update path.
//   - DoNothing() skips conflicting rows instead of updating them.
//   - autoCreate/autoUpdate timestamps are stamped on the inserted values.
//   - Auto-generated PKs are NOT written back into the entities (unlike
//     Session inserts) — this is a set-based statement; reload if you
//     need the keys. Exec returns the driver-reported affected count,
//     whose meaning differs per engine (MySQL counts an update as 2).
func Upsert[E any](db DB) UpsertBuilder[E] {
	return UpsertBuilder[E]{db: db, meta: metaFor[E](), d: dialectOf(db), schema: schemaOf(db)}
}

type UpsertBuilder[E any] struct {
	db        DB
	meta      *Meta[E]
	d         dialect.Dialect
	schema    string
	rows      []*E
	conflict  []string // conflict target column names
	updates   []string // columns to update on conflict
	doNothing bool
	name      string
}

// Named labels the statement for instrumentation (sorm.query.name).
func (q UpsertBuilder[E]) Named(name string) UpsertBuilder[E] {
	q.name = name
	return q
}

// Rows adds entities to insert.
func (q UpsertBuilder[E]) Rows(es ...*E) UpsertBuilder[E] {
	q.rows = append(append([]*E{}, q.rows...), es...)
	return q
}

// OnConflict sets the conflict target (a unique column or unique-index
// column set). Required on PostgreSQL/SQLite; ignored on MySQL.
func (q UpsertBuilder[E]) OnConflict(cols ...ColOf[E]) UpsertBuilder[E] {
	for _, c := range cols {
		q.conflict = append(append([]string{}, q.conflict...), c.ColName())
	}
	return q
}

// DoUpdate lists the columns to overwrite with the incoming values when
// the row already exists.
func (q UpsertBuilder[E]) DoUpdate(cols ...ColOf[E]) UpsertBuilder[E] {
	for _, c := range cols {
		q.updates = append(append([]string{}, q.updates...), c.ColName())
	}
	return q
}

// DoNothing skips conflicting rows entirely.
func (q UpsertBuilder[E]) DoNothing() UpsertBuilder[E] {
	q.doNothing = true
	return q
}

func (q UpsertBuilder[E]) ToSQL() (string, []any, error) {
	m := q.meta
	switch {
	case len(q.rows) == 0:
		return "", nil, errors.New("sorm: upsert without Rows")
	case q.doNothing && len(q.updates) > 0:
		return "", nil, errors.New("sorm: upsert: DoUpdate and DoNothing are mutually exclusive")
	case !q.doNothing && len(q.updates) == 0:
		return "", nil, errors.New("sorm: upsert: choose DoUpdate(cols...) or DoNothing()")
	case len(q.conflict) == 0 && q.d.Name() != "mysql":
		return "", nil, errors.New("sorm: upsert: OnConflict(cols...) is required on " + q.d.Name())
	}
	insertable := map[string]bool{}
	for _, c := range m.InsertCols {
		insertable[c] = true
	}
	for _, c := range q.updates {
		if !insertable[c] {
			return "", nil, fmt.Errorf("sorm: upsert: %q is not an insertable column of %s", c, m.Table)
		}
	}

	w := newSchemaSQLWriter(q.d, q.schema)
	w.raw("INSERT INTO ")
	w.table(m.Table)
	w.raw(" (")
	for i, c := range m.InsertCols {
		if i > 0 {
			w.raw(", ")
		}
		w.ident(c)
	}
	w.raw(") VALUES ")
	now := time.Now()
	for r, e := range q.rows {
		// Same lifecycle as session inserts: timestamps and version init.
		if m.TouchCreate != nil {
			m.TouchCreate(e, now)
		}
		if m.TouchUpdate != nil {
			m.TouchUpdate(e, now)
		}
		if m.VersionCol != "" && m.GetVersion(e) == 0 {
			m.SetVersion(e, 1)
		}
		if r > 0 {
			w.raw(", ")
		}
		w.raw("(")
		for i, v := range m.InsertValues(e) {
			if i > 0 {
				w.raw(", ")
			}
			w.arg(v)
		}
		w.raw(")")
	}

	if q.d.Name() == "mysql" {
		w.raw(" ON DUPLICATE KEY UPDATE ")
		if q.doNothing {
			// MySQL has no DO NOTHING; a self-assignment of the PK is the
			// canonical no-op (INSERT IGNORE would swallow real errors).
			w.ident(m.PK)
			w.raw(" = ")
			w.ident(m.PK)
		} else {
			q.writeUpdates(w, func(col string) {
				w.raw("VALUES(")
				w.ident(col)
				w.raw(")")
			})
		}
		return w.sb.String(), w.args, w.err
	}

	// PostgreSQL / SQLite.
	w.raw(" ON CONFLICT (")
	for i, c := range q.conflict {
		if i > 0 {
			w.raw(", ")
		}
		w.ident(c)
	}
	w.raw(")")
	if q.doNothing {
		w.raw(" DO NOTHING")
		return w.sb.String(), w.args, w.err
	}
	w.raw(" DO UPDATE SET ")
	q.writeUpdates(w, func(col string) {
		w.raw("excluded.")
		w.ident(col)
	})
	return w.sb.String(), w.args, w.err
}

// writeUpdates emits "col = <new value ref>, ..." plus the automatic
// version bump for optimistic-concurrency entities.
func (q UpsertBuilder[E]) writeUpdates(w *sqlWriter, newValue func(col string)) {
	for i, c := range q.updates {
		if i > 0 {
			w.raw(", ")
		}
		w.ident(c)
		w.raw(" = ")
		newValue(c)
	}
	if v := q.meta.VersionCol; v != "" {
		w.raw(", ")
		w.ident(v)
		w.raw(" = ")
		if q.d.Name() != "mysql" {
			// Qualify with the table name: inside DO UPDATE a bare column
			// can be ambiguous with excluded on PostgreSQL.
			w.ident(q.meta.Table)
			w.raw(".")
		}
		w.ident(v)
		w.raw(" + 1")
	}
}

func (q UpsertBuilder[E]) Exec(ctx context.Context) (int64, error) {
	ctx = named(ctx, q.name)
	sqlStr, args, err := q.ToSQL()
	if err != nil {
		return 0, err
	}
	n, err := q.db.Exec(ctx, sqlStr, args...)
	if err != nil {
		return 0, fmt.Errorf("sorm: upsert %s: %w", q.meta.Table, err)
	}
	return n, nil
}
