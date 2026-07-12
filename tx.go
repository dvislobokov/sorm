package sorm

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// retryClassifier — опциональная способность драйверного адаптера
// распознавать transient-ошибки (serialization failure, deadlock,
// lock timeout), после которых транзакцию имеет смысл повторить.
type retryClassifier interface {
	RetryableError(err error) bool
}

// RunInTx выполняет fn в транзакции: commit при nil, rollback при ошибке.
// Если адаптер классифицирует ошибку как transient (deadlock, serialization
// failure), транзакция повторяется с экспоненциальным backoff'ом —
// до 3 повторов. fn может быть вызвана несколько раз: побочные эффекты
// вне БД должны быть идемпотентны.
//
// Сессии внутри работают естественно: NewSession(tx).SaveChanges выполнит
// flush в этой транзакции, не открывая вложенную.
func RunInTx(ctx context.Context, db DB, fn func(tx Tx) error) error {
	const maxAttempts = 4 // 1 попытка + 3 повтора
	backoff := 10 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = runOnce(ctx, db, fn)
		if lastErr == nil {
			return nil
		}
		classifier, ok := db.(retryClassifier)
		if !ok || !classifier.RetryableError(lastErr) {
			return lastErr
		}
		if attempt == maxAttempts {
			break
		}
		// джиттер, чтобы конкуренты не бились в такт
		sleep := backoff + time.Duration(rand.Int63n(int64(backoff)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		backoff *= 2
	}
	return fmt.Errorf("sorm: tx failed after %d attempts: %w", maxAttempts, lastErr)
}

func runOnce(ctx context.Context, db DB, fn func(tx Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("sorm: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("sorm: commit: %w", err)
	}
	return nil
}
