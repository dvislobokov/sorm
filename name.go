package sorm

import "context"

// Query naming: a developer-chosen label that flows to instrumentation
// (spans and metrics) so operators can tell WHICH logical query is slow
// or failing instead of staring at anonymous SELECTs.
//
// Two ways to attach a name:
//
//	// 1. Context — covers everything under it: sessions, SaveChanges,
//	//    transactions, raw SQL.
//	ctx = sorm.WithQueryName(ctx, "CreateOrder")
//
//	// 2. Builder sugar — for individual queries.
//	tasks, err := sorm.Query[models.Task](db).
//	    Named("GetOpenTasks").
//	    Where(t.Done.Eq(false)).
//	    All(ctx)
//
// Instrumentation middleware reads the name with QueryNameFromContext.

type queryNameKey struct{}

// WithQueryName returns a context carrying a logical query/operation name.
// Every database operation executed under this context is attributed to it.
func WithQueryName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, queryNameKey{}, name)
}

// QueryNameFromContext returns the query name attached by WithQueryName
// or a builder's Named, or "" if none.
func QueryNameFromContext(ctx context.Context) string {
	s, _ := ctx.Value(queryNameKey{}).(string)
	return s
}

// named applies a builder-level name to the context, unless the caller
// already set one explicitly (the explicit context wins).
func named(ctx context.Context, name string) context.Context {
	if name == "" || QueryNameFromContext(ctx) != "" {
		return ctx
	}
	return WithQueryName(ctx, name)
}
