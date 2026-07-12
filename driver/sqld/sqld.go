// Package sqld is the database/sql adapter (MySQL, SQLite). Batches execute
// sequentially on the current connection/transaction; auto-PKs come via
// RETURNING (if the dialect supports it) or LastInsertId.
package sqld

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect"
)

// Wrap wraps a *sql.DB into a sorm.DB with the given dialect
// (dialect/my for MySQL, dialect/lite for SQLite).
func Wrap(sdb *sql.DB, d dialect.Dialect) sorm.DB {
	return db{q: sdb, d: d, beginner: sdb}
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type db struct {
	q        queryer
	d        dialect.Dialect
	beginner *sql.DB // nil inside a transaction
}

func (d db) Dialect() dialect.Dialect { return d.d }

func (d db) Query(ctx context.Context, query string, args ...any) (sorm.Rows, error) {
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, d.translate(err)
	}
	cols, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, err
	}
	return &sqlRows{r: rows, cols: cols}, nil
}

func (d db) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := d.q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, d.translate(err)
	}
	return res.RowsAffected()
}

func (d db) ExecBatch(ctx context.Context, items []sorm.BatchItem) error {
	for _, it := range items {
		if it.IDCount > 0 {
			if d.d.ReturningSupported() {
				ids, err := d.queryIDs(ctx, it)
				if err != nil {
					return d.translate(err)
				}
				it.OnIDs(ids)
				continue
			}
			res, err := d.q.ExecContext(ctx, it.SQL, it.Args...)
			if err != nil {
				return d.translate(err)
			}
			ids, err := idsFromLastInsert(d.d.Name(), res, it.IDCount)
			if err != nil {
				return err
			}
			it.OnIDs(ids)
			continue
		}
		res, err := d.q.ExecContext(ctx, it.SQL, it.Args...)
		if err != nil {
			return d.translate(err)
		}
		if it.Check != nil {
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("sqld: RowsAffected: %w", err)
			}
			if err := it.Check(n); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d db) Begin(ctx context.Context) (sorm.Tx, error) {
	if d.beginner == nil {
		return nil, errors.New("sqld: nested transactions are not supported")
	}
	stx, err := d.beginner.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqld: begin: %w", err)
	}
	return tx{db: db{q: stx, d: d.d}, t: stx}, nil
}

func (d db) queryIDs(ctx context.Context, it sorm.BatchItem) ([]int64, error) {
	rows, err := d.q.QueryContext(ctx, it.SQL, it.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, it.IDCount)
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
	if len(ids) != it.IDCount {
		return nil, fmt.Errorf("sqld: RETURNING returned %d ids, expected %d", len(ids), it.IDCount)
	}
	return ids, nil
}

// idsFromLastInsert reconstructs the row ids of a multi-row INSERT from
// LastInsertId: MySQL returns the FIRST id of the batch (ids ascend when
// auto_increment_increment=1), SQLite returns the LAST rowid.
func idsFromLastInsert(dialect string, res sql.Result, n int) ([]int64, error) {
	last, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("sqld: LastInsertId: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected != int64(n) {
		return nil, fmt.Errorf("sqld: multi-insert affected %d rows, expected %d", affected, n)
	}
	ids := make([]int64, n)
	first := last
	if dialect == "sqlite" {
		first = last - int64(n) + 1
	}
	for i := range ids {
		ids[i] = first + int64(i)
	}
	return ids, nil
}

// translate converts MySQL/SQLite constraint errors into a typed
// *sorm.ConstraintError. No driver imports — matches by code/text:
// MySQL: 1062 (duplicate), 1452/1451 (FK), 1048 (null), 3819 (check);
// SQLite: "UNIQUE constraint failed" etc.
func (d db) translate(err error) error {
	if err == nil {
		return err
	}
	msg := err.Error()
	var kind sorm.ConstraintKind
	switch d.d.Name() {
	case "mysql":
		switch {
		case strings.Contains(msg, "Error 1062"):
			kind = sorm.ConstraintUnique
		case strings.Contains(msg, "Error 1452"), strings.Contains(msg, "Error 1451"):
			kind = sorm.ConstraintForeignKey
		case strings.Contains(msg, "Error 1048"):
			kind = sorm.ConstraintNotNull
		case strings.Contains(msg, "Error 3819"):
			kind = sorm.ConstraintCheck
		default:
			return err
		}
	case "sqlite":
		switch {
		case strings.Contains(msg, "UNIQUE constraint failed"):
			kind = sorm.ConstraintUnique
		case strings.Contains(msg, "FOREIGN KEY constraint failed"):
			kind = sorm.ConstraintForeignKey
		case strings.Contains(msg, "NOT NULL constraint failed"):
			kind = sorm.ConstraintNotNull
		case strings.Contains(msg, "CHECK constraint failed"):
			kind = sorm.ConstraintCheck
		default:
			return err
		}
	default:
		return err
	}
	return &sorm.ConstraintError{Kind: kind, Err: err}
}

// RetryableError reports transient errors after which the transaction is
// worth retrying. No specific driver imports: MySQL 1213 (deadlock) and
// 1205 (lock wait timeout) are recognized by text, SQLite by
// "database is locked" (SQLITE_BUSY).
func (d db) RetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch d.d.Name() {
	case "mysql":
		return strings.Contains(msg, "Error 1213") || strings.Contains(msg, "Error 1205")
	case "sqlite":
		return strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY")
	}
	return false
}

type tx struct {
	db
	t *sql.Tx
}

func (t tx) Commit(context.Context) error   { return t.t.Commit() }
func (t tx) Rollback(context.Context) error { return t.t.Rollback() }

type sqlRows struct {
	r    *sql.Rows
	cols []string
}

func (r *sqlRows) Next() bool             { return r.r.Next() }
func (r *sqlRows) Scan(dest ...any) error { return r.r.Scan(dest...) }
func (r *sqlRows) Err() error             { return r.r.Err() }
func (r *sqlRows) Close()                 { _ = r.r.Close() }
func (r *sqlRows) Columns() []string      { return r.cols }
