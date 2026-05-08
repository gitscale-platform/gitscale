//go:build integration

package billing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	billingstore "github.com/gitscale-platform/gitscale/plane/data/store/billing"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestRestorePartition_archiveRestoreRoundTrip seeds rows into the live
// partition, runs ExportActivity to produce an encrypted parquet on a stub
// object store, then runs the four restore activities in order and asserts
// the quarantine table holds the exact same (id, ts) row set.
//
// Vault is real (testcontainer) — DEK derivation must round-trip end-to-end,
// otherwise drift between encode and decode is silent. The object store is
// the in-process stub so the test does not require minio.
func TestRestorePartition_archiveRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()

	pool := bootRestorePostgres(t)
	vaultClient := bootVaultForExport(t)
	keys := NewVaultKeyProvider(vaultClient, "", "")

	year, month := 2026, 5

	// Create the live partition + seed rows.
	partitioner := billingstore.NewPostgresPartitioner(pool)
	if _, err := partitioner.CreateUsageEventsPartition(ctx, year, month); err != nil {
		t.Fatalf("create partition: %v", err)
	}
	seeded := seedUsageEvents(t, pool, year, month, 50)

	// Export: detach + stream to stub object store.
	archiver := billingstore.NewPostgresArchiver(pool)
	if err := archiver.DetachUsageEventsPartition(ctx, year, month); err != nil {
		t.Fatalf("detach: %v", err)
	}
	store := newStubObjectStore("test-bucket")
	exportAct, err := NewExportActivity(archiver, store, keys, "test-bucket")
	if err != nil {
		t.Fatal(err)
	}
	exportRes, err := exportAct.Execute(ctx, ExportInput{Year: year, Month: month})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exportRes.RowCount != int64(len(seeded)) {
		t.Fatalf("export RowCount=%d want %d", exportRes.RowCount, len(seeded))
	}

	// Restore: run each activity in workflow order against the same store.
	fetchAct, _ := NewFetchManifestActivity(store)
	manifest, err := fetchAct.Execute(ctx, FetchManifestInput{Year: year, Month: month})
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	if manifest.EncFormat != encFormatV1 {
		t.Fatalf("manifest enc_format=%q", manifest.EncFormat)
	}

	verifyAct, _ := NewVerifyChecksumActivity(store)
	if err := verifyAct.Execute(ctx, VerifyChecksumInput{
		ParquetKey: manifest.ParquetKey, ChecksumKey: manifest.ChecksumKey,
	}); err != nil {
		t.Fatalf("verify checksum: %v", err)
	}

	scratch := t.TempDir()
	decAct, _ := NewDownloadAndDecryptActivity(store, keys, scratch)
	decRes, err := decAct.Execute(ctx, DownloadDecryptInput{
		Year:            year,
		Month:           month,
		ParquetKey:      manifest.ParquetKey,
		SourcePartition: manifest.SourcePartition,
		EncFormat:       manifest.EncFormat,
		KEKHint:         manifest.KEKHint,
	})
	if err != nil {
		t.Fatalf("download+decrypt: %v", err)
	}

	restorer := billingstore.NewPostgresRestorer(pool)
	loadAct, _ := NewLoadIntoQuarantineActivity(restorer)
	loadRes, err := loadAct.Execute(ctx, LoadQuarantineInput{
		Year: year, Month: month, PlaintextPath: decRes.PlaintextPath,
	})
	if err != nil {
		t.Fatalf("load quarantine: %v", err)
	}
	if loadRes.RowsImported != int64(len(seeded)) {
		t.Fatalf("RowsImported=%d want %d", loadRes.RowsImported, len(seeded))
	}

	// Assert quarantine table holds the same (id, ts) row set.
	got := readQuarantine(t, pool, year, month)
	sort.Slice(got, func(i, j int) bool { return got[i].id < got[j].id })
	want := append([]idTs(nil), seeded...)
	sort.Slice(want, func(i, j int) bool { return want[i].id < want[j].id })
	if len(got) != len(want) {
		t.Fatalf("row count got=%d want=%d", len(got), len(want))
	}
	for i := range got {
		if got[i].id != want[i].id || !got[i].ts.Equal(want[i].ts) {
			t.Errorf("row %d: got=(%s, %s) want=(%s, %s)",
				i, got[i].id, got[i].ts, want[i].id, want[i].ts)
		}
	}

	// Quarantine table must be sealed read-only.
	if !quarantineIsSealed(t, pool, year, month) {
		t.Error("quarantine table is not sealed read-only after restore")
	}
}

type idTs struct {
	id string
	ts time.Time
}

func seedUsageEvents(t *testing.T, pool *pgxpool.Pool, year, month, n int) []idTs {
	t.Helper()
	ctx := context.Background()
	rows := make([]idTs, 0, n)
	base := time.Date(year, time.Month(month), 5, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1)
		ts := base.Add(time.Duration(i) * time.Minute)
		_, err := pool.Exec(ctx, `
			INSERT INTO billing.usage_events
			  (id, account_id, principal_id, principal_type, surface, cost_vector, value, event_source, ts, created_at)
			VALUES
			  ($1::uuid, $2::uuid, $3::uuid, 'agent', 'tokens', '{}'::jsonb, 1000, 'api', $4, $4)`,
			id,
			"00000000-0000-0000-0000-000000000001",
			"00000000-0000-0000-0000-000000000002",
			ts,
		)
		if err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
		rows = append(rows, idTs{id: id, ts: ts})
	}
	return rows
}

func readQuarantine(t *testing.T, pool *pgxpool.Pool, year, month int) []idTs {
	t.Helper()
	ctx := context.Background()
	q := fmt.Sprintf(
		"SELECT id::text, ts FROM billing.usage_events_restore_%04d_%02d ORDER BY ts, id",
		year, month,
	)
	rows, err := pool.Query(ctx, q)
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	defer rows.Close()
	var out []idTs
	for rows.Next() {
		var r idTs
		if err := rows.Scan(&r.id, &r.ts); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func quarantineIsSealed(t *testing.T, pool *pgxpool.Pool, year, month int) bool {
	t.Helper()
	ctx := context.Background()
	tableName := fmt.Sprintf("usage_events_restore_%04d_%02d", year, month)
	// has_table_privilege returns true if PUBLIC has INSERT on the table.
	var canInsert bool
	if err := pool.QueryRow(ctx,
		`SELECT has_table_privilege('public', $1::regclass, 'INSERT')`,
		"billing."+tableName,
	).Scan(&canInsert); err != nil {
		t.Fatalf("probe privilege: %v", err)
	}
	return !canInsert
}

func bootRestorePostgres(t *testing.T) *pgxpool.Pool {
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
		"006_identity_revocation.sql", "007_billing_partition_archives.sql",
		"008_updated_at_triggers.sql", "009_identity_temporal_columns.sql",
	} {
		sql, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return pool
}
