// Package otelsorm provides OpenTelemetry tracing AND metrics for sorm on
// top of sorm.Instrument:
//
//	db = otelsorm.Wrap(db)
//
// Every database operation (query/exec/batch/begin/commit/rollback) becomes
// a client span, and the following metrics are recorded (all carry
// db.system, db.operation.name, sorm.query.name when set via
// sorm.WithQueryName / builder .Named(), and best-effort db.collection.name):
//
//	db.client.operation.duration  histogram (s)   OTel semconv-compatible latency
//	sorm.db.batch.size            histogram       statements per write batch
//	sorm.db.statements            counter         by db.statement.kind (insert/update/delete/select)
//	sorm.db.errors                counter         by error.type (conflict, constraint.unique, transient, ...)
//	sorm.db.rows.returned         histogram       rows fetched per query
//	sorm.tx.duration              histogram (s)   begin→commit/rollback, by outcome
//	sorm.tx.retries               counter         RunInTx transient-error retries
//	sorm.pool.*                   gauges          optional, via WithDBStats/WithPoolStats
//
// SQL text is never put into metric attributes (unbounded cardinality); it
// stays on spans. The sorm core does not depend on OpenTelemetry — the
// dependency links only when this package is imported.
package otelsorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/dvislobokov/sorm"
)

const scopeName = "github.com/dvislobokov/sorm/otelsorm"

type config struct {
	tracer      trace.Tracer
	meter       metric.Meter
	includeArgs bool
	tableAttr   bool
	poolStats   func() PoolStats
}

// Option configures the wrapper.
type Option func(*config)

// WithTracerProvider sets the tracer provider (default: the global one).
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) { c.tracer = tp.Tracer(scopeName) }
}

// WithMeterProvider sets the meter provider (default: the global one; a
// no-op provider makes all metrics free).
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(c *config) { c.meter = mp.Meter(scopeName) }
}

// WithArgs records query arguments on spans.
// Off by default: arguments routinely contain sensitive data.
func WithArgs() Option {
	return func(c *config) { c.includeArgs = true }
}

// WithoutTableAttr disables the best-effort db.collection.name attribute.
func WithoutTableAttr() Option {
	return func(c *config) { c.tableAttr = false }
}

// PoolStats is a driver-neutral snapshot of connection-pool state.
type PoolStats struct {
	Max          int64
	Idle         int64
	Used         int64
	WaitCount    int64
	WaitDuration time.Duration
}

// WithPoolStats registers connection-pool gauges fed by the callback.
// For pgxpool:
//
//	otelsorm.WithPoolStats(func() otelsorm.PoolStats {
//	    s := pool.Stat()
//	    return otelsorm.PoolStats{
//	        Max: int64(s.MaxConns()), Idle: int64(s.IdleConns()),
//	        Used: int64(s.AcquiredConns()),
//	        WaitCount: s.EmptyAcquireCount(), WaitDuration: s.AcquireDuration(),
//	    }
//	})
func WithPoolStats(fn func() PoolStats) Option {
	return func(c *config) { c.poolStats = fn }
}

// WithDBStats registers connection-pool gauges from a database/sql pool.
func WithDBStats(sdb *sql.DB) Option {
	return WithPoolStats(func() PoolStats {
		s := sdb.Stats()
		return PoolStats{
			Max: int64(s.MaxOpenConnections), Idle: int64(s.Idle),
			Used: int64(s.InUse), WaitCount: s.WaitCount, WaitDuration: s.WaitDuration,
		}
	})
}

// Wrap instruments a sorm.DB with tracing and metrics.
func Wrap(db sorm.DB, opts ...Option) sorm.DB {
	cfg := &config{
		tracer:    otel.Tracer(scopeName),
		meter:     otel.Meter(scopeName),
		tableAttr: true,
	}
	for _, o := range opts {
		o(cfg)
	}
	m := newMetrics(cfg)
	system := db.Dialect().Name()
	classify := func(err error) string { return classifyError(db, err) }

	inner := sorm.Instrument(db, func(ctx context.Context, op sorm.Op, next func(context.Context) error) error {
		attrs := []attribute.KeyValue{
			attribute.String("db.system", system),
			attribute.String("db.operation.name", op.Kind),
		}
		if name := sorm.QueryNameFromContext(ctx); name != "" {
			attrs = append(attrs, attribute.String("sorm.query.name", name))
		}
		if cfg.tableAttr && op.SQL != "" {
			if table := tableOf(op.SQL); table != "" {
				attrs = append(attrs, attribute.String("db.collection.name", table))
			}
		}

		// Synthetic events: counted, no span/duration.
		if op.Kind == "tx.retry" {
			m.txRetries.Add(ctx, 1, metric.WithAttributes(attrs...))
			return next(ctx)
		}

		spanAttrs := attrs
		if op.SQL != "" {
			spanAttrs = append(spanAttrs, attribute.String("db.statement", op.SQL))
		}
		if op.Kind == "batch" {
			spanAttrs = append(spanAttrs, attribute.Int("db.operation.batch.size", len(op.Statements)))
		}
		if cfg.includeArgs && len(op.Args) > 0 {
			args := make([]string, len(op.Args))
			for i, a := range op.Args {
				args[i] = fmt.Sprint(a)
			}
			spanAttrs = append(spanAttrs, attribute.StringSlice("db.query.parameters", args))
		}

		ctx, span := cfg.tracer.Start(ctx, "sorm."+op.Kind,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(spanAttrs...),
		)
		defer span.End()

		start := time.Now()
		err := next(ctx)
		elapsed := time.Since(start).Seconds()

		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			errType := classify(err)
			attrs = append(attrs, attribute.String("error.type", errType))
			m.errors.Add(ctx, 1, metric.WithAttributes(
				attribute.String("db.system", system),
				attribute.String("error.type", errType),
			))
		}
		m.opDuration.Record(ctx, elapsed, metric.WithAttributes(attrs...))

		if op.Kind == "batch" {
			m.batchSize.Record(ctx, int64(len(op.Statements)),
				metric.WithAttributes(attribute.String("db.system", system)))
			for kind, n := range statementKinds(op.Statements) {
				m.statements.Add(ctx, n, metric.WithAttributes(
					attribute.String("db.system", system),
					attribute.String("db.statement.kind", kind),
				))
			}
		}
		return err
	})

	if cfg.poolStats != nil {
		registerPoolGauges(cfg.meter, system, cfg.poolStats)
	}
	return observer{inner: inner, m: m, system: system}
}

// --- metric instruments ---

type metrics struct {
	opDuration metric.Float64Histogram
	batchSize  metric.Int64Histogram
	statements metric.Int64Counter
	errors     metric.Int64Counter
	rows       metric.Int64Histogram
	txDuration metric.Float64Histogram
	txRetries  metric.Int64Counter
}

func newMetrics(cfg *config) *metrics {
	mt := cfg.meter
	opDur, _ := mt.Float64Histogram("db.client.operation.duration",
		metric.WithDescription("Duration of database client operations"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10))
	batch, _ := mt.Int64Histogram("sorm.db.batch.size",
		metric.WithDescription("Statements per write batch"),
		metric.WithUnit("{statement}"),
		metric.WithExplicitBucketBoundaries(1, 2, 5, 10, 25, 50, 100, 250, 500))
	stmts, _ := mt.Int64Counter("sorm.db.statements",
		metric.WithDescription("Executed statements by kind"),
		metric.WithUnit("{statement}"))
	errs, _ := mt.Int64Counter("sorm.db.errors",
		metric.WithDescription("Database errors by type"),
		metric.WithUnit("{error}"))
	rows, _ := mt.Int64Histogram("sorm.db.rows.returned",
		metric.WithDescription("Rows fetched per query"),
		metric.WithUnit("{row}"),
		metric.WithExplicitBucketBoundaries(1, 5, 25, 100, 500, 2500, 10000, 50000))
	txDur, _ := mt.Float64Histogram("sorm.tx.duration",
		metric.WithDescription("Transaction duration from begin to commit/rollback"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30))
	retries, _ := mt.Int64Counter("sorm.tx.retries",
		metric.WithDescription("RunInTx retries after transient errors"),
		metric.WithUnit("{retry}"))
	return &metrics{opDur, batch, stmts, errs, rows, txDur, retries}
}

func registerPoolGauges(mt metric.Meter, system string, stats func() PoolStats) {
	maxG, _ := mt.Int64ObservableGauge("sorm.pool.connections.max", metric.WithUnit("{connection}"))
	idleG, _ := mt.Int64ObservableGauge("sorm.pool.connections.idle", metric.WithUnit("{connection}"))
	usedG, _ := mt.Int64ObservableGauge("sorm.pool.connections.used", metric.WithUnit("{connection}"))
	waitC, _ := mt.Int64ObservableCounter("sorm.pool.acquire.wait_count", metric.WithUnit("{acquire}"))
	waitD, _ := mt.Float64ObservableCounter("sorm.pool.acquire.wait.duration", metric.WithUnit("s"))
	attrs := metric.WithAttributes(attribute.String("db.system", system))
	_, _ = mt.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := stats()
		o.ObserveInt64(maxG, s.Max, attrs)
		o.ObserveInt64(idleG, s.Idle, attrs)
		o.ObserveInt64(usedG, s.Used, attrs)
		o.ObserveInt64(waitC, s.WaitCount, attrs)
		o.ObserveFloat64(waitD, s.WaitDuration.Seconds(), attrs)
		return nil
	}, maxG, idleG, usedG, waitC, waitD)
}

// --- error classification for sorm.db.errors ---

func classifyError(db sorm.DB, err error) string {
	var conflict *sorm.ConflictError
	if errors.As(err, &conflict) {
		return "conflict"
	}
	var ce *sorm.ConstraintError
	if errors.As(err, &ce) {
		switch ce.Kind {
		case sorm.ConstraintUnique:
			return "constraint.unique"
		case sorm.ConstraintForeignKey:
			return "constraint.foreign_key"
		case sorm.ConstraintNotNull:
			return "constraint.not_null"
		case sorm.ConstraintCheck:
			return "constraint.check"
		}
	}
	if rc, ok := db.(interface{ RetryableError(error) bool }); ok && rc.RetryableError(err) {
		return "transient"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	return "other"
}

// --- statement classification (flush profile) ---

func statementKinds(stmts []string) map[string]int64 {
	out := map[string]int64{}
	for _, s := range stmts {
		s = strings.TrimSpace(s)
		switch {
		case hasPrefixFold(s, "INSERT"):
			out["insert"]++
		case hasPrefixFold(s, "UPDATE"):
			out["update"]++
		case hasPrefixFold(s, "DELETE"):
			out["delete"]++
		case hasPrefixFold(s, "SELECT"):
			out["select"]++
		default:
			out["other"]++
		}
	}
	return out
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// --- best-effort table extraction for db.collection.name ---

var tableRe = regexp.MustCompile("(?i)(?:FROM|INTO|UPDATE|JOIN)\\s+[\"`]?([A-Za-z0-9_]+)")

var (
	tableCacheMu sync.RWMutex
	tableCache   = map[string]string{}
)

const tableCacheCap = 2048

func tableOf(sql string) string {
	tableCacheMu.RLock()
	t, ok := tableCache[sql]
	tableCacheMu.RUnlock()
	if ok {
		return t
	}
	t = ""
	if m := tableRe.FindStringSubmatch(sql); m != nil {
		t = m[1]
	}
	tableCacheMu.Lock()
	if len(tableCache) < tableCacheCap {
		tableCache[sql] = t
	}
	tableCacheMu.Unlock()
	return t
}
