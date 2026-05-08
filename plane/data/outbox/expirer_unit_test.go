package outbox_test

import (
	"testing"

	"github.com/gitscale-platform/gitscale/plane/data/outbox"
	"github.com/gitscale-platform/gitscale/plane/data/store"
)

func TestNewExpirer_InvalidDomainPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid domain")
		}
	}()
	_ = outbox.NewExpirer(nil, store.Domain("bogus"), outbox.ExpirerOptions{})
}
