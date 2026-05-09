package middleware_test

import (
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/application/graphql/middleware"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	storestub "github.com/gitscale-platform/gitscale/plane/data/store/stub"
)

func mustParse(t *testing.T, src string) cost.Operation {
	t.Helper()
	doc, err := cost.ParseQuery(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc.Operations[0]
}

func TestSelectStore_QueryGoesToReader(t *testing.T) {
	t.Parallel()
	r, p := storestub.New(), storestub.New()
	pools := middleware.Pools{Reader: r, Primary: p}
	got := middleware.SelectStore(pools, mustParse(t, `{ user(login:"a") { id } }`))
	if got != store.MetadataStore(r) {
		t.Errorf("query routed to wrong store; want Reader")
	}
}

func TestSelectStore_MutationGoesToPrimary(t *testing.T) {
	t.Parallel()
	r, p := storestub.New(), storestub.New()
	pools := middleware.Pools{Reader: r, Primary: p}
	got := middleware.SelectStore(pools, mustParse(t, `mutation { createAgent(input: {parentUserId:"u", displayName:"a", permissionScope:["x"]}) { agent { id } } }`))
	if got != store.MetadataStore(p) {
		t.Errorf("mutation routed to wrong store; want Primary")
	}
}

func TestSelectStore_LiveReadDirectiveForcesPrimary(t *testing.T) {
	t.Parallel()
	r, p := storestub.New(), storestub.New()
	pools := middleware.Pools{Reader: r, Primary: p}
	got := middleware.SelectStore(pools, mustParse(t, `{ user(login:"a") @liveRead { id } }`))
	if got != store.MetadataStore(p) {
		t.Errorf("liveRead directive should route to Primary")
	}
}

func TestSelectStore_NestedLiveReadForcesPrimary(t *testing.T) {
	t.Parallel()
	r, p := storestub.New(), storestub.New()
	pools := middleware.Pools{Reader: r, Primary: p}
	got := middleware.SelectStore(pools, mustParse(t, `{ repository(owner:"o", name:"r") { pullRequests @liveRead { nodes { id } } } }`))
	if got != store.MetadataStore(p) {
		t.Errorf("nested liveRead should route to Primary")
	}
}
