package sorm

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// retryClassifier — an optional capability of a driver adapter to
// recognize transient errors (serialization failure, deadlock,
// lock timeout) after which retrying the transaction makes sense.
type retryClassifier interface {
	RetryableError(err error) bool
}

// RunInTx runs fn in a transaction: commit on nil, rollback on error.
// If the adapter classifies the error as transient (deadlock, serialization
// failure), the transaction is retried with exponential backoff —
// up to 3 retries. fn may be called multiple times: side effects
// outside the DB must be idempotent.
//
// Sessions work naturally inside: NewSession(tx).SaveChanges flushes
// within this transaction without opening a nested one.
func RunInTx(ctx context.Context, db DB, fn func(tx Tx) error) error {
	const maxAttempts = 4 // 1 attempt + 3 retries
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
		// jitter so that competitors don't retry in lockstep
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
