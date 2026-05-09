# Topology: full

Plane-multiplexed compose for L4 nightly. Proves plane isolation,
cross-plane blast-radius containment, polling-outbox fanout to >=4
idempotent consumers (search, webhooks, billing, audit), EC degraded
read. NOT a 9-host topology — node count is incidental; plane-per-
container is the signal.

Implemented in sub-issue 1 of #132.
