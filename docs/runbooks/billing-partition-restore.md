# Billing partition restore

**Workflow:** `billing.RestorePartitionWorkflow`
**Task queue:** `billing-maintenance`
**Issue:** #79
**ADR:** ADR-018 §Restore path (read-side); ADR-019 (data-plane import permitted for read paths and quarantine-only DDL)

## When to run

Run this workflow to restore an archived monthly partition into a quarantine
table for dispute investigation, audit-restore, or compliance review. The
quarantine table is **read-only by design** — it is never re-attached to the
live `billing.usage_events` partition tree.

If you need to materially change billing state, that is a separate
application-plane action and is **not** covered by this workflow.

## Inputs

```json
{ "Year": 2026, "Month": 5 }
```

Year must be in `[2026, 2099]`. Month must be in `[1, 12]`.

## Trigger

```sh
temporal workflow start \
  --task-queue billing-maintenance \
  --type billing.RestorePartitionWorkflow \
  --workflow-id billing.restore-partition.2026-05 \
  --input '{"Year": 2026, "Month": 5}'
```

The workflow ID convention `billing.restore-partition.YYYY-MM` makes re-runs
idempotent (subsequent attempts collide with the running ID).

## What it does

1. **FetchManifest** — `s3://<bucket>/billing/usage_events/year=YYYY/month=MM/usage_events_YYYY_MM.manifest.json`
   - validates `source_partition` matches the requested (year, month)
   - validates `schema_version == 1`
2. **VerifyChecksum** — recomputes SHA-256 over the encrypted parquet object;
   compares to the `.checksum.sha256` sidecar.
3. **DownloadAndDecrypt** — chunked AES-256-GCM-v1-4mib decoder with per-chunk
   AAD `<source_partition>:<chunk_index>`. Plaintext lands on the worker's
   scratch volume (`RESTORE_SCRATCH_DIR`, default `/tmp`).
4. **LoadIntoQuarantine** — `CREATE TABLE billing.usage_events_restore_YYYY_MM
   (LIKE billing.usage_events INCLUDING DEFAULTS)`, COPY rows in, then `REVOKE
   INSERT, UPDATE, DELETE, TRUNCATE … FROM PUBLIC`.

On failure after step 4 starts, the workflow runs `DropQuarantineActivity` as
compensation so the operator does not have to clean up a half-loaded table.

## Verifying success

```sql
-- Row count should equal the manifest's row_count.
SELECT count(*) FROM billing.usage_events_restore_2026_05;

-- Quarantine table is read-only:
INSERT INTO billing.usage_events_restore_2026_05 (id) VALUES (gen_random_uuid());
-- ERROR: permission denied for table usage_events_restore_2026_05
```

## Failure modes

| Symptom | Cause | Action |
|---|---|---|
| `ErrObjectNotFound: …manifest.json` | Archive never ran or object lifecycled out | Verify `billing.partition_archives` row exists and the bucket lifecycle policy. Restore is impossible if the parquet object is gone. |
| `ErrChecksumMismatch` | Encrypted parquet was tampered or corrupted in object storage | Do **not** re-run. Open an incident; the bucket's data integrity is suspect. |
| `ErrUnsupportedEncFormat` | Manifest declares a format this worker does not implement | Deploy a worker version that supports the manifest's `enc_format`. There is no compatibility shim by design. |
| `ErrFrameTampered` | Decrypt-time AEAD failure: bad DEK, wrong AAD, truncated chunk, or bit-flip | Almost always a **DEK version mismatch** — see "Historical key versions" below. |
| `kek_hint mismatch — manifest=… worker=…` | Vault transit key has rotated since the archive was written | See "Historical key versions" below. |

## Historical key versions (known limitation)

The current `VaultKeyProvider` always derives the DEK against Vault transit's
**latest** key version (`platform-billing-master`). After a rotation, the
`kek_hint` recorded in older manifests (e.g. `platform-billing-v1`) will not
match what the worker derives (e.g. `platform-billing-v3`), and
`DownloadAndDecryptActivity` will surface a kek_hint mismatch error.

Vault transit retains all prior key versions on rotation, so the data is
recoverable. The fix is to plumb a versioned key provider — out of scope for
this PR. Tracked as a follow-up.

**Operator workaround until that lands:**

1. Identify the manifest's `kek_hint`:

   ```sh
   aws s3 cp s3://$BUCKET/billing/usage_events/year=2026/month=05/usage_events_2026_05.manifest.json - \
     | jq .kek_hint
   ```

2. Stage a temporary worker pinned to that key version by overriding
   `VAULT_BILLING_KEY` to a Vault-side alias that points at the historical
   version, **or** request a rotation rollback via the security on-call.
3. Run the workflow against the staged worker.

## Reverting

The quarantine table is independent of the live partition tree. To remove a
restored quarantine table:

```sql
DROP TABLE billing.usage_events_restore_2026_05;
```

This has no effect on production billing state.

## References

- Spec: `docs/superpowers/specs/2026-05-08-issue-79-restore-partition-design.md`
- Plan: `docs/superpowers/plans/2026-05-08-issue-79-restore-partition-plan.md`
- Encrypt loop (must stay inverse of decoder): `plane/workflow/billing/export_activity.go`
- Decoder: `plane/workflow/billing/restore_decoder.go`
