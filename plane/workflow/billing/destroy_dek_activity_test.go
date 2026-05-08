package billing

import (
	"context"
	"errors"
	"testing"

	vault "github.com/hashicorp/vault/api"
)

// fakeVaultLogical captures Read/Write calls for assertions and replays
// scripted responses. Used by DestroyDEKActivity unit tests so the suite
// runs without a Vault testcontainer.
type fakeVaultLogical struct {
	readPath string
	readResp *vault.Secret
	readErr  error

	writeCalls []writeCall
	writeErr   map[string]error
}

type writeCall struct {
	path string
	data map[string]any
}

func (f *fakeVaultLogical) ReadWithContext(_ context.Context, path string) (*vault.Secret, error) {
	f.readPath = path
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.readResp, nil
}

func (f *fakeVaultLogical) WriteWithContext(_ context.Context, path string, data map[string]any) (*vault.Secret, error) {
	f.writeCalls = append(f.writeCalls, writeCall{path: path, data: data})
	if err := f.writeErr[path]; err != nil {
		return nil, err
	}
	return nil, nil
}

func TestDestroyDEKActivity_HappyPath_BumpsAndTrims(t *testing.T) {
	fake := &fakeVaultLogical{
		readResp: &vault.Secret{Data: map[string]any{"min_available_version": float64(0)}},
	}
	a := newDestroyDEKActivityWithLogical(fake, "transit", "platform-billing-master")

	res, err := a.Execute(context.Background(), DestroyDEKInput{
		Year: 2027, Month: 1, KEKHint: "platform-billing-v3",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.VaultKeyVersion != 3 {
		t.Errorf("VaultKeyVersion=%d want 3", res.VaultKeyVersion)
	}
	if fake.readPath != "transit/keys/platform-billing-master" {
		t.Errorf("read path=%q", fake.readPath)
	}
	if len(fake.writeCalls) != 2 {
		t.Fatalf("write calls=%d want 2", len(fake.writeCalls))
	}
	if fake.writeCalls[0].path != "transit/keys/platform-billing-master/config" {
		t.Errorf("first write path=%q", fake.writeCalls[0].path)
	}
	if fake.writeCalls[0].data["min_decryption_version"] != 4 {
		t.Errorf("min_decryption_version=%v want 4", fake.writeCalls[0].data["min_decryption_version"])
	}
	if fake.writeCalls[0].data["min_encryption_version"] != 4 {
		t.Errorf("min_encryption_version=%v want 4", fake.writeCalls[0].data["min_encryption_version"])
	}
	if fake.writeCalls[1].path != "transit/keys/platform-billing-master/trim" {
		t.Errorf("second write path=%q", fake.writeCalls[1].path)
	}
	if fake.writeCalls[1].data["min_available_version"] != 4 {
		t.Errorf("min_available_version=%v want 4", fake.writeCalls[1].data["min_available_version"])
	}
}

func TestDestroyDEKActivity_AlreadyTrimmed_Idempotent(t *testing.T) {
	fake := &fakeVaultLogical{
		readResp: &vault.Secret{Data: map[string]any{"min_available_version": float64(5)}},
	}
	a := newDestroyDEKActivityWithLogical(fake, "transit", "platform-billing-master")

	res, err := a.Execute(context.Background(), DestroyDEKInput{
		Year: 2027, Month: 1, KEKHint: "platform-billing-v3",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.VaultKeyVersion != 3 {
		t.Errorf("VaultKeyVersion=%d want 3", res.VaultKeyVersion)
	}
	if len(fake.writeCalls) != 0 {
		t.Errorf("expected no writes when already trimmed, got %d", len(fake.writeCalls))
	}
}

func TestDestroyDEKActivity_KeyDeleted_Idempotent(t *testing.T) {
	// Vault 404 → nil secret. Treated as already-shredded.
	fake := &fakeVaultLogical{readResp: nil}
	a := newDestroyDEKActivityWithLogical(fake, "transit", "platform-billing-master")

	res, err := a.Execute(context.Background(), DestroyDEKInput{
		Year: 2027, Month: 1, KEKHint: "platform-billing-v3",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.VaultKeyVersion != 3 {
		t.Errorf("VaultKeyVersion=%d want 3", res.VaultKeyVersion)
	}
	if len(fake.writeCalls) != 0 {
		t.Errorf("expected no writes when key absent, got %d", len(fake.writeCalls))
	}
}

func TestDestroyDEKActivity_BadKEKHint(t *testing.T) {
	fake := &fakeVaultLogical{}
	a := newDestroyDEKActivityWithLogical(fake, "", "")

	cases := []string{"", "platform-billing", "platform-billing-vXYZ", "platform-billing-v0"}
	for _, hint := range cases {
		_, err := a.Execute(context.Background(), DestroyDEKInput{Year: 2027, Month: 1, KEKHint: hint})
		if err == nil {
			t.Errorf("expected error for hint %q", hint)
		}
	}
}

func TestDestroyDEKActivity_TrimError_Surfaces(t *testing.T) {
	fake := &fakeVaultLogical{
		readResp: &vault.Secret{Data: map[string]any{"min_available_version": float64(0)}},
		writeErr: map[string]error{
			"transit/keys/platform-billing-master/trim": errors.New("vault: trim refused"),
		},
	}
	a := newDestroyDEKActivityWithLogical(fake, "", "")

	_, err := a.Execute(context.Background(), DestroyDEKInput{Year: 2027, Month: 1, KEKHint: "platform-billing-v3"})
	if err == nil {
		t.Fatal("expected trim error")
	}
}
