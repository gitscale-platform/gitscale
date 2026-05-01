---
name: gitscale-storage-tier-lint
description: Use when adding or modifying code in plane/git/storage, when introducing a Reed-Solomon or other erasure-coding library, when changing replication factors, when writing to or reading from S3-compatible object storage, when defining hot/cold tier promotion logic, or when the user asks "where should this go — hot or cold?", "is erasure coding ok here?", "what's the replication factor?". Storage tier choices are silently wrong-by-default — applying erasure coding to hot data degrades read latency by orders of magnitude under random access, and skipping replication on hot data loses durability before the cold migration window.
---

# GitScale Storage Tier Lint

## Overview

GitScale runs two storage tiers with sharply different durability and latency profiles:

- **Hot** (< 7 days active, all interactive reads): local NVMe, **3× synchronous replication, 2-of-3 quorum writes**.
- **Cold** (> 30 days, all LFS): S3-compatible object store, **(10,4) Reed-Solomon erasure coding**.

The asymmetry isn't an aesthetic choice — small random reads on erasure-coded data require reconstruction across `k` shards, which is fine for cold sequential workloads but ruinous for hot interactive ones. Conversely, paying 3× replication for cold data multi-PB is wasteful when erasure coding gives the same durability at ~1.4× overhead.

**Core principle:** tier choice determines encoding. Don't mix.

## When to Use

Trigger on **any** of:

- Code under `plane/git/storage/hot/...` references erasure coding, Reed-Solomon, ISA-L, or `EncodeShards`-style APIs
- Code under `plane/git/storage/cold/...` (or LFS writers) skips erasure coding and writes raw bytes
- Replication factor is set: any `replicas: <N>` in config, `WithReplicas(N)`, or DDL `REPLICATION FACTOR = N`. Hot must be 3, cold N/A (erasure coding handles it).
- Hot tier code uses async/eventual replication or single-quorum writes. Hot is **synchronous** with **2-of-3 quorum**.
- Promotion logic moves data hot → cold without re-encoding, or cold → hot without reconstructing
- The user asks "should this be erasure coded?", "what's the replication factor?", "hot or cold?"

**Don't trigger** for: pure metadata code (no object payloads), test fixtures with fake encoders, benchmark code that deliberately exercises both encodings.

## The tier rules

| Property | Hot | Cold |
|---|---|---|
| Storage medium | local NVMe | S3-compatible object store |
| Encoding | 3× full-copy replication | (10,4) Reed-Solomon erasure coding |
| Write semantics | synchronous, 2-of-3 quorum | async eventual durability fine |
| Read latency target | sub-ms | seconds |
| Object size profile | small + random | large + sequential |
| Time-in-tier | < 7 days active | > 30 days OR all LFS |
| Promotion direction | written here first | demoted from hot via background job |

The 7-30 day band is the migration window. Code in that band must read from hot and, on demote, re-encode to (10,4) RS for cold.

## Workflow

1. **Identify the tier** the diff touches. If both, treat each separately.
2. **For hot tier code**:
   - Verify no erasure-coding library imports.
   - Verify replication is configured to 3.
   - Verify writes are synchronous with quorum 2.
3. **For cold tier code**:
   - Verify (10,4) RS encoding is applied.
   - Verify async writes are acceptable (no synchronous blocking on remote durability).
4. **For promotion / demotion code**:
   - Verify re-encoding happens on the boundary.
5. **Output a verdict** with file:line citations.

## Output Format

```
storage-tier: <ok | violation>
Tier touched: <hot | cold | both | promotion>
Flags:
  <file:line — what's wrong — why it's wrong for this tier>
Fix: <concrete change>
```

## Example

**Input diff:**

```go
// plane/git/storage/hot/writer.go
import "github.com/klauspost/reedsolomon"

func (w *HotWriter) Put(ctx context.Context, key string, obj []byte) error {
    enc, _ := reedsolomon.New(10, 4)
    shards, _ := enc.Split(obj)
    enc.Encode(shards)
    return w.persistShards(ctx, key, shards) // ❌
}
```

**Verdict:**

```
storage-tier: violation
Tier touched: hot
Flags:
  plane/git/storage/hot/writer.go:1 — imports reedsolomon library — hot tier uses
    3× full-copy replication, not erasure coding.
  plane/git/storage/hot/writer.go:5 — applies (10,4) RS to hot writes — read-side
    reconstruction across 10 shards is prohibitive for the small random reads
    that hot tier serves.
Fix:
  Replace shard encoding with full-copy 3× replication via the existing
  hot-tier replication client. Reserve reedsolomon imports for plane/git/storage/cold/.
```

## Common Mistakes

| Mistake | Fix |
|---|---|
| "Erasure coding is more efficient, let's use it everywhere" | Storage cost is not the only axis. Read latency under reconstruction is the dominant cost for hot. |
| Hot replication via async/eventual mode "to save tail latency" | Hot durability requires synchronous quorum. Tail latency is the price of not losing acknowledged writes. |
| Cold tier writing raw bytes "because S3 already replicates" | S3 replication is per-region durability, not the encoding-overhead optimization. (10,4) RS gives you the same durability at ~1.4× storage vs. 3× full replication, materially cheaper at PB scale. |
| Promotion code copying bytes without re-encoding | Hot bytes are full copies; cold bytes are RS-encoded shards. The bytes on disk in each tier are *not* the same shape. Re-encode on the boundary. |
| Reading from cold synchronously on a request path | Cold reads are seconds, not milliseconds. If a request needs the data, promote first or fail fast with a "not in hot tier" error. |

## Why This Matters

Mis-tiering is the kind of bug that benchmarks don't catch — both encodings work. It only shows up under production load: hot reads slow, on-call gets paged, the team spends a week tracing latency, eventually finds the bug in a six-month-old PR. Or cold tier blows out the storage budget at the next quarter's invoice and someone gets a finance email.

Catching it in the diff is cheap. Catching it later is not.
