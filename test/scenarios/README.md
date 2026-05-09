# Test scenarios

Build-tag taxonomy (two-axis):

- Topology (mutex): `topo_single`, `topo_quorum`, `topo_full`
- Kind (orthogonal): `perf`, `chaos_link`, `chaos_blast`

Every scenario file declares both axes:

    //go:build integration && topo_quorum

`make lint-test-tags` rejects files with a kind tag but no topology tag.

Layout (populated by sub-issues 7 and 8):

    functional/    integration && topo_single
    quorum/        integration && topo_quorum
    full/          integration && topo_full
    perf/          integration && perf && (topo_single|topo_quorum)
    chaos_link/    integration && chaos_link && topo_quorum
    chaos_blast/   integration && chaos_blast && topo_quorum
