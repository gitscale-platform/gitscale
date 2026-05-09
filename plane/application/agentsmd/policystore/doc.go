// Package policystore resolves the org-level AGENTS.md policy bytes for
// a repository. Convention: every org's policy lives in a repository
// named "<org>/.gitscale-agents" at path AGENTS.md (mirrors the GitHub
// ".github" repo convention). If the conventional repo or the file
// does not exist, ResolveOrgPolicyBytes returns (nil, nil) — i.e. no
// org policy.
//
// This package depends only on small consumer-defined interfaces
// (RepoMetadata + BlobReader). The production wiring binds a concrete
// metadata service from plane/application/repositories (when it lands)
// and a Gitaly-backed BlobReader from plane/git/gitaly. Tests inject
// in-memory fakes.
package policystore
