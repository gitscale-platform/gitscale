// Package schema is the SDL source-of-truth for the GitScale GraphQL API.
// schema.graphql is embedded at compile-time so the runtime never reads from
// disk and so a deliberate field rename / drop is reviewable as a diff.
package schema

import _ "embed"

//go:embed schema.graphql
var sdlBytes []byte

//go:embed github_subset_snapshot.graphql
var githubSubsetBytes []byte

// SDL returns the embedded GraphQL schema definition language source.
// The bytes are immutable; callers must not mutate the returned slice.
func SDL() string { return string(sdlBytes) }

// GitHubSubsetSnapshot returns the vendored GitHub GraphQL public-schema
// snapshot trimmed to the named subset. The compat test diffs it against
// SDL().
func GitHubSubsetSnapshot() string { return string(githubSubsetBytes) }
