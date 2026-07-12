package otelsorm_test

import (
	"context"
	"database/sql"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	_ "modernc.org/sqlite"

	"sorm"
	"sorm/dialect/lite"
	"sorm/driver/sqld"
	models "sorm/internal/testmodels"
	gen "sorm/internal/testmodels/sormgen"
	"sorm/otelsorm"
)

func TestSpans(t *testing.T) {
	ctx := context.Background()

	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	defer sdb.Close()
	if _, err := sdb.Exec(`CREATE TABLE tags (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT NOT NULL UNIQUE)`); err != nil {
		t.Fatal(err)
	}

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	db := otelsorm.Wrap(sqld.Wrap(sdb, lite.Dialect{}), otelsorm.WithTracerProvider(tp))

	s := sorm.NewSession(db)
	sorm.Add(s, &models.Tag{Label: "otel"})
	if err := s.SaveChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sorm.Query[models.Tag](db).Where(gen.Tag.Label.Eq("otel")).All(ctx); err != nil {
		t.Fatal(err)
	}

	spans := recorder.Ended()
	byName := map[string]int{}
	var stmtSeen bool
	for _, sp := range spans {
		byName[sp.Name()]++
		for _, a := range sp.Attributes() {
			if a.Key == "db.statement" && a.Value.AsString() != "" {
				stmtSeen = true
			}
			if a.Key == "db.system" && a.Value.AsString() != "sqlite" {
				t.Errorf("db.system = %q", a.Value.AsString())
			}
		}
	}
	// SaveChanges: begin + batch + commit; затем query.
	for _, want := range []string{"sorm.begin", "sorm.batch", "sorm.commit", "sorm.query"} {
		if byName[want] == 0 {
			t.Errorf("нет спана %s (есть: %v)", want, byName)
		}
	}
	if !stmtSeen {
		t.Error("ни один спан не несёт db.statement")
	}
}
