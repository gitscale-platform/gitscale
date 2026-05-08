# Plan: #27 — Identity cache invalidator consumer

**Date:** 2026-05-06
**Issue:** #27
**Spec:** none (issue body + execution-plan §13.2 + #15 spec D6)
**Branch:** `feat/application-identity-cache-invalidator`
**Pre-merge of:** #15-revocation (without it, no producer for the invalidating events; consumer would be a no-op)
**Blocks:** none

## Pre-flight

- Confirm #15-revocation merged AND emitting at least one revocation event in staging.
- `git fetch && git checkout main && git pull`
- `git checkout -b feat/application-identity-cache-invalidator`
- Verify `plane/data/cache/identity.go::IdentityKey` exists and `gitscale.identity.events` topic is configured.

## Step sequence

### Step 1 — Consumer service structure

File: `plane/application/identity-cache-invalidator/main.go`

```go
package main

func main() {
    cfg := loadConfig()
    redisClient := openRedis(cfg.RedisURL, cfg.RedisUseCluster)
    cacheStore := cache.NewRedisStore(redisClient).WithNamespace(cfg.Env)
    consumer := newConsumer(cacheStore, cfg.KafkaBootstrap)
    consumer.Run(signalContext())
}
```

### Step 2 — Consumer impl

File: `plane/application/identity-cache-invalidator/consumer.go`

```go
type consumer struct {
    cache    cache.Store
    kafka    *kgo.Client // franz-go (matches outbox consumer's Kafka client)
    deduper  *redisDedupe
    metrics  *metricRegistry
}

func (c *consumer) processMessage(ctx context.Context, msg *kgo.Record) error {
    var env kafka.EventEnvelope
    if err := json.Unmarshal(msg.Value, &env); err != nil {
        return c.routeToDLQ(msg, "envelope_decode_failed")
    }

    // Idempotency check
    if seen, _ := c.deduper.Seen(ctx, env.EventID); seen {
        c.metrics.invalidations.WithLabelValues(env.EventType, "already_processed").Inc()
        return nil
    }

    handler := c.eventHandlers[env.EventType]
    if handler == nil {
        c.metrics.invalidations.WithLabelValues(env.EventType, "unknown_event_type").Inc()
        return nil // not for us; commit
    }

    affected, err := handler(env)
    if err != nil {
        c.metrics.invalidations.WithLabelValues(env.EventType, "handler_error").Inc()
        return err
    }

    for _, principalID := range affected {
        if err := c.cache.Delete(ctx, fmt.Sprintf(cache.IdentityKey, principalID)); err != nil {
            c.metrics.invalidations.WithLabelValues(env.EventType, "cache_error").Inc()
            return err
        }
    }

    c.metrics.invalidations.WithLabelValues(env.EventType, "ok").Inc()
    return c.deduper.Mark(ctx, env.EventID)
}
```

### Step 3 — Event handlers

File: `plane/application/identity-cache-invalidator/handlers.go`

Registry mapping event types to extractors of `affected_principal_ids[]`:

```go
var eventHandlers = map[string]handler{
    "user.disabled":                payloadAffectedPrincipals,
    "user.deleted":                 payloadAffectedPrincipals,
    "agent.revoked":                payloadAffectedPrincipals,
    "agent.deleted":                payloadAffectedPrincipals,
    "org.member_removed":           payloadAffectedPrincipals,
    "principal.permissions_changed": payloadAffectedPrincipals,
}

func payloadAffectedPrincipals(env kafka.EventEnvelope) ([]uuid.UUID, error) {
    var p struct {
        AffectedPrincipalIDs []uuid.UUID `json:"affected_principal_ids"`
    }
    if err := json.Unmarshal(env.Payload, &p); err != nil {
        return nil, err
    }
    return p.AffectedPrincipalIDs, nil
}
```

### Step 4 — Dedupe via Redis SET

File: `plane/application/identity-cache-invalidator/dedupe.go`

`SET event_id 1 NX EX 86400` — TTL 24h matches Kafka retention.

### Step 5 — Metrics

File: `plane/application/identity-cache-invalidator/metrics.go`

Per execution-plan §13.2 + outbox `metrics.go` convention:

```
identity_invalidations_total{event_type, result}
identity_invalidator_dlq_total{event_type, reason}
identity_invalidator_consumer_lag_seconds
identity_invalidator_oldest_unprocessed_seconds
```

### Step 6 — Kafka consumer config

File: `plane/application/identity-cache-invalidator/config.go`

| Env | Default | Purpose |
|---|---|---|
| `KAFKA_BOOTSTRAP_SERVERS` | (required) | |
| `REDIS_URL` | (required) | rediss:// in prod |
| `REDIS_USE_CLUSTER` | `true` (prod) / `false` (dev) | |
| `IDENTITY_INVALIDATOR_GROUP` | `gitscale.identity-cache-invalidator` | |
| `KAFKA_AUTO_OFFSET_RESET` | `earliest` (per #12 spec D7) | |

### Step 7 — Tests

Files:

- `plane/application/identity-cache-invalidator/consumer_test.go` — unit tests with stub `cache.Store` and mock Kafka producer.
  - Sends `user.disabled` → assert `cache.Delete(IdentityKey<user_id>)` called.
  - Sends `org.member_removed` with multiple `affected_principal_ids[]` → assert all deleted.
  - Replays same `event_id` → assert delete called once (dedupe).
  - Sends unknown event type → assert no delete + counter incremented.
- `plane/application/identity-cache-invalidator/integration_test.go` — testcontainers Redpanda + Redis.
  - Produces `user.disabled` event → asserts cache key deleted.

### Step 8 — Dockerfile + helm chart entry (optional, scope decision)

If project ships per-service Dockerfiles, add `cmd/identity-cache-invalidator/Dockerfile`. Otherwise leave to deployment PR.

## Validation gates

| Gate | Command | Pass |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Unit | `go test -race ./plane/application/identity-cache-invalidator/...` | pass |
| Integration | `go test -tags integration -race ./plane/application/identity-cache-invalidator/...` | pass |
| Manual e2e | trigger `RevokeAgent` via `cmd/identity-service`; `redis-cli GET identity:<uuid>` | returns nil after event consumed |
| Lag metric | observe metric during integration test | drops to 0 after consumer catches up |

## Acceptance checklist

- [ ] Consumer subscribes to `gitscale.identity.events` with group `gitscale.identity-cache-invalidator`.
- [ ] Handles all 6 event types listed in Step 3.
- [ ] Iterates `affected_principal_ids[]` (not `aggregate_id`) — per #15 spec D6.
- [ ] Idempotent on `event_id` via Redis dedupe SET.
- [ ] All 4 metrics emitted with `result` label set.
- [ ] `auto.offset.reset=earliest`.
- [ ] PR description cross-links #15-revocation as the producer.
- [ ] Integration test demonstrates real round-trip (Redpanda + Redis).
- [ ] PR closes #27.

## Risks

| Risk | Mitigation |
|---|---|
| Consumer lags behind during cold start | `auto.offset.reset=earliest` + bounded backlog in metrics; alert if lag > 60s |
| `affected_principal_ids[]` missing from old payloads | schema is mandatory in #15; integration test produces real envelope from #15 emitter |
| Dedupe collisions across event types | `event_id` is UUIDv7 globally unique per #14 |
| Redis cluster split-brain causes double-delete | delete is idempotent; no harm |
| Consumer crashes after DEL but before dedupe-mark | delete + mark are independent; replay re-deletes (idempotent), then re-marks |
| Revocation events flood the topic during mass cleanup | consumer batches 100; per-message handler is O(deletes); metrics track throughput |

## Rollback

Stand-alone consumer; no other code path depends on it. Revert is a single PR drop; cached identities continue to expire by TTL (60s) without it — same behaviour as before #27 shipped.
