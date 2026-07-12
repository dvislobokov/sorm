// Package sqld — адаптер database/sql (MySQL, SQLite). Батчи исполняются
// последовательно в текущем соединении/транзакции; auto-PK — через
// RETURNING (если диалект умеет) или LastInsertId.
package sqld

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"sorm"
	"sorm/dialect"
)

// Wrap оборачивает *sql.DB в sorm.DB с указанным диалектом
// (dialect/my для MySQL, dialect/lite для SQLite).
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
	beginner *sql.DB // nil внутри транзакции
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
		if it.WantID {
			if d.d.ReturningSupported() {
				var id int64
				if err := d.q.QueryRowContext(ctx, it.SQL, it.Args...).Scan(&id); err != nil {
					return d.translate(err)
				}
				it.OnID(id)
				continue
			}
			res, err := d.q.ExecContext(ctx, it.SQL, it.Args...)
			if err != nil {
				return d.translate(err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("sqld: LastInsertId: %w", err)
			}
			it.OnID(id)
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

// translate конвертирует ошибки констрейнтов MySQL/SQLite в типизированный
// *sorm.ConstraintError. Без импорта драйверов — по кодам/тексту:
// MySQL: 1062 (duplicate), 1452/1451 (FK), 1048 (null), 3819 (check);
// SQLite: "UNIQUE constraint failed" и т.п.
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

// RetryableError — transient-ошибки, после которых транзакцию имеет смысл
// повторить. Без импорта конкретных драйверов: MySQL 1213 (deadlock) и
// 1205 (lock wait timeout) распознаются по тексту, SQLite — по
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
