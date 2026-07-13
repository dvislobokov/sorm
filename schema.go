package sorm

import "context"

// InSchema binds a connection to a database schema: every table rendered
// by sorm becomes schema-qualified ("billing"."orders"). Models stay
// schema-agnostic — the same entities can live in different schemas via
// different wrappers over one pool (per-schema multi-tenancy):
//
//	db := sorm.InSchema(pgxd.Wrap(pool), "billing")
//	c  := sormgen.NewContext(db)   // the context inherits the schema
//
// On MySQL a "schema" is a database name (`billing`.`orders`); on SQLite
// it is an attached database name. Raw/RawAs SQL is the caller's text and
// is not rewritten. For migrations see migrate.WithSchema.
func InSchema(db DB, schema string) DB {
	return schemaDB{DB: db, schema: schema}
}

type schemaProvider interface{ Schema() string }

// schemaOf extracts the schema of an InSchema-wrapped connection ("" otherwise).
func schemaOf(db DB) string {
	if sp, ok := db.(schemaProvider); ok {
		return sp.Schema()
	}
	return ""
}

type schemaDB struct {
	DB
	schema string
}

func (s schemaDB) Schema() string { return s.schema }

// Primary/Replica compose with WithReplicas: routing happens inside,
// the schema stays on the outside.
func (s schemaDB) Primary() DB { return schemaDB{DB: Primary(s.DB), schema: s.schema} }
func (s schemaDB) Replica() DB { return schemaDB{DB: Replica(s.DB), schema: s.schema} }

// Begin wraps the transaction so the schema survives RunInTx and sessions.
func (s schemaDB) Begin(ctx context.Context) (Tx, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return schemaTx{Tx: tx, schema: s.schema}, nil
}

// RetryableError / EmitOp delegate to the wrapped DB so RunInTx retries
// and instrumentation keep working through this wrapper.
func (s schemaDB) RetryableError(err error) bool {
	if rc, ok := s.DB.(retryClassifier); ok {
		return rc.RetryableError(err)
	}
	return false
}

func (s schemaDB) EmitOp(ctx context.Context, op Op) {
	if em, ok := s.DB.(interface {
		EmitOp(context.Context, Op)
	}); ok {
		em.EmitOp(ctx, op)
	}
}

type schemaTx struct {
	Tx
	schema string
}

func (s schemaTx) Schema() string { return s.schema }

func (s schemaTx) RetryableError(err error) bool {
	if rc, ok := s.Tx.(retryClassifier); ok {
		return rc.RetryableError(err)
	}
	return false
}
