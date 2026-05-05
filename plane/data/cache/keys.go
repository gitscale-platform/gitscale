package cache

import "time"

// Key templates. The env-namespace prefix ("gitscale:<env>:") is applied by
// WithNamespace and must NOT be included here.

const (
	// RepoLocationKey identifies a repo's storage location.
	// Fill: repo UUID string.
	// TTL: RepoLocationTTL. On miss: query repositories.repositories for
	// (replica_set_id, home_region, acl_fingerprint), then cache the result.
	RepoLocationKey = "repo:loc:%s"

	// IdentityKey caches the resolved identity of a principal.
	// Fill: principal UUID string.
	// TTL: IdentityTTL. Invalidated by the identity-cache-invalidator
	// consumer on gitscale.identity.events mutations (separate issue).
	IdentityKey = "identity:%s"

	// AgentSessionQuotaKey stores the JSON-encoded SessionQuota for a parent
	// agent session. Mutated exclusively via CompareAndSwap.
	// Fill: session UUID string.
	AgentSessionQuotaKey = "quota:session:%s"
)

const (
	RepoLocationTTL        = 600 * time.Second
	RepoLocationNotFoundTTL = 30 * time.Second // negative-cache sentinel TTL

	IdentityTTL        = 60 * time.Second
	IdentityNotFoundTTL = 30 * time.Second // negative-cache sentinel TTL
)
