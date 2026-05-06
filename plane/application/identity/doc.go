// Package identity is the application-plane domain service for HumanUser
// and AgentIdentity CRUD plus revocation. It owns the credential-hash policy
// and the outbox event payload shapes for identity events (ADR-008).
//
// Implementations satisfy the Service interface using a MetadataStore from
// plane/data/store (ADR-017 swap surface). State mutations open a single
// serializable transaction that writes the source row + an identity_outbox
// row in the same Tx (ADR-008). Workflow-plane callers reach this service
// through the application-plane gRPC surface (ADR-019); they do not call
// MetadataStore directly.
//
// The package ships in three sequenced PRs:
//
//   - #15-stub        — this PR: Service interface, models, events, credential
//                       hasher, retry helper, and an in-memory stub impl over
//                       the stub MetadataStore from #14.
//   - #15-postgres    — postgres_service.go wired against the real postgres
//                       MetadataStore + integration tests.
//   - #15-revocation  — DisableUser, RevokeAgent, UpdateAgentPermissions,
//                       AddOrgMember, RemoveOrgMember + emitters + gRPC.
package identity
