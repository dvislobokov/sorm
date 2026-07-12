package otelsorm_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	_ "modernc.org/sqlite"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect"
	"github.com/dvislobokov/sorm/dialect/lite"
	"github.com/dvislobokov/sorm/driver/sqld"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	_ "github.com/dvislobokov/sorm/internal/testmodels/sormgen" // registers metas
	"github.com/dvislobokov/sorm/otelsorm"
)

func metricsSetup(t *testing.T) (*sdkmetric.ManualReader, sorm.DB, *sql.DB) {
	t.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	t.Cleanup(func() { sdb.Close() })
	for _, ddl := range []string{
		`CREATE TABLE tags (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT NOT NULL UNIQUE)`,
	} {
		if _, err := sdb.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	db := otelsorm.Wrap(sqld.Wrap(sdb, lite.Dialect{}),
		otelsorm.WithMeterProvider(mp),
		otelsorm.WithDBStats(sdb),
	)
	return reader, db, sdb
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func TestMetricsEndToEnd(t *testing.T) {
	reader, db, _ := metricsSetup(t)
	ctx := context.Background()

	// A named write (session) and a named read.
	s := sorm.NewSession(db)
	sorm.Add(s, &models.Tag{Label: "metrics"})
	if err := s.SaveChanges(sorm.WithQueryName(ctx, "CreateTag")); err != nil {
		t.Fatal(err)
	}
	if _, err := sorm.Query[models.Tag](db).Named("GetTags").All(ctx); err != nil {
		t.Fatal(err)
	}
	// A unique violation for the error counter.
	s2 := sorm.NewSession(db)
	sorm.Add(s2, &models.Tag{Label: "metrics"})
	if err := s2.SaveChanges(ctx); !sorm.IsUniqueViolation(err) {
		t.Fatalf("expected unique violation, got %v", err)
	}

	ms := collect(t, reader)

	// db.client.operation.duration exists and carries query names.
	dur, ok := ms["db.client.operation.duration"].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatal("no operation.duration histogram")
	}
	names := map[string]bool{}
	kinds := map[string]bool{}
	for _, dp := range dur.DataPoints {
		if v, ok := dp.Attributes.Value("sorm.query.name"); ok {
			names[v.AsString()] = true
		}
		if v, ok := dp.Attributes.Value("db.operation.name"); ok {
			kinds[v.AsString()] = true
		}
	}
	if !names["CreateTag"] || !names["GetTags"] {
		t.Fatalf("query names missing in duration attrs: %v", names)
	}
	for _, want := range []string{"query", "batch", "begin", "commit"} {
		if !kinds[want] {
			t.Fatalf("operation kind %q missing: %v", want, kinds)
		}
	}

	// Batch size and statement kinds.
	if _, ok := ms["sorm.db.batch.size"]; !ok {
		t.Fatal("no batch.size histogram")
	}
	stmts, ok := ms["sorm.db.statements"].Data.(metricdata.Sum[int64])
	if !ok || !hasAttr(stmts.DataPoints, "db.statement.kind", "insert") {
		t.Fatalf("statements counter missing insert kind")
	}

	// Error counter classified the unique violation.
	errs, ok := ms["sorm.db.errors"].Data.(metricdata.Sum[int64])
	if !ok || !hasAttr(errs.DataPoints, "error.type", "constraint.unique") {
		t.Fatalf("errors counter missing constraint.unique")
	}

	// Rows returned recorded for the read.
	if _, ok := ms["sorm.db.rows.returned"]; !ok {
		t.Fatal("no rows.returned histogram")
	}

	// Transaction duration with commit outcome.
	txDur, ok := ms["sorm.tx.duration"].Data.(metricdata.Histogram[float64])
	if !ok || !hasHistAttr(txDur.DataPoints, "outcome", "commit") {
		t.Fatal("tx.duration missing commit outcome")
	}

	// Pool gauges present (WithDBStats).
	if _, ok := ms["sorm.pool.connections.max"]; !ok {
		t.Fatal("no pool gauges")
	}
}

// stubDB forces RunInTx retries: fn errors are classified as transient.
type stubDB struct{ sorm.DB }

var errFlaky = errors.New("flaky")

func (s stubDB) RetryableError(err error) bool { return errors.Is(err, errFlaky) }
func (s stubDB) Dialect() dialect.Dialect      { return lite.Dialect{} }

func TestTxRetryMetric(t *testing.T) {
	ctx := context.Background()

	// A transient classifier wrapped INSIDE otelsorm: RunInTx consults the
	// outermost DB, and the observer delegates down the chain.
	sdb2, _ := sql.Open("sqlite", ":memory:")
	sdb2.SetMaxOpenConns(1)
	defer sdb2.Close()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	flakyDB := otelsorm.Wrap(stubDB{sqld.Wrap(sdb2, lite.Dialect{})},
		otelsorm.WithMeterProvider(mp))

	attempts := 0
	err := sorm.RunInTx(ctx, flakyDB, func(tx sorm.Tx) error {
		attempts++
		if attempts < 3 {
			return errFlaky
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}

	ms := collect(t, reader)
	retries, ok := ms["sorm.tx.retries"].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatal("no tx.retries counter")
	}
	var total int64
	for _, dp := range retries.DataPoints {
		total += dp.Value
	}
	if total != 2 {
		t.Fatalf("retries = %d, want 2", total)
	}
}

func TestNamedPropagation(t *testing.T) {
	// The core-level contract: Named puts the name into the ctx seen by
	// instrumentation; explicit WithQueryName wins.
	var seen []string
	base := sqliteMem(t)
	db := sorm.Instrument(base, func(ctx context.Context, op sorm.Op, next func(context.Context) error) error {
		if op.Kind == "query" {
			seen = append(seen, sorm.QueryNameFromContext(ctx))
		}
		return next(ctx)
	})
	ctx := context.Background()

	if _, err := sorm.Query[models.Tag](db).Named("A").All(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sorm.Query[models.Tag](db).Named("B").All(sorm.WithQueryName(ctx, "explicit")); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "A" || seen[1] != "explicit" {
		t.Fatalf("seen = %v", seen)
	}
}

func sqliteMem(t *testing.T) sorm.DB {
	t.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	t.Cleanup(func() { sdb.Close() })
	if _, err := sdb.Exec(`CREATE TABLE tags (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT NOT NULL UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	return sqld.Wrap(sdb, lite.Dialect{})
}

func hasAttr(dps []metricdata.DataPoint[int64], key, val string) bool {
	for _, dp := range dps {
		if v, ok := dp.Attributes.Value(attribute.Key(key)); ok && v.AsString() == val {
			return true
		}
	}
	return false
}

func hasHistAttr(dps []metricdata.HistogramDataPoint[float64], key, val string) bool {
	for _, dp := range dps {
		if v, ok := dp.Attributes.Value(attribute.Key(key)); ok && v.AsString() == val {
			return true
		}
	}
	return false
}
