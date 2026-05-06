package identity

import "github.com/gitscale-platform/gitscale/plane/data/store"

// HumanUser is the application-layer view of an identity.human_users row.
// Aliased to the storage-layer struct so callers do not need to convert at
// every plane boundary; if/when the application view diverges (e.g. computed
// fields), this becomes a real struct with an explicit conversion.
type HumanUser = store.HumanUser

// AgentIdentity is the application-layer view of an identity.agent_identities
// row. See HumanUser for the aliasing rationale.
type AgentIdentity = store.AgentIdentity

// OrgMembership is the application-layer view of identity.org_memberships.
type OrgMembership = store.OrgMembership
