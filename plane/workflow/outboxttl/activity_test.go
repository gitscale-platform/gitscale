package outboxttl_test

import (
	"context"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/workflow/outboxttl"
)

func TestActivity_RejectsInvalidDomain(t *testing.T) {
	a := outboxttl.NewExpireDomainOutboxActivity(map[store.Domain]*outbox.Expirer{})
	if _, err := a.Execute(context.Background(), outboxttl.ExpireDomainInput{Domain: "bogus"}); err == nil {
		t.Fatal("expected error for unknown domain string")
	}
}

func TestActivity_RejectsUnregisteredDomain(t *testing.T) {
	a := outboxttl.NewExpireDomainOutboxActivity(map[store.Domain]*outbox.Expirer{})
	if _, err := a.Execute(context.Background(), outboxttl.ExpireDomainInput{Domain: string(store.DomainIdentity)}); err == nil {
		t.Fatal("expected error for valid-but-unregistered domain")
	}
}

func TestActivity_RejectsNilExpirerEntry(t *testing.T) {
	a := outboxttl.NewExpireDomainOutboxActivity(map[store.Domain]*outbox.Expirer{
		store.DomainIdentity: nil,
	})
	if _, err := a.Execute(context.Background(), outboxttl.ExpireDomainInput{Domain: string(store.DomainIdentity)}); err == nil {
		t.Fatal("expected error for nil expirer entry")
	}
}
