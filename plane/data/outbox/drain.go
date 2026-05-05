package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// drainResult is the outcome of one drain cycle, used for metric labelling.
type drainResult string

const (
	resultOK           drainResult = "ok"
	resultLockMissed   drainResult = "lock_missed"
	resultEmpty        drainResult = "empty"
	resultPublishError drainResult = "publish_error"
	resultUpdateError  drainResult = "update_error"
)

// drainBatch executes one full drain cycle inside a single transaction:
//
//  1. Sample oldest-unprocessed gauge (always, regardless of lock outcome).
//  2. pg_try_advisory_xact_lock — if false, return resultLockMissed.
//  3. SELECT … FOR UPDATE SKIP LOCKED LIMIT batchSize.
//  4. If empty, return resultEmpty.
//  5. producer.PublishBatch with publishTimeout deadline.
//  6. If publish failed, return resultPublishError (txn rolled back by defer).
//  7. UPDATE processed_at = now() for all row IDs.
//  8. COMMIT.
//
// The advisory lock is released automatically on COMMIT or ROLLBACK because it
// is transaction-scoped (pg_try_advisory_xact_lock, not session-scoped).
// The explicit ::bigint cast avoids the (int,int) overload ambiguity (spec §6).
func drainBatch(ctx context.Context, cfg Config) (drainResult, int, error) {
	tx, err := cfg.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return resultPublishError, 0, fmt.Errorf("drainBatch: begin tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit (pgx contract).
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// Step 1: sample oldest-unprocessed for SLO gauge — always, even if we
	// don't win the lock, so the metric does not drop during leadership rotation.
	sampleOldestUnprocessed(ctx, cfg.DB, cfg)

	// Step 2: advisory lock — transaction-scoped.
	// hashtext($1) produces a stable int4; ::bigint casts to the (bigint)
	// overload of pg_try_advisory_xact_lock, disambiguating from (int,int).
	var locked bool
	row := tx.QueryRow(ctx,
		"SELECT pg_try_advisory_xact_lock(hashtext($1)::bigint)",
		cfg.Table,
	)
	if err := row.Scan(&locked); err != nil {
		return resultPublishError, 0, fmt.Errorf("drainBatch: advisory lock: %w", err)
	}

	if !locked {
		cfg.Metrics.setAdvisoryLockHeld(false)
		_ = tx.Rollback(ctx)
		return resultLockMissed, 0, nil
	}
	cfg.Metrics.setAdvisoryLockHeld(true)

	// Step 3: select unprocessed rows ordered by created_at, id.
	// ORDER BY created_at, id: id is BIGSERIAL so it provides a deterministic
	// tiebreaker for rows with identical created_at microsecond precision.
	// FOR UPDATE SKIP LOCKED: defence-in-depth against concurrent readers
	// even though the advisory lock makes it redundant for the primary
	// exclusivity goal (spec §6).
	//nolint:gosec // table and schema names come from internal config, not user input
	query := fmt.Sprintf(
		`SELECT id, event_id, aggregate_type, aggregate_id, event_type, payload, created_at
		 FROM %s
		 WHERE processed_at IS NULL
		 ORDER BY created_at, id
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`,
		cfg.Table,
	)
	rows, err := tx.Query(ctx, query, cfg.BatchSize)
	if err != nil {
		return resultPublishError, 0, fmt.Errorf("drainBatch: select: %w", err)
	}
	batch, err := scanRows(rows)
	if err != nil {
		return resultPublishError, 0, fmt.Errorf("drainBatch: scan: %w", err)
	}

	// Step 4: empty batch — no rows to publish.
	if len(batch) == 0 {
		cfg.Metrics.setAdvisoryLockHeld(false)
		return resultEmpty, 0, nil
	}

	// Step 5: publish with bounded deadline.
	pubCtx, cancel := context.WithTimeout(ctx, cfg.PublishTimeout)
	defer cancel()

	pubStart := time.Now()
	pubErr := cfg.Producer.PublishBatch(pubCtx, cfg.Topic, batch)
	pubElapsed := time.Since(pubStart).Seconds()

	if pubErr != nil {
		cfg.Metrics.observePublishDuration(pubElapsed, "error")
		slog.WarnContext(ctx, "outbox: publish error",
			"domain", cfg.Domain,
			"batch_size", len(batch),
			"error", pubErr,
		)
		// Step 6: error — defer Rollback fires, rows remain unprocessed.
		return resultPublishError, 0, fmt.Errorf("drainBatch: publish: %w", pubErr)
	}
	cfg.Metrics.observePublishDuration(pubElapsed, "ok")

	// Step 7: mark rows processed in the same transaction.
	ids := make([]int64, len(batch))
	for i, r := range batch {
		ids[i] = r.ID
	}
	//nolint:gosec // table comes from internal config, not user input
	updateQuery := fmt.Sprintf(
		`UPDATE %s SET processed_at = now() WHERE id = ANY($1)`,
		cfg.Table,
	)
	tag, err := tx.Exec(ctx, updateQuery, ids)
	if err != nil {
		cfg.Metrics.observeBatchSize(0)
		return resultUpdateError, 0, fmt.Errorf("drainBatch: update processed_at: %w", err)
	}
	updated := int(tag.RowsAffected())

	// Step 8: commit — makes the UPDATE durable and releases the advisory lock.
	if err := tx.Commit(ctx); err != nil {
		return resultUpdateError, 0, fmt.Errorf("drainBatch: commit: %w", err)
	}

	cfg.Metrics.observeBatchSize(updated)
	cfg.Metrics.addProcessed(updated)
	cfg.Metrics.setAdvisoryLockHeld(false)

	if updated > 0 {
		slog.InfoContext(ctx, "outbox: drained batch",
			"domain", cfg.Domain,
			"count", updated,
		)
	}

	return resultOK, updated, nil
}

// sampleOldestUnprocessed queries the minimum created_at of unprocessed rows
// and sets the SLO gauge. Called at every poll cycle, regardless of whether
// this replica holds the advisory lock (spec §12).
func sampleOldestUnprocessed(ctx context.Context, db *pgxpool.Pool, cfg Config) {
	//nolint:gosec // table comes from internal config
	q := fmt.Sprintf(
		`SELECT EXTRACT(EPOCH FROM (now() - MIN(created_at)))
		 FROM %s WHERE processed_at IS NULL`,
		cfg.Table,
	)
	var ageSeconds *float64
	row := db.QueryRow(ctx, q)
	if err := row.Scan(&ageSeconds); err != nil {
		// Non-fatal; metric simply won't update this cycle.
		return
	}
	if ageSeconds == nil {
		cfg.Metrics.setOldestUnprocessed(0)
		return
	}
	cfg.Metrics.setOldestUnprocessed(*ageSeconds)
}

// scanRows converts pgx.Rows into a slice of OutboxRow.
func scanRows(rows pgx.Rows) ([]OutboxRow, error) {
	defer rows.Close()
	var batch []OutboxRow
	for rows.Next() {
		var r OutboxRow
		if err := rows.Scan(
			&r.ID,
			&r.EventID,
			&r.AggregateType,
			&r.AggregateID,
			&r.EventType,
			&r.Payload,
			&r.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		batch = append(batch, r)
	}
	return batch, rows.Err()
}
