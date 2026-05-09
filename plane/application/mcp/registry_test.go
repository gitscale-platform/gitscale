package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/restapi"
)

func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	tool := Tool{
		Name:        "x",
		Handler:     func(_ context.Context, _ restapi.Principal, _ json.RawMessage) (any, error) { return nil, nil },
		InputSchema: json.RawMessage(`{}`),
	}
	if err := r.Register(tool); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(tool)
	if !errors.Is(err, ErrToolAlreadyRegistered) {
		t.Fatalf("duplicate Register err = %v, want %v", err, ErrToolAlreadyRegistered)
	}
}

func TestRegistry_NilHandlerRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{Name: "x"}); err == nil {
		t.Fatal("expected error for nil handler")
	}
}

func TestRegisterDefaults_AllSeven(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r, Deps{})
	got := r.Names()
	want := AllToolNames()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("names: got %v want %v", got, want)
	}
}

func TestRegistry_ManifestSorted(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r, Deps{})
	manifest := r.Manifest()
	for i := 1; i < len(manifest); i++ {
		if manifest[i-1].Name >= manifest[i].Name {
			t.Errorf("manifest not sorted at %d: %q >= %q", i, manifest[i-1].Name, manifest[i].Name)
		}
	}
}

func TestRegistry_GetUnknown(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("nope"); ok {
		t.Fatal("expected miss")
	}
}
