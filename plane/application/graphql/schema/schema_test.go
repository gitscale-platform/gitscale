package schema_test

import (
	"strings"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/schema"
)

func TestSDL_ContainsRequiredRoots(t *testing.T) {
	t.Parallel()
	sdl := schema.SDL()
	for _, want := range []string{
		"type Query",
		"type Mutation",
		"type Repository",
		"type User",
		"type Agent",
		"type PullRequest",
		"type Organization",
		"directive @cost",
		"directive @liveRead",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
}
