package middleware

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/application/graphql/cost"
	"github.com/gitscale-platform/gitscale/plane/data/store"
)

// Pools bundles the two MetadataStore instances the GraphQL surface
// dispatches against. Both must pass plane/data/compliance — the
// swap-surface invariant from ADR-017 is preserved by injecting two
// distinct stores at startup.
type Pools struct {
	Reader  store.MetadataStore
	Primary store.MetadataStore
}

// LiveReadDirective is the SDL directive whose presence on a field forces
// a Primary-pool dispatch even for a query operation.
const LiveReadDirective = "liveRead"

// SelectStore picks Reader or Primary based on the operation type and
// directive presence on the parsed document.
//
// Mutation → Primary (always).
// Query with @liveRead anywhere in the operation → Primary.
// Otherwise → Reader.
func SelectStore(pools Pools, op cost.Operation) store.MetadataStore {
	if op.Kind == cost.OpMutation {
		return pools.Primary
	}
	if hasLiveRead(op.Sels) {
		return pools.Primary
	}
	return pools.Reader
}

func hasLiveRead(sels []cost.Selection) bool {
	for _, s := range sels {
		switch s.Kind {
		case cost.SelField:
			for _, d := range s.Field.Directives {
				if d == LiveReadDirective {
					return true
				}
			}
			if hasLiveRead(s.Field.Sels) {
				return true
			}
		case cost.SelInlineFragment:
			if hasLiveRead(s.Fragment.Sels) {
				return true
			}
		}
	}
	return false
}

type ctxStoreKey struct{}

// WithStore returns ctx carrying the resolved MetadataStore for the
// current operation. Resolvers read this via StoreFrom.
func WithStore(ctx context.Context, s store.MetadataStore) context.Context {
	return context.WithValue(ctx, ctxStoreKey{}, s)
}

// StoreFrom returns the MetadataStore selected for this request, or nil
// if none was attached (defensive — should never happen in production).
func StoreFrom(ctx context.Context) store.MetadataStore {
	if v := ctx.Value(ctxStoreKey{}); v != nil {
		if s, ok := v.(store.MetadataStore); ok {
			return s
		}
	}
	return nil
}
