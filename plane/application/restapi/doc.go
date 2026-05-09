// Package restapi is the HTTP/JSON edge for the application plane. It mounts
// at /v1/ and wraps the in-process identity.Service and repositories.Service
// with bearer-auth, per-principal rate-limit, and request-id middleware.
//
// Plane boundary (ADR-019): application-plane only. This package MUST NOT
// import plane/git/* or plane/workflow/*. Long-running async work goes
// through Temporal via a service method, not a handler-spawned goroutine.
//
// Outbox (ADR-008): handlers do not write outbox rows directly — they
// delegate to the underlying domain services which already pair source row
// + outbox row in the same Tx.
//
// Storage swap surfaces (ADR-017): the package depends only on interfaces
// (identity.Service, repositories.Service, store.MetadataStore-derived
// readers, ratelimit.RateLimiter, PrincipalResolver). No concrete driver
// imports leak across the boundary.
package restapi
