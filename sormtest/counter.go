package sormtest

import (
	"context"
	"sync/atomic"

	"github.com/dvislobokov/sorm"
)

// Counter tallies the statements a wrapped connection executed —
// the tool for asserting query budgets and catching N+1 in tests.
type Counter struct {
	selects atomic.Int64
	writes  atomic.Int64
}

// Selects — number of SELECT roundtrips (Query calls).
func (c *Counter) Selects() int64 { return c.selects.Load() }

// Writes — number of write statements (Exec + every batch item).
func (c *Counter) Writes() int64 { return c.writes.Load() }

// Total — all statements.
func (c *Counter) Total() int64 { return c.Selects() + c.Writes() }

// Reset zeroes the counters (e.g. after seeding).
func (c *Counter) Reset() {
	c.selects.Store(0)
	c.writes.Store(0)
}

// CountQueries wraps a connection with a statement counter:
//
//	db, queries := sormtest.CountQueries(sormtest.NewSQLite(t))
//	... load a page with includes ...
//	if queries.Selects() > 3 {
//	    t.Fatalf("N+1: %d selects for one page", queries.Selects())
//	}
func CountQueries(db sorm.DB) (sorm.DB, *Counter) {
	c := &Counter{}
	wrapped := sorm.Instrument(db, func(ctx context.Context, op sorm.Op, next func(context.Context) error) error {
		switch op.Kind {
		case "query":
			c.selects.Add(1)
		case "exec":
			c.writes.Add(1)
		case "batch":
			c.writes.Add(int64(len(op.Statements)))
		}
		return next(ctx)
	})
	return wrapped, c
}
