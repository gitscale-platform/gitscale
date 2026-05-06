package stub_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/compliance"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

// TestStubMetadataStoreCompliance runs the ADR-017 contract suite against the
// in-memory stub. The 40001 race subtest is skipped because the stub does not
// provide real serializable contention; that contract is verified by the
// postgres integration compliance test.
func TestStubMetadataStoreCompliance(t *testing.T) {
	factory := func(t *testing.T) (store.MetadataStore, compliance.OutboxVerifier, func()) {
		s := stub.New()
		return s, &stubVerifier{store: s}, func() {}
	}
	compliance.RunMetadataStoreCompliance(t, factory, compliance.MetadataStoreOptions{
		SkipSerializable40001: true,
	})
}

// stubVerifier reads from stub.Store.Recorded() to satisfy the compliance
// OutboxVerifier interface. The stub store records outbox writes in the order
// their containing transactions commit.
type stubVerifier struct {
	store *stub.Store
}

func (v *stubVerifier) OutboxCount(_ context.Context, domain store.Domain, eventType string) (int, error) {
	count := 0
	for _, r := range v.store.Recorded() {
		if r.Domain == domain && r.EventType == eventType {
			count++
		}
	}
	return count, nil
}

func (v *stubVerifier) OutboxEventIDs(_ context.Context, domain store.Domain) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for _, r := range v.store.Recorded() {
		if r.Domain == domain {
			ids = append(ids, r.EventID)
		}
	}
	return ids, nil
}
