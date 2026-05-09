# Test fixtures

- `repos/{small,medium,large}/`: golden git bundles (`git bundle create`),
  not full directories. <5 MB / <50 MB / <200 MB. Populated in sub-issue 1.
- `seed/`: deterministic generator scripts with fixed PRNG seed and pinned
  schema version. Output cached as CI workflow artifact keyed on script SHA.
  Never check in raw .sql dumps. Populated in sub-issue 7.
