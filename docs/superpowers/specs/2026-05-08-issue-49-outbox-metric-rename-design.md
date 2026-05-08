# Spec — Issue #49 Rename outbox lag metric to ADR-008 name

Date: 2026-05-08
Issue: https://github.com/gitscale-platform/gitscale/issues/49
Plane: data
Priority: p3 (Wave 0)
ADR-impact: conforming (renames to ADR-008's prescribed name)

## Problem

ADR-008 + the SLO table in `docs/architecture.md` line 435 reference
`outbox_consumer_high_water_lag_seconds`. The actual metric in
`plane/data/outbox/metrics.go` is `outbox_oldest_unprocessed_seconds`. Same
semantics, wrong name — Grafana dashboards and oncall runbooks written
against ADR-008 don't find anything.

## Goals

1. Rename the metric to match ADR-008.
2. Rename the Go field `oldestUnprocessed` → `highWaterLag` (and any test
   refs) for clarity at the source.
3. Make sure `make lint-events` and any other lint that scans for the metric
   name keeps passing.

## Non-goals

- A deprecation-alias gauge that emits both names. There is no production
  scrape target on this metric yet (the outbox consumer ships pre-launch);
  a flat rename is safe.
- Renaming any other metric. Only the lag gauge mismatches ADR-008.
- Adding new metrics or changing histograms.

## Architecture

### Code changes

`plane/data/outbox/metrics.go`:

```diff
-       // oldestUnprocessed is the age in seconds of the oldest unprocessed outbox
+       // highWaterLag is the age in seconds of the oldest unprocessed outbox
        // row. Tracks ADR-008's high-water mark — the time horizon up to which
        // every outbox row has been published to Kafka.
-       oldestUnprocessed *prometheus.GaugeVec
+       highWaterLag *prometheus.GaugeVec
```

```diff
-       m.oldestUnprocessed = factory.NewGaugeVec(prometheus.GaugeOpts{
-               Name:        "outbox_oldest_unprocessed_seconds",
+       m.highWaterLag = factory.NewGaugeVec(prometheus.GaugeOpts{
+               Name:        "outbox_consumer_high_water_lag_seconds",
                Help:        ...
        }, ...)
```

```diff
-       m.oldestUnprocessed.WithLabelValues().Set(seconds)
+       m.highWaterLag.WithLabelValues().Set(seconds)
```

Existing tests that read this metric by name update to the new string;
field-name updates ride along with the rename.

### Lint config

`make lint-events` already scans for event-related strings; the metric name
is not part of an event schema, so the rename is invisible to that lint.
Re-run it as part of the test sweep to confirm.

If a `metric-naming-lint` exists (check `Makefile` and `.githooks/`), the
new name conforms to Prometheus convention (`<subsystem>_<unit>_seconds`).
No new lint config is needed.

## Test plan

- Existing outbox metric tests: pass after the rename, asserting the new
  name in scrape output.
- `make lint-events`: pass.
- A small unit test reads the gauge by exact name to lock in the spelling.

## Acceptance checklist

- [ ] Metric named `outbox_consumer_high_water_lag_seconds`
- [ ] Field name `highWaterLag` reflects the metric
- [ ] Existing tests updated and passing
- [ ] No deprecation alias (justified above)
- [ ] PR description references ADR-008 line + the docs/design.md SLO line

## Open questions

None.

## References

- `plane/data/outbox/metrics.go:24,27,65,66,111`
- `docs/architecture.md:435`
- `docs/design.md:483`
