package sorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect"
	"github.com/dvislobokov/sorm/dialect/pg"
	models "github.com/dvislobokov/sorm/internal/testmodels"
	gen "github.com/dvislobokov/sorm/internal/testmodels/sormgen"
)

// fakeDB records which node served each operation. Query returns an
// empty result set; Exec reports success.
type fakeDB struct {
	name string
	log  *[]string
}

func (f fakeDB) Dialect() dialect.Dialect { return pg.Dialect{} }

func (f fakeDB) Query(_ context.Context, sql string, _ ...any) (sorm.Rows, error) {
	*f.log = append(*f.log, f.name+":query")
	return emptyRows{}, nil
}

func (f fakeDB) Exec(_ context.Context, sql string, _ ...any) (int64, error) {
	*f.log = append(*f.log, f.name+":exec")
	return 1, nil
}

func (f fakeDB) ExecBatch(_ context.Context, items []sorm.BatchItem) error {
	*f.log = append(*f.log, f.name+":batch")
	return nil
}

func (f fakeDB) Begin(context.Context) (sorm.Tx, error) {
	*f.log = append(*f.log, f.name+":begin")
	return fakeTx{f}, nil
}

type fakeTx struct{ fakeDB }

func (fakeTx) Commit(context.Context) error   { return nil }
func (fakeTx) Rollback(context.Context) error { return nil }

type emptyRows struct{}

func (emptyRows) Next() bool             { return false }
func (emptyRows) Scan(...any) error      { return nil }
func (emptyRows) Close()                 {}
func (emptyRows) Err() error             { return nil }
func (emptyRows) Columns() []string      { return nil }

func TestResolverRouting(t *testing.T) {
	var log []string
	primary := fakeDB{name: "primary", log: &log}
	r1 := fakeDB{name: "r1", log: &log}
	r2 := fakeDB{name: "r2", log: &log}
	db := sorm.WithReplicas(primary, r1, r2)
	ctx := context.Background()

	// Untracked reads: round-robin over replicas.
	_, _ = sorm.Query[models.User](db).All(ctx)
	_, _ = sorm.Query[models.User](db).All(ctx)
	_, _ = sorm.Query[models.User](db).All(ctx)
	if got := strings.Join(log, ","); got != "r1:query,r2:query,r1:query" {
		t.Fatalf("reads: %s", got)
	}

	// Writes: primary.
	log = nil
	_, _ = sorm.Update[models.User](db).Set(gen.User.Name.Set("x")).AllRows().Exec(ctx)
	if strings.Join(log, ",") != "primary:exec" {
		t.Fatalf("write: %v", log)
	}

	// Locked read: primary even though it is a SELECT.
	log = nil
	_, _ = sorm.Query[models.User](db).ForUpdate().All(ctx)
	if strings.Join(log, ",") != "primary:query" {
		t.Fatalf("locked read: %v", log)
	}

	// Session / Context: everything on the primary (read-your-writes).
	log = nil
	c := gen.NewContext(db)
	_, _ = c.Users.All(ctx)
	c.Users.Add(&models.User{Email: "x@x", Name: "X"})
	_ = c.SaveChanges(ctx)
	for _, op := range log {
		if !strings.HasPrefix(op, "primary:") {
			t.Fatalf("session op left the primary: %v", log)
		}
	}

	// Transactions: primary.
	log = nil
	_ = sorm.RunInTx(ctx, db, func(tx sorm.Tx) error {
		_, err := sorm.Query[models.User](tx).All(ctx)
		return err
	})
	if log[0] != "primary:begin" || log[1] != "primary:query" {
		t.Fatalf("tx: %v", log)
	}

	// Explicit pins.
	log = nil
	_, _ = sorm.Query[models.User](sorm.Primary(db)).All(ctx)
	_, _ = sorm.Query[models.User](sorm.Replica(db)).All(ctx)
	if log[0] != "primary:query" || !strings.HasSuffix(log[1], ":query") || strings.HasPrefix(log[1], "primary") {
		t.Fatalf("pins: %v", log)
	}
}

// InSchema outside the resolver: routing still works and the schema is
// applied to whichever node serves the query.
func TestResolverWithSchema(t *testing.T) {
	var log []string
	primary := fakeDB{name: "primary", log: &log}
	r1 := fakeDB{name: "r1", log: &log}
	db := sorm.InSchema(sorm.WithReplicas(primary, r1), "tenant")
	ctx := context.Background()

	sql, _ := sorm.Query[models.User](db).ToSQL()
	if !strings.Contains(sql, `FROM "tenant"."users"`) {
		t.Fatalf("schema lost: %s", sql)
	}
	_, _ = sorm.Query[models.User](db).All(ctx)
	if log[0] != "r1:query" {
		t.Fatalf("replica routing through schema wrapper: %v", log)
	}

	// The session unwraps to primary AND keeps the schema.
	log = nil
	s := sorm.NewSession(db)
	sq, _ := sorm.Track[models.User](s).ToSQL()
	if !strings.Contains(sq, `FROM "tenant"."users"`) {
		t.Fatalf("session lost schema: %s", sq)
	}
	_, _ = sorm.Track[models.User](s).All(ctx)
	if log[0] != "primary:query" {
		t.Fatalf("tracked read must hit primary: %v", log)
	}
}
