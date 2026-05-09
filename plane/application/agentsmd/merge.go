package agentsmd

// Merge produces the effective policy for a (org, repo) pair.
//
// Semantics (per spec):
//   - If both inputs are nil/empty, the result is the empty policy.
//   - The Never list is the union of org and repo predicates.
//   - On a (name + full selector) collision, the org predicate wins and
//     the repo duplicate is dropped.
//   - Order in the result: org predicates first (in their declared order),
//     then repo predicates that did not collide (in their declared order).
//
// Either argument may be nil; a nil input is treated as an empty policy.
func Merge(org, repo *Policy) *Policy {
	merged := &Policy{SchemaVersion: SchemaVersionV1}
	seen := map[string]struct{}{}

	if org != nil && !org.Empty {
		for _, p := range org.Never {
			k := p.key()
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			merged.Never = append(merged.Never, p)
		}
	}
	if repo != nil && !repo.Empty {
		for _, p := range repo.Never {
			k := p.key()
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			merged.Never = append(merged.Never, p)
		}
	}
	merged.Empty = len(merged.Never) == 0
	return merged
}
