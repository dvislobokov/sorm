// Package pgxd — адаптер pgx v5 (PostgreSQL): один roundtrip на батч
// через pgx.Batch. Оборачивает *pgxpool.Pool, *pgx.Conn или pgx.Tx.
package pgxd

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"sorm"
	"sorm/dialect"
	"sorm/dialect/pg"
)

// Pgx — общая поверхность pgxpool.Pool / *pgx.Conn / pgx.Tx.
type Pgx interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// Wrap оборачивает pgx-совместимый пул/соединение в sorm.DB.
func Wrap(p Pgx) sorm.DB { return db{p} }

type db struct{ p Pgx }

func (d db) Dialect() dialect.Dialect { return pg.Dialect{} }

func (d db) Query(ctx context.Context, sql string, args ...any) (sorm.Rows, error) {
	rows, err := d.p.Query(ctx, sql, args...)
	if err != nil {
		return nil, translate(err)
	}
	return pgxRows{rows}, nil
}

func (d db) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	ct, err := d.p.Exec(ctx, sql, args...)
	if err != nil {
		return 0, translate(err)
	}
	return ct.RowsAffected(), nil
}

// collectIDs читает RETURNING-строки multi-row INSERT'а.
func collectIDs(br pgx.BatchResults, n int) ([]int64, error) {
	rows, err := br.Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, n)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != n {
		return nil, fmt.Errorf("pgxd: RETURNING вернул %d id, ожидали %d", len(ids), n)
	}
	return ids, nil
}

// translate конвертирует ошибки констрейнтов PostgreSQL (класс 23xxx)
// в типизированный *sorm.ConstraintError.
func translate(err error) error {
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) {
		return err
	}
	var kind sorm.ConstraintKind
	switch pgErr.Code {
	case "23505":
		kind = sorm.ConstraintUnique
	case "23503":
		kind = sorm.ConstraintForeignKey
	case "23502":
		kind = sorm.ConstraintNotNull
	case "23514":
		kind = sorm.ConstraintCheck
	default:
		return err
	}
	name := pgErr.ConstraintName
	if name == "" {
		name = pgErr.ColumnName
	}
	return &sorm.ConstraintError{Kind: kind, Constraint: name, Err: err}
}

func (d db) ExecBatch(ctx context.Context, items []sorm.BatchItem) error {
	b := &pgx.Batch{}
	for _, it := range items {
		b.Queue(it.SQL, it.Args...)
	}
	br := d.p.SendBatch(ctx, b)
	for _, it := range items {
		if it.IDCount > 0 {
			ids, err := collectIDs(br, it.IDCount)
			if err != nil {
				br.Close()
				return translate(err)
			}
			it.OnIDs(ids)
			continue
		}
		ct, err := br.Exec()
		if err != nil {
			br.Close()
			return translate(err)
		}
		if it.Check != nil {
			if err := it.Check(ct.RowsAffected()); err != nil {
				br.Close()
				return err
			}
		}
	}
	return br.Close()
}

func (d db) Begin(ctx context.Context) (sorm.Tx, error) {
	beginner, ok := d.p.(interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	})
	if !ok {
		return nil, errors.New("pgxd: underlying connection cannot begin transactions")
	}
	pgtx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("pgxd: begin: %w", err)
	}
	return tx{db{pgtx}, pgtx}, nil
}

// RetryableError — transient-ошибки PostgreSQL, после которых транзакцию
// имеет смысл повторить: serialization_failure (40001) и deadlock_detected (40P01).
func (d db) RetryableError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

type tx struct {
	db
	t pgx.Tx
}

func (t tx) Commit(ctx context.Context) error   { return t.t.Commit(ctx) }
func (t tx) Rollback(ctx context.Context) error { return t.t.Rollback(ctx) }

type pgxRows struct{ r pgx.Rows }

func (r pgxRows) Next() bool             { return r.r.Next() }
func (r pgxRows) Scan(dest ...any) error { return r.r.Scan(dest...) }
func (r pgxRows) Err() error             { return r.r.Err() }
func (r pgxRows) Close()                 { r.r.Close() }

func (r pgxRows) Columns() []string {
	fds := r.r.FieldDescriptions()
	out := make([]string, len(fds))
	for i, fd := range fds {
		out[i] = fd.Name
	}
	return out
}
