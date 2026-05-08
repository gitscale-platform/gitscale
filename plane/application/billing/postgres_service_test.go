//go:build integration

package billing_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/application/billing"
	pgstore "github.com/gitscale-platform/gitscale/plane/data/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// setupPostgres mirrors plane/application/identity/integration_test.go with
// the additional 007 migration applied so billing.partition_archives exists.
func setupPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	ctr, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("gitscale_test"),
		pgmodule.WithUsername("gs"),
		pgmodule.WithPassword("gs"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	migrationsDir := filepath.Join("..", "..", "..", "plane", "data", "migrations")
	for _, f := range []string{
		"000_init.sql", "001_identity.sql", "002_repositories.sql",
		"003_collaboration.sql", "004_ci.sql", "005_billing.sql",
		"006_identity_revocation.sql",
		"007_billing_partition_archives.sql",
	} {
		sql, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read migration %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply migration %s: %v", f, err)
		}
	}
	return pool
}

func TestPostgresService_RecordPartitionArchived_FirstAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := billing.NewPostgresService(ms)

	in := billing.RecordPartitionArchivedInput{
		Year:          2026,
		Month:         5,
		PartitionName: "usage_events_2026_05",
		LakeURI:       "s3://lake/billing/usage_events/2026/05/",
		RowCount:      12,
		BytesWritten:  4096,
	}

	first, err := svc.RecordPartitionArchived(ctx, in)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !first.Created {
		t.Fatalf("expected Created=true on first call")
	}

	// Source row exists.
	var sourceCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.partition_archives WHERE partition_name = $1`,
		in.PartitionName,
	).Scan(&sourceCount); err != nil {
		t.Fatalf("count source: %v", err)
	}
	if sourceCount != 1 {
		t.Fatalf("expected 1 source row, got %d", sourceCount)
	}

	// Outbox row exists with the right event_type, payload shape, and aggregate id.
	var (
		outboxCount  int
		rawPayload   []byte
		aggregateID  string
	)
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.billing_outbox WHERE event_type = $1`,
		billing.EventTypePartitionArchived,
	).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("expected 1 outbox row, got %d", outboxCount)
	}
	if err := pool.QueryRow(ctx,
		`SELECT payload::text, aggregate_id::text FROM billing.billing_outbox WHERE event_type = $1`,
		billing.EventTypePartitionArchived,
	).Scan(&rawPayload, &aggregateID); err != nil {
		t.Fatalf("scan outbox row: %v", err)
	}
	if aggregateID != first.ArchiveID {
		t.Fatalf("aggregate_id %q != ArchiveID %q", aggregateID, first.ArchiveID)
	}
	var payload billing.PartitionArchivedPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.PartitionName != in.PartitionName ||
		payload.RowCount != in.RowCount ||
		payload.BytesWritten != in.BytesWritten ||
		payload.LakeURI != in.LakeURI ||
		payload.ArchiveID.String() != first.ArchiveID ||
		payload.EnvelopeVersion != 1 {
		t.Fatalf("payload mismatch: %+v", payload)
	}

	// Retry — idempotent, no second outbox row.
	second, err := svc.RecordPartitionArchived(ctx, in)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second.Created {
		t.Fatalf("expected Created=false on retry")
	}
	if second.ArchiveID != first.ArchiveID {
		t.Fatalf("ArchiveID changed across retry")
	}

	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.billing_outbox WHERE event_type = $1`,
		billing.EventTypePartitionArchived,
	).Scan(&outboxCount); err != nil {
		t.Fatalf("recount outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("expected outbox count to stay 1 after retry, got %d", outboxCount)
	}
}

// TestPostgresService_RecordPartitionArchived_ConcurrentDuplicates exercises the
// retry loop under serialization conflict. Two goroutines race on the same
// natural key; exactly one source row and exactly one outbox row must persist.
func TestPostgresService_RecordPartitionArchived_ConcurrentDuplicates(t *testing.T) {
	ctx := context.Background()
	pool := setupPostgres(t)
	ms := pgstore.New(pool)
	svc := billing.NewPostgresService(ms)

	in := billing.RecordPartitionArchivedInput{
		Year:          2026,
		Month:         6,
		PartitionName: "usage_events_2026_06",
		LakeURI:       "s3://lake/billing/usage_events/2026/06/",
		RowCount:      7,
		BytesWritten:  2048,
	}

	const concurrency = 5
	var wg sync.WaitGroup
	results := make([]billing.RecordPartitionArchivedOutput, concurrency)
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = svc.RecordPartitionArchived(ctx, in)
		}()
	}
	wg.Wait()

	createdCount := 0
	var canonicalID string
	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: unexpected error %v", i, e)
			continue
		}
		if results[i].Created {
			createdCount++
		}
		if canonicalID == "" {
			canonicalID = results[i].ArchiveID
		} else if results[i].ArchiveID != canonicalID {
			t.Errorf("goroutine %d: ArchiveID %s differs from %s", i, results[i].ArchiveID, canonicalID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("expected exactly one Created=true result, got %d", createdCount)
	}

	var sourceCount, outboxCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.partition_archives WHERE partition_name = $1`,
		in.PartitionName,
	).Scan(&sourceCount); err != nil {
		t.Fatalf("count source: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.billing_outbox WHERE event_type = $1`,
		billing.EventTypePartitionArchived,
	).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if sourceCount != 1 || outboxCount != 1 {
		t.Fatalf("expected exactly 1 source and 1 outbox row, got %d / %d", sourceCount, outboxCount)
	}
}
