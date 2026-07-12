// Package otelsorm — OpenTelemetry-трейсинг для sorm поверх sorm.Instrument:
//
//	db = otelsorm.Wrap(db)
//
// Каждая операция БД (query/exec/batch/begin/commit/rollback) становится
// спаном с атрибутами db.system, db.statement и db.operation.batch.size;
// ошибки записываются в статус спана. Ядро sorm от OpenTelemetry не
// зависит — зависимость линкуется только при импорте этого пакета.
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

// Option — настройка обёртки.
type Option func(*config)

// WithTracerProvider задаёт провайдер (по умолчанию — глобальный otel).
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) { c.tracer = tp.Tracer("github.com/dvislobokov/sorm") }
}

// WithArgs включает запись аргументов запроса в атрибуты спана.
// По умолчанию выключено: аргументы часто содержат чувствительные данные.
func WithArgs() Option {
	return func(c *config) { c.includeArgs = true }
}

// Wrap оборачивает sorm.DB трейсингом.
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
