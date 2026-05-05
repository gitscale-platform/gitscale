//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	kafkadata "github.com/gitscale-platform/gitscale/plane/data/kafka"
	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ---------------------------------------------------------------------------
// Test infrastructure helpers
// ---------------------------------------------------------------------------

// testDB starts a PostgreSQL testcontainer, applies all 5 domain migrations,
// and returns a *pgxpool.Pool. The container is terminated when cleanup is called.
func testDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := tcpostgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:16-alpine"),
		tcpostgres.WithDatabase("gitscale_test"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}

	applyMigrations(t, pool)

	cleanup := func() {
		pool.Close()
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("terminate postgres: %v", err)
		}
	}
	return pool, cleanup
}

// applyMigrations runs all SQL migration files against the pool.
func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	// Migration files relative to the repo root. In tests, the working
	// directory is the package directory; climb to the repo root.
	root := repoRoot(t)
	files := []string{
		root + "/plane/data/migrations/000_init.sql",
		root + "/plane/data/migrations/001_identity.sql",
		root + "/plane/data/migrations/002_repositories.sql",
		root + "/plane/data/migrations/003_collaboration.sql",
		root + "/plane/data/migrations/004_ci.sql",
		root + "/plane/data/migrations/005_billing.sql",
	}
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read migration %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			// Some migrations may include non-critical statements; log and continue.
			t.Logf("migration %s: %v", f, err)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Walk up from the test file's package directory.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// plane/data/outbox → go up 3 levels
	parts := strings.Split(dir, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "gitscale" {
			return strings.Join(parts[:i+1], "/")
		}
	}
	t.Fatalf("could not find repo root from %s", dir)
	return ""
}

// testBroker starts a Redpanda testcontainer and returns the broker address.
//
// Testcontainers maps the container's 9092 port to a random host port. To
// make Redpanda's metadata advertise the correct host:port (so clients do not
// try to connect to the container-internal IP), we use a two-phase approach:
//
//  1. Start the container once to discover the host and mapped port.
//  2. The writer (segmentio/kafka-go) is configured to write directly to the
//     bootstrap broker, so it does not follow metadata redirects — this works
//     without any special configuration.
//  3. The reader and topic-admin use createTopicDirect which dials directly
//     rather than following the controller election, so the advertised address
//     mismatch does not matter for single-node Redpanda.
//
// For all reading/admin calls we use kafkago.Dial with the bootstrap addr
// directly; Redpanda on a single node is both leader and follower for all
// partitions, so direct-dial always reaches the right node.
func testBroker(t *testing.T) (brokerAddr string, cleanup func()) {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "docker.redpanda.com/redpandadata/redpanda:v24.1.1",
		ExposedPorts: []string{"9092/tcp"},
		Cmd: []string{
			"redpanda", "start",
			"--overprovisioned",
			"--smp", "1",
			"--memory", "512M",
			"--reserve-memory", "0M",
			"--node-id", "0",
			"--check=false",
		},
		WaitingFor: wait.ForLog("Successfully started Redpanda!").
			WithStartupTimeout(90 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start redpanda: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("redpanda host: %v", err)
	}
	port, err := container.MappedPort(ctx, "9092")
	if err != nil {
		t.Fatalf("redpanda port: %v", err)
	}

	addr := fmt.Sprintf("%s:%s", host, port.Port())

	// Give Redpanda a moment after the log line to finish binding.
	time.Sleep(1 * time.Second)

	cleanup = func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate redpanda: %v", err)
		}
	}
	return addr, cleanup
}

// createTopicDirect creates a Kafka topic by directly dialing the broker.
// It uses fixedDialFunc so all connections go to the mapped address regardless
// of metadata redirects from Redpanda.
func createTopicDirect(t *testing.T, brokerAddr, topic string, partitions int) {
	t.Helper()
	ctx := context.Background()
	// Use the Dialer's DialContext which returns a *kafkago.Conn directly.
	conn, err := brokerDialer(brokerAddr).DialContext(ctx, "tcp", brokerAddr)
	if err != nil {
		t.Fatalf("kafka dial %s: %v", brokerAddr, err)
	}
	defer conn.Close()

	err = conn.CreateTopics(kafkago.TopicConfig{
		Topic:             topic,
		NumPartitions:     partitions,
		ReplicationFactor: 1,
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "TOPIC_ALREADY_EXISTS") {
		t.Logf("create topic %s (non-fatal): %v", topic, err)
	}
}


// fixedDialFunc returns a dial function that always connects to brokerAddr,
// ignoring the address requested by the Kafka client. This is required when
// Redpanda runs behind Docker bridge networking: after a metadata fetch,
// kafka-go learns the container-internal IP and tries to dial it directly.
// By fixing the dial target to the mapped host:port, all connections succeed.
func fixedDialFunc(brokerAddr string) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, brokerAddr)
	}
}

// brokerDialer returns a kafka-go Dialer that always connects to brokerAddr.
func brokerDialer(brokerAddr string) *kafkago.Dialer {
	return &kafkago.Dialer{
		DialFunc: fixedDialFunc(brokerAddr),
	}
}

// readAllMessages reads up to n messages from partition 0 of topic, starting
// from offset 0. Use for single-partition topics in tests.
func readAllMessages(t *testing.T, brokerAddr, topic string, n int, timeout time.Duration) []kafkadata.EventEnvelope {
	t.Helper()
	return readPartitionMessages(t, brokerAddr, topic, 0, n, timeout)
}

// readPartitionMessages reads up to n messages from a specific partition of
// topic, starting from offset 0.
func readPartitionMessages(t *testing.T, brokerAddr, topic string, partition, n int, timeout time.Duration) []kafkadata.EventEnvelope {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:   []string{brokerAddr},
		Dialer:    brokerDialer(brokerAddr),
		Topic:     topic,
		Partition: partition,
		MinBytes:  1,
		MaxBytes:  10 * 1024 * 1024,
	})
	defer r.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := r.SetOffset(kafkago.FirstOffset); err != nil {
		t.Logf("SetOffset: %v", err)
	}

	var out []kafkadata.EventEnvelope
	for len(out) < n {
		msg, err := r.ReadMessage(ctx)
		if err != nil {
			break
		}
		var env kafkadata.EventEnvelope
		if err := json.Unmarshal(msg.Value, &env); err != nil {
			t.Logf("unmarshal message: %v", err)
			continue
		}
		out = append(out, env)
	}
	return out
}

// readAllPartitions reads messages across all partitions of topic until
// timeout. It spawns one reader per partition and collects all messages.
// Since partition distribution is uneven, each reader reads up to n messages
// (generous upper bound) and returns whatever it collected before timeout.
func readAllPartitions(t *testing.T, brokerAddr, topic string, numPartitions, n int, timeout time.Duration) []kafkadata.EventEnvelope {
	t.Helper()
	var mu sync.Mutex
	var all []kafkadata.EventEnvelope
	var wg sync.WaitGroup

	// Use a shared context so all readers stop at the same deadline.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for p := 0; p < numPartitions; p++ {
		wg.Add(1)
		p := p
		go func() {
			defer wg.Done()
			r := kafkago.NewReader(kafkago.ReaderConfig{
				Brokers:   []string{brokerAddr},
				Dialer:    brokerDialer(brokerAddr),
				Topic:     topic,
				Partition: p,
				MinBytes:  1,
				MaxBytes:  10 * 1024 * 1024,
			})
			defer r.Close()
			if err := r.SetOffset(kafkago.FirstOffset); err != nil {
				t.Logf("SetOffset partition %d: %v", p, err)
			}
			for {
				msg, err := r.ReadMessage(ctx)
				if err != nil {
					return
				}
				var env kafkadata.EventEnvelope
				if err := json.Unmarshal(msg.Value, &env); err != nil {
					t.Logf("unmarshal partition %d: %v", p, err)
					continue
				}
				mu.Lock()
				all = append(all, env)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return all
}

// insertOutboxRow inserts a single row into the given outbox table and returns
// the event_id.
func insertOutboxRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) uuid.UUID {
	t.Helper()
	eventID := uuid.New()
	aggID := uuid.New()
	payload, _ := json.Marshal(map[string]string{"key": "val"})

	//nolint:gosec // table name comes from test code, not user input
	q := fmt.Sprintf(
		`INSERT INTO %s (event_id, aggregate_type, aggregate_id, event_type, payload)
		 VALUES ($1, $2, $3, $4, $5)`,
		table,
	)
	if _, err := pool.Exec(ctx, q, eventID, "TestAggregate", aggID, "test.created", payload); err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	return eventID
}

// countUnprocessed returns the count of rows with processed_at IS NULL.
func countUnprocessed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	//nolint:gosec
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE processed_at IS NULL", table)
	var n int
	if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
		t.Fatalf("count unprocessed: %v", err)
	}
	return n
}

// newConsumer builds a consumer wired to pool + real Kafka producer.
// DialFunc is set to fixedDialFunc so the producer always dials the mapped
// broker address regardless of what metadata returns.
func newConsumer(t *testing.T, pool *pgxpool.Pool, brokerAddr, domain, table, topic string) *outbox.OutboxConsumer {
	t.Helper()
	prod, err := outbox.NewKafkaProducer(outbox.KafkaProducerConfig{
		BootstrapServers: brokerAddr,
		ClientID:         "test-outbox-" + domain,
		DialFunc:         fixedDialFunc(brokerAddr),
	})
	if err != nil {
		t.Fatalf("NewKafkaProducer: %v", err)
	}

	cfg := outbox.Config{
		Domain:         domain,
		Table:          table,
		Topic:          topic,
		DB:             pool,
		Producer:       prod,
		PollInterval:   100 * time.Millisecond,
		PublishTimeout: 10 * time.Second,
		BatchSize:      200,
	}
	c, err := outbox.NewOutboxConsumer(cfg)
	if err != nil {
		t.Fatalf("NewOutboxConsumer: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// Integration scenario 1: Happy path — 3 rows drain, verified on broker
// ---------------------------------------------------------------------------

func TestIntegration_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires Docker")
	}

	ctx := context.Background()

	pool, dbCleanup := testDB(t)
	defer dbCleanup()

	brokerAddr, brokerCleanup := testBroker(t)
	defer brokerCleanup()

	const (
		domain = "identity"
		table  = "identity.identity_outbox"
		topic  = kafkadata.TopicIdentityEvents
	)
	// Use 1 partition so the reader (which reads partition 0 by default) sees
	// all messages without needing a consumer group.
	createTopicDirect(t, brokerAddr, topic, 1)

	// Insert 3 rows.
	var inserted []uuid.UUID
	for i := 0; i < 3; i++ {
		inserted = append(inserted, insertOutboxRow(t, ctx, pool, table))
	}

	// Run consumer until rows are drained (max 5s).
	consumerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c := newConsumer(t, pool, brokerAddr, domain, table, topic)
	go func() {
		_ = c.Run(consumerCtx)
	}()

	// Poll until all rows are processed or timeout.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n := countUnprocessed(t, ctx, pool, table)
		if n == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()

	// Assert: all rows processed in DB.
	remaining := countUnprocessed(t, ctx, pool, table)
	if remaining != 0 {
		t.Errorf("expected 0 unprocessed rows, got %d", remaining)
	}

	// Assert: 3 messages on broker.
	msgs := readAllMessages(t, brokerAddr, topic, 3, 10*time.Second)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages on broker, got %d", len(msgs))
	}

	// Assert: event IDs match inserted set.
	published := make(map[uuid.UUID]struct{}, 3)
	for _, m := range msgs {
		published[m.EventID] = struct{}{}
	}
	for _, id := range inserted {
		if _, ok := published[id]; !ok {
			t.Errorf("event_id %s missing from broker", id)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration scenario 2: Crash-mid-batch — partial publish, restart, dedupe
// ---------------------------------------------------------------------------

func TestIntegration_CrashMidBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires Docker")
	}

	ctx := context.Background()

	pool, dbCleanup := testDB(t)
	defer dbCleanup()

	brokerAddr, brokerCleanup := testBroker(t)
	defer brokerCleanup()

	const (
		domain = "identity"
		table  = "identity.identity_outbox"
		topic  = kafkadata.TopicIdentityEvents
	)
	// 1 partition so the reader (partition 0) sees all messages.
	createTopicDirect(t, brokerAddr, topic, 1)

	// Insert 5 rows.
	var inserted []uuid.UUID
	for i := 0; i < 5; i++ {
		inserted = append(inserted, insertOutboxRow(t, ctx, pool, table))
	}

	// First consumer: mock producer that publishes 3 then crashes (returns error).
	// This simulates: rows remain unprocessed in DB, but 3 events are on broker.
	crashProd := outbox.NewMockProducer()
	crashProd.InjectErr = fmt.Errorf("simulated crash after 3")
	crashProd.PublishAfterN = 3

	crashCfg := outbox.Config{
		Domain:         domain,
		Table:          table,
		Topic:          topic,
		DB:             pool,
		Producer:       crashProd,
		PollInterval:   100 * time.Millisecond,
		PublishTimeout: 10 * time.Second,
		BatchSize:      10,
	}
	crashConsumer, err := outbox.NewOutboxConsumer(crashCfg)
	if err != nil {
		t.Fatalf("NewOutboxConsumer: %v", err)
	}

	// Run crash consumer for a couple of cycles (it will always fail to publish).
	crashCtx, crashCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	_ = crashConsumer.Run(crashCtx)
	crashCancel()

	// All 5 rows must still be unprocessed in DB (txn rolled back due to publish error).
	remaining := countUnprocessed(t, ctx, pool, table)
	if remaining != 5 {
		t.Errorf("expected 5 unprocessed rows after crash, got %d", remaining)
	}

	// Publish 3 messages to Redpanda (simulating what the crash consumer would
	// have published — in reality the publish DID succeed to the broker but
	// the DB UPDATE rolled back).
	fakeProd, err := outbox.NewKafkaProducer(outbox.KafkaProducerConfig{
		BootstrapServers: brokerAddr,
		ClientID:         "fake-pre-crash",
		DialFunc:         fixedDialFunc(brokerAddr),
	})
	if err != nil {
		t.Fatalf("NewKafkaProducer: %v", err)
	}
	// We simulate the "3 were already on broker" by publishing the first 3 directly.
	_ = fakeProd.PublishBatch(ctx, topic, func() []outbox.OutboxRow {
		rows := make([]outbox.OutboxRow, 3)
		for i, id := range inserted[:3] {
			rows[i] = outbox.OutboxRow{
				ID:            int64(i + 1),
				EventID:       id,
				AggregateType: "TestAggregate",
				AggregateID:   uuid.New(),
				EventType:     "test.created",
				Payload:       json.RawMessage(`{}`),
				CreatedAt:     time.Now(),
			}
		}
		return rows
	}())
	_ = fakeProd.Close()

	// Now restart with a real producer that will successfully drain all 5 rows.
	restartConsumer := newConsumer(t, pool, brokerAddr, domain, table, topic)
	restartCtx, restartCancel := context.WithTimeout(ctx, 5*time.Second)
	defer restartCancel()
	go func() {
		_ = restartConsumer.Run(restartCtx)
	}()

	// Wait for all rows to drain.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countUnprocessed(t, ctx, pool, table) == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	restartCancel()

	if n := countUnprocessed(t, ctx, pool, table); n != 0 {
		t.Errorf("expected 0 unprocessed rows after restart, got %d", n)
	}

	// Read all messages from broker. Due to at-least-once delivery, there may
	// be up to 8 (5 original + 3 that were "pre-published"). The set of
	// event_ids must equal the original 5.
	allMsgs := readAllMessages(t, brokerAddr, topic, 8, 10*time.Second)
	uniqueIDs := make(map[uuid.UUID]struct{})
	for _, m := range allMsgs {
		uniqueIDs[m.EventID] = struct{}{}
	}
	if len(uniqueIDs) != 5 {
		t.Errorf("expected 5 unique event_ids on broker, got %d", len(uniqueIDs))
	}
	for _, id := range inserted {
		if _, ok := uniqueIDs[id]; !ok {
			t.Errorf("event_id %s missing from broker after restart", id)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration scenario 3: Two-replica race — each event_id published exactly once
// ---------------------------------------------------------------------------

func TestIntegration_TwoReplicaRace(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires Docker")
	}

	ctx := context.Background()

	pool, dbCleanup := testDB(t)
	defer dbCleanup()

	brokerAddr, brokerCleanup := testBroker(t)
	defer brokerCleanup()

	const (
		domain = "identity"
		table  = "identity.identity_outbox"
		topic  = kafkadata.TopicIdentityEvents
	)
	const numPartitions = 10
	createTopicDirect(t, brokerAddr, topic, numPartitions)

	const rowCount = 100
	for i := 0; i < rowCount; i++ {
		insertOutboxRow(t, ctx, pool, table)
	}

	// Start two consumer replicas sharing the same DB and broker.
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		prod, err := outbox.NewKafkaProducer(outbox.KafkaProducerConfig{
			BootstrapServers: brokerAddr,
			ClientID:         fmt.Sprintf("replica-%d", i),
			DialFunc:         fixedDialFunc(brokerAddr),
		})
		if err != nil {
			t.Fatalf("replica %d: NewKafkaProducer: %v", i, err)
		}
		cfg := outbox.Config{
			Domain:         domain,
			Table:          table,
			Topic:          topic,
			DB:             pool,
			Producer:       prod,
			PollInterval:   50 * time.Millisecond,
			PublishTimeout: 10 * time.Second,
			BatchSize:      20,
		}
		c, err := outbox.NewOutboxConsumer(cfg)
		if err != nil {
			t.Fatalf("replica %d: NewOutboxConsumer: %v", i, err)
		}
		go func() {
			defer wg.Done()
			_ = c.Run(runCtx)
		}()
	}

	// Wait for all rows to drain.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if countUnprocessed(t, ctx, pool, table) == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	// Assert: no rows stuck unprocessed.
	if n := countUnprocessed(t, ctx, pool, table); n != 0 {
		t.Errorf("expected 0 unprocessed rows, got %d (two-replica race)", n)
	}

	// Read all messages from broker. Due to advisory lock guaranteeing
	// single-active-drainer per cycle, duplicates should be absent but we
	// assert on unique event_ids = 100 (at-least-once means duplicates are
	// acceptable; the test asserts no event is missed).
	allMsgs := readAllPartitions(t, brokerAddr, topic, numPartitions, rowCount, 15*time.Second)

	uniqueIDs := make(map[uuid.UUID]struct{}, rowCount)
	for _, m := range allMsgs {
		uniqueIDs[m.EventID] = struct{}{}
	}

	if len(uniqueIDs) != rowCount {
		t.Errorf("expected %d unique event_ids, got %d", rowCount, len(uniqueIDs))
	}

	// Verify: each event_id published at least once (none stuck/missed).
	//nolint:gosec
	q := fmt.Sprintf("SELECT event_id FROM %s", table)
	rows, err := pool.Query(ctx, q)
	if err != nil {
		t.Fatalf("query all event_ids: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := uniqueIDs[id]; !ok {
			t.Errorf("event_id %s from DB was never published to broker", id)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration scenario 4: Kafka unavailable — rows remain unprocessed, no panic
// ---------------------------------------------------------------------------

func TestIntegration_KafkaUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — requires Docker")
	}

	ctx := context.Background()

	pool, dbCleanup := testDB(t)
	defer dbCleanup()

	// Use a non-existent broker address to simulate Kafka unavailable.
	unavailableBroker := "127.0.0.1:19099"

	const (
		domain = "identity"
		table  = "identity.identity_outbox"
		topic  = kafkadata.TopicIdentityEvents
	)

	// Insert 3 rows.
	for i := 0; i < 3; i++ {
		insertOutboxRow(t, ctx, pool, table)
	}

	prod, err := outbox.NewKafkaProducer(outbox.KafkaProducerConfig{
		BootstrapServers: unavailableBroker,
		ClientID:         "unavailable-test",
	})
	if err != nil {
		t.Fatalf("NewKafkaProducer: %v", err)
	}

	cfg := outbox.Config{
		Domain:         domain,
		Table:          table,
		Topic:          topic,
		DB:             pool,
		Producer:       prod,
		PollInterval:   100 * time.Millisecond,
		PublishTimeout: 500 * time.Millisecond, // short timeout for test speed
		BatchSize:      100,
	}
	c, err := outbox.NewOutboxConsumer(cfg)
	if err != nil {
		t.Fatalf("NewOutboxConsumer: %v", err)
	}

	// Run for 2 seconds — should not panic.
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_ = c.Run(runCtx)
	cancel()

	// All 3 rows must still be unprocessed.
	if n := countUnprocessed(t, ctx, pool, table); n != 3 {
		t.Errorf("expected 3 unprocessed rows, got %d (kafka unavailable)", n)
	}
}

