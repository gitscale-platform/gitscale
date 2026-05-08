# Spike + ADR draft: #34 Billing `usage_events` archival tier and format

**Date:** 2026-05-06
**Issue:** #34 `[ADR] Billing usage_events archival tier and format`
**Status:** Spike complete; recommended decisions ready for ADR-018
**Branch:** `adr/data-billing-archival-tier`
**Blocks:** #18-archive (the archive arm of the partition rollover workflow)

## 1. Context

`005_billing.sql` (#23, merged) creates `billing.usage_events` partitioned monthly with 12 initial partitions covering 2026-05 through 2027-04. Issue #18 will add a Temporal workflow that:

1. Rolls partitions forward (creates the next month's partition before EoM).
2. Detaches and archives partitions older than the retention horizon.

The archival activity needs a target storage tier and format. **No ADR currently specifies this.** ADR-002 (Git tiering) covers Git data; ADR-011 (per-org encryption with scoped dedup) governs cold-tier Git encryption. Neither covers operational analytics / billing data.

#34 lists 7 open questions. This spike resolves each with a recommendation backed by the data-shape and access-pattern profile of `usage_events`.

## 2. Data-shape profile

From `005_billing.sql`:

| Property | Value |
|---|---|
| Table | `billing.usage_events` |
| Partitioning | `PARTITION BY RANGE (ts)` |
| Partition stride | 1 month |
| Row volume estimate | ~10⁹ rows / month at scale (agent traffic dominates per CLAUDE.md core principle 1) |
| Row size estimate | ~200 bytes (UUIDs + numeric counters + JSONB cost_vector) |
| Monthly partition size estimate | ~200 GB raw / ~60 GB Parquet+zstd |
| Read pattern | Audit (rare, broad scan), reconciliation (rare, broad scan), dispute investigation (very rare, point lookup) |
| Write pattern | Append-only via outbox-driven idempotent insert (`external_event_id UNIQUE`) |
| Compliance horizon | US tax + SOC2: 7 years for invoice-supporting data |

The profile is unambiguous: **cold, append-only, broad-scan analytical reads, mandatory long retention**. This is the canonical analytics-lake profile.

## 3. Decision matrix — seven open questions

### Q1 — Storage target

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Same S3-compatible bucket as Git cold tier** | One bucket to operate; existing IAM scope | Mixed access patterns (Git cold = small random reads on EC stripes; billing archive = large sequential scans on Parquet) → cache + lifecycle policies fight each other; encryption scope (ADR-011) is per-org for Git, but billing data is org-keyed not repo-keyed → org-master-key reuse semantically ambiguous | **Reject** |
| **Separate analytics-lake bucket** | Clean access-pattern segregation; lifecycle policy simple (Glacier transition at 90 days); IAM scope minimal (read = analyst+reconciler; write = workflow worker only) | One more bucket; one more IAM policy | **Recommend** |

**Recommendation: separate analytics-lake bucket** named `gitscale-analytics-${env}`. Lifecycle: standard tier 0–90 days, Glacier Instant Retrieval 90 days–2 years, Glacier Deep Archive 2y–7y, expire at 7y+30d.

### Q2 — Erasure coding vs replication

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **(10,4) Reed-Solomon EC (mirrors ADR-002 cold-tier)** | ~93% storage-cost saving vs replication; existing project EC tooling | Complex for a small dataset; reconstruct cost on broad scans is real; data set is < 25 TB even after 7 years (~0.01% of Git cold tier) | **Reject** |
| **3× replication (S3 default)** | Operationally trivial — S3 does it; no code path to maintain; suits broad-scan analytical reads | Higher per-GB cost than EC | **Recommend** |
| **S3 + Glacier tiers (lifecycle managed)** | S3 picks the right durability shape per tier; ~95% cost reduction without us building EC for analytics | Restore latency from Glacier Deep is hours | **Adopt** for old partitions (>2y) |

**Recommendation: 3× replication via S3 standard tier for ≤90 days; lifecycle to Glacier classes for older partitions.** No project-side EC for billing archive. This is a different cost-shape than Git cold tier (where every read is interactive) — billing reads are batch-acceptable.

### Q3 — Format

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **JSONL** | Trivial to write; easy to grep | ~3× the storage of Parquet; no column projection; no predicate pushdown | **Reject** |
| **Parquet + zstd** | Columnar; ~70% smaller than JSONL; predicate pushdown via Athena/Trino/DuckDB; rich timestamp + numeric type support; mature Go writers (`parquet-go`) | Schema discipline required; small writer learning curve | **Recommend** |
| **Avro** | Strong schema; Confluent ecosystem | Row-oriented; loses predicate-pushdown advantage; bigger than Parquet | **Reject** |

**Recommendation: Parquet + zstd compression, one file per detached partition** (~60 GB compressed for a typical month). Schema embedded in the file's footer. Writer: `github.com/parquet-go/parquet-go`.

**File layout.**

```
s3://gitscale-analytics-${env}/billing/usage_events/
  year=2026/month=05/
    usage_events_2026_05.parquet          # main file
    usage_events_2026_05.manifest.json    # row count, hash, schema version, source partition name
    usage_events_2026_05.checksum.sha256  # integrity check
```

Hive-style partitioning (`year=YYYY/month=MM/`) makes Athena/Trino partition pruning work without configuration.

### Q4 — Query path

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Athena (AWS) / Trino (cloud-agnostic)** | SQL surface; Hive partitioning; minimal new infrastructure | Cost per scan; needs IAM + Glue Data Catalog (Athena) or Trino cluster | **Recommend** Athena where AWS-native; Trino where multi-cloud |
| **DuckDB on local laptop** | Zero infrastructure; analyst-friendly | No central governance; downloads up to TBs to laptop | **Adopt** as fallback; document |
| **Restore to PG via FDW** | SQL-identical to live data | Slow; requires PG-side resources; doesn't scale to 7y of data | **Adopt** only for dispute-investigation point lookups |
| **Write-and-forget** | Simplest | Auditors will ask; saying "we have it but cannot read it" is a finding | **Reject** |

**Recommendation: primary query path is Athena (AWS) or Trino (multi-cloud); fallback DuckDB; restore-via-FDW for dispute investigation only.** Glue Data Catalog (or Hive metastore) is registered as part of the archive activity — the workflow registers each new file in the catalog after upload.

### Q5 — Retention horizon

| Driver | Floor |
|---|---|
| US tax / SOC2 / invoice-supporting data | 7 years |
| GDPR (EU customers) | "no longer than necessary" — 7y is defensible; 10y is not |
| Industry baseline for billing audit | 7 years |

**Recommendation: 18 months hot in PG (active live + 6 month retention overlap), then archive. Total retention 7 years from partition creation. Original issue body proposed 13 months hot; bumping to 18 lets Q1+Q2 reconciliation across calendar-year close happen against live data.**

After 7 years, lifecycle policy expires the file from S3.

**Calendar:**

| Age range | Storage |
|---|---|
| 0–18 months | PG live partition |
| 18 months – 2 years | S3 Standard (Parquet) |
| 2y – 7y | S3 Glacier Instant Retrieval |
| 7y – 7y 30d | S3 Glacier Deep Archive (grace) |
| 7y 30d+ | expired |

### Q6 — Restore path

**Recommendation: tiered, by use case.**

| Use case | Path | Latency |
|---|---|---|
| Analyst point query (most common) | Athena query on Parquet directly — no restore needed | seconds–minutes |
| Reconciliation across multiple months | Athena multi-partition scan | minutes–hours |
| Dispute investigation requiring SQL semantics identical to live PG | Manual Temporal workflow `RestorePartition(year, month)` — pulls Parquet, loads into a quarantine table in PG, attaches as read-only FDW partition | hours |
| Bulk export for legal hold | Direct S3 download | depends on volume |

The `RestorePartition` workflow is **out of scope for #18-archive** — it ships as a follow-up. ADR-018 acknowledges its existence; #18-archive does not implement it.

### Q7 — Encryption at rest

**Recommendation: ADR-011 inheritance with scope adjustment.**

| Property | ADR-011 (Git cold tier) | This decision (billing archive) |
|---|---|---|
| KEK location | HashiCorp Vault | HashiCorp Vault |
| KEK scope | Per-org master | **Single platform-master KEK for billing** |
| DEK derivation | `HKDF(org_master, repo_id)` | `HKDF(platform_master, year-month)` |
| Object encryption | Per-object content key derivation | Per-file (one KEK use per Parquet file) |
| Dedup | Within-repo always; cross-repo within-org behind flag | **N/A** — no dedup; usage_events are unique by `external_event_id` |
| Crypto-shred semantics | Per-repo DEK destroy = O(1) repo deletion | **Per-month DEK destroy = O(1) month-level deletion** for retention enforcement |

**Rationale.** Per-org keys are wrong here: usage_events span all orgs in a single time window; per-org files would explode the file count and break the lifecycle/Athena-pruning model. A platform-level KEK with per-month DEK preserves crypto-shred semantics (retention deletes the month's DEK = the data is unrecoverable) without exploding file cardinality.

**Compliance note.** GDPR right-to-erasure for billing data is bounded by the legal-retention floor — we cannot delete usage_events for an EU customer before the 7y horizon, but we can guarantee post-horizon erasure via DEK destruction.

## 4. Recommended ADR-018 text

The text below is ready to land as ADR-018 in `docs/architecture.md §8`.

---

### ADR-018: Adopted analytics-lake archival for billing `usage_events` (separate bucket, Parquet+zstd, S3 lifecycle, platform-KEK with per-month DEK)

- **Status:** Proposed
- **Date:** 2026-05-06
- **Context:** `billing.usage_events` is a high-volume, append-only operational-analytics table partitioned monthly in PostgreSQL. After the hot retention horizon, partitions must be archived in a queryable, durable, encrypted, retention-managed format. ADR-002's Git cold-tier shape (per-org encryption, EC striping) is a poor fit: usage_events span all orgs in a single time window, reads are broad scans rather than small random reads, and the data set is small relative to Git cold storage.

- **Decision:**

  **Storage target.** Separate S3-compatible bucket `gitscale-analytics-${env}` distinct from the Git cold-tier bucket. Hive-partitioned layout `billing/usage_events/year=YYYY/month=MM/usage_events_YYYY_MM.parquet`, plus matching `.manifest.json` and `.checksum.sha256` siblings.

  **Format.** Parquet with zstd compression. Schema embedded; one file per source PG partition. Writer is the `DetachAndArchivePartition` activity in the billing partition rollover workflow (#18-archive).

  **Durability.** S3 standard 3× replication for ≤90 days, lifecycle-transitioned to Glacier Instant Retrieval 90d–2y, Glacier Deep Archive 2y–7y, expired at 7y+30d. No project-managed erasure coding; S3 native durability suffices for this workload's read profile.

  **Hot retention.** 18 months in PG (active + reconciliation overlap), then archive. Total retention 7 years from partition creation; aligned with US tax / SOC2 / industry billing-audit floors.

  **Query path.** Athena (AWS) or Trino (multi-cloud) is the primary analyst query path. Glue Data Catalog (or Hive metastore) is updated by the archive activity after each upload. DuckDB-on-laptop is an acceptable fallback for ad-hoc work. A separate `RestorePartition` Temporal workflow handles dispute investigations that require SQL parity with live PG (out of scope for #18-archive; deferred).

  **Encryption.** Platform-level KEK in HashiCorp Vault. Per-month DEK derived as `HKDF(platform_billing_master, "year-month")`. Each Parquet file is encrypted with one DEK use. Crypto-shred semantics preserved for post-7y deletion: destroying the month's DEK renders the corresponding archive unrecoverable. ADR-011's per-org encryption pattern is intentionally NOT applied here — usage_events span orgs in a single time window, and per-org files would explode cardinality and break Athena partition pruning.

  **Cross-org dedup.** Not applicable. `usage_events.external_event_id UNIQUE` guarantees row uniqueness; there is no dedup decision to make.

- **Consequences:**

  Audit and reconciliation queries are SQL-native via Athena/Trino without restore. Storage cost is ~95% lower at year 2+ vs always-on PG. Crypto-shred preserves post-retention erasure guarantees. The decision is decoupled from ADR-002 (Git cold tier): the two cold tiers can evolve independently. `RestorePartition` capability is acknowledged but deferred. The shape locks in for 7 years of data — schema evolution must be backwards-compatible at the Parquet level (Parquet supports column-add gracefully; column-rename or column-drop is a breaking change and requires a v2 directory).

---

## 5. Implementation plan (for the doc-only PR)

### Files to modify

- `docs/architecture.md` — append ADR-018 text (§ 4 above) after ADR-017 in §8.
- `docs/architecture.md` §8 ADR-002 — add cross-reference: *"For operational-analytics archives (e.g. billing.usage_events), see ADR-018."*
- `docs/architecture.md` §8 ADR-011 — add cross-reference: *"Billing archive encryption follows a different KEK scope per ADR-018."*

### Acceptance criteria

- [ ] ADR-018 appears in `docs/architecture.md §8` with `Status: Proposed`.
- [ ] `make lint-md` introduces zero NEW errors in the modified range.
- [ ] ADR-002 + ADR-011 carry cross-reference lines.
- [ ] Issue #34 closed by this PR.
- [ ] Issue #18-archive references ADR-018 as the unblocker.

## 6. Implementation plan (for #18-archive — referenced from this spec, not implemented in this PR)

The archival workflow's Temporal activity needs to:

1. Detach partition: `ALTER TABLE billing.usage_events DETACH PARTITION billing.usage_events_YYYY_MM CONCURRENTLY`.
2. Stream rows from detached partition → Parquet writer → S3 multipart upload.
3. Compute SHA-256 of the Parquet file; write `.checksum.sha256`.
4. Write `.manifest.json` (row count, schema version, source partition name, archive timestamp, KEK version).
5. Update Glue Data Catalog / Hive metastore with the new partition pointer.
6. Drop the detached partition: `DROP TABLE billing.usage_events_YYYY_MM`.
7. Emit `billing.partition_archived` event to outbox via app-plane RPC (per ADR-019).

Each step is a separate activity; the workflow handles retry + compensation. Compensation: if step 6 fails after S3 upload succeeds, the workflow logs and pages — the partition exists in both places, but no data is lost.

## 7. Risk mitigations

| Risk | Mitigation |
|---|---|
| Parquet schema evolution breaks old readers | Use Parquet's optional-field semantics; new fields default to null in old files. Document in archive manifest's `schema_version`. Forbid field rename/drop without a v2 directory. |
| Glue Data Catalog drifts from S3 reality | Archive workflow writes the catalog entry as a final step; if it fails, the partition is "dark" until a reconciliation workflow runs (out of scope; tracked separately). |
| KEK rotation during a multi-year retention | DEK is derived from `(platform_master, year-month)`; rotating `platform_master` requires re-wrapping all DEKs. Use Vault's transit engine which handles versioned KEKs natively. Manifest records `kek_version`. |
| Cost runaway from analyst Athena scans | Per-tenant cost allocation tag on the bucket; alarm on monthly spend > $X; enforce partition-pruning on every ad-hoc query via a query template with `WHERE year=?` mandatory. |
| Restore latency from Glacier Deep Archive (12+ hours) | Document explicitly in incident runbooks. For dispute investigation typically the 90d–2y range is sufficient (Glacier IR = minutes), so Deep Archive restore is genuinely rare. |
| Compliance officer wants > 7y retention for some jurisdictions | Lifecycle policy is per-prefix; a `legal-hold/` prefix can be configured with no expiration. ADR doesn't require ongoing change. |

## 8. Cross-references

- ADR-002 (Git cold-tier 10,4 Reed-Solomon) — different access pattern; ADR-018 explicitly diverges.
- ADR-008 (outbox) — `billing.partition_archived` event uses the standard outbox pattern via app-plane RPC.
- ADR-011 (per-org encryption) — different KEK scope; ADR-018 explicitly diverges.
- ADR-012 (two-tier billing counters) — usage_events is the analytics-counter side; archive is the cold tail.
- ADR-019 (workflow→app-plane boundary) — `billing.partition_archived` outbox emit goes via app-plane RPC, not direct from workflow.
- #18-archive — depends on this ADR; archive activity built atop the decisions here.
- Open architecture question: erasure coding library (June 2026) — explicitly does NOT block this ADR; ADR-018 chooses not to use project EC for billing archives, so the library decision is irrelevant here.

## 9. Open follow-ups (not in #34 / ADR-018 scope)

- **`RestorePartition` workflow** — separate issue under workflow plane.
- **Glue / Hive catalog wiring** — separate issue under data plane (Terraform + IAM).
- **Cost allocation tagging convention** — separate issue under observability.
- **Athena query template library** — separate issue under data plane.
- **Multi-region replication of analytics-lake bucket** — disaster-recovery concern; defer until DR plan is in place.
