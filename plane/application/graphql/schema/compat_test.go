package schema

import (
	"sort"
	"strings"
	"testing"
)

// gitscaleExtensionTypes are excluded from the GitHub-compat diff because
// they are GitScale-specific surfaces with no GitHub counterpart.
var gitscaleExtensionTypes = map[string]struct{}{
	"Agent": {},
}

// TestGitHubSubsetCompat asserts every type/field name present in the
// vendored GitHub snapshot is also present under the same parent type in
// our SDL. Extra fields in our SDL are permitted (extensions); missing
// fields are not.
//
// A deliberate rename in our SDL must fail this test — that is the
// field-stable contract from the spec.
func TestGitHubSubsetCompat(t *testing.T) {
	t.Parallel()

	gh := extractTypeFields(GitHubSubsetSnapshot())
	ours := extractTypeFields(SDL())

	for typeName, ghFields := range gh {
		if _, skip := gitscaleExtensionTypes[typeName]; skip {
			continue
		}
		ourFields, ok := ours[typeName]
		if !ok {
			t.Errorf("type %s present in GitHub snapshot but missing from SDL", typeName)
			continue
		}
		for f := range ghFields {
			if _, ok := ourFields[f]; !ok {
				t.Errorf("type %s: GitHub field %q missing from our SDL", typeName, f)
			}
		}
	}
}

// TestSDL_HasNoOrphanedTypeFields is a sanity check that the SDL extractor
// is finding every expected type in our schema. If this fails, the
// extractor regressed.
func TestSDL_HasNoOrphanedTypeFields(t *testing.T) {
	t.Parallel()
	ours := extractTypeFields(SDL())
	required := []string{"Query", "Mutation", "Repository", "User",
		"Agent", "PullRequest", "Organization", "PullRequestConnection"}
	for _, r := range required {
		if _, ok := ours[r]; !ok {
			t.Errorf("extractor missed type %s; SDL types found: %v", r, sortedKeys(ours))
		}
	}
}

// TestSDL_DeprecatedFieldsCarryRemovalDate enforces the deprecation policy:
// every @deprecated annotation must include a removalDate argument. A
// past-dated deprecation is not flagged here (date-walk happens in CI lint).
func TestSDL_DeprecatedFieldsCarryRemovalDate(t *testing.T) {
	t.Parallel()
	sdl := SDL()
	idx := 0
	for {
		i := strings.Index(sdl[idx:], "@deprecated")
		if i < 0 {
			return
		}
		// Walk forward to first `)` or newline. If we see `removalDate:`
		// before then, pass.
		start := idx + i
		end := start
		for end < len(sdl) && sdl[end] != ')' && sdl[end] != '\n' {
			end++
		}
		seg := sdl[start:end]
		if !strings.Contains(seg, "removalDate:") {
			t.Errorf("@deprecated without removalDate at offset %d: %q", start, seg)
		}
		idx = end
	}
}

func sortedKeys(m map[string]map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
