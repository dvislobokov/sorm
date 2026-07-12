// Package otelsorm provides OpenTelemetry tracing for sorm on top of
// sorm.Instrument:
//
//	db = otelsorm.Wrap(db)
//
// Every DB operation (query/exec/batch/begin/commit/rollback) becomes a
// span with db.system, db.statement, and db.operation.batch.size attributes;
// errors are recorded in the span status. The sorm core does not depend on
// OpenTelemetry — the dependency is linked only when this package is imported.
package otelsorm

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/dvislobokov/sorm"
)

type config struct {
	tracer         trace.Tracer
	includeArgs    bool
	spanNamePrefix string
}

// Option configures the wrapper.
type Option func(*config)

// WithTracerProvider sets the provider (defaults to the global otel one).
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) { c.tracer = tp.Tracer("github.com/dvislobokov/sorm") }
}

// WithArgs enables recording query arguments in span attributes.
// Disabled by default: arguments often contain sensitive data.
func WithArgs() Option {
	return func(c *config) { c.includeArgs = true }
}

// Wrap wraps a sorm.DB with tracing.
func Wrap(db sorm.DB, opts ...Option) sorm.DB {
	cfg := &config{tracer: otel.Tracer("github.com/dvislobokov/sorm")}
	for _, o := range opts {
		o(cfg)
	}
	system := db.Dialect().Name()

	return sorm.Instrument(db, func(ctx context.Context, op sorm.Op, next func(context.Context) error) error {
		attrs := []attribute.KeyValue{
			attribute.String("db.system", system),
			attribute.String("db.operation", op.Kind),
		}
		if op.SQL != "" {
			attrs = append(attrs, attribute.String("db.statement", op.SQL))
		}
		if op.Kind == "batch" {
			attrs = append(attrs, attribute.Int("db.operation.batch.size", len(op.Statements)))
		}
		if cfg.includeArgs && len(op.Args) > 0 {
			args := make([]string, len(op.Args))
			for i, a := range op.Args {
				args[i] = fmt.Sprint(a)
			}
			attrs = append(attrs, attribute.StringSlice("db.query.parameters", args))
		}

		ctx, span := cfg.tracer.Start(ctx, "sorm."+op.Kind,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attrs...),
		)
		defer span.End()

		err := next(ctx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	})
}
