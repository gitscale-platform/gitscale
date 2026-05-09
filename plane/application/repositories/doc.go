// Package repositories is the application-plane service layer for the
// repositories metadata domain. It wraps store.MetadataStore Transact calls
// so source row + outbox row land in the same Tx (ADR-008).
//
// Plane boundary (ADR-019): this package is application-plane. It MUST NOT
// import plane/git/* or plane/workflow/* — physical Git repo provisioning is
// the git plane's job (issue #107) and is triggered out-of-band by a consumer
// of repositories.repository_created on gitscale.repositories.events.
package repositories
