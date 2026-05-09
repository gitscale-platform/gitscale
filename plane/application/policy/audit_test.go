package policy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// buildChain produces n linked audit rows for tests. CreatedAt is monotone
// so determinism doesn't depend on time.Now resolution.
func buildChain(t *testing.T, n int) []AuditRow {
	t.Helper()
	policyID := uuid.New()
	rows := make([]AuditRow, n)
	prev := GenesisHash
	base := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		actor := uuid.New()
		payload, _ := json.Marshal(map[string]any{"i": i, "label": "evt"})
		rows[i] = AuditRow{
			ID:        int64(i + 1),
			PolicyID:  policyID,
			EventKind: AuditEventSubmitted,
			ActorID:   &actor,
			ActorKind: ActorKindAgent,
			Payload:   payload,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := AppendRow(prev, &rows[i]); err != nil {
			t.Fatal(err)
		}
		prev = rows[i].RowHash
	}
	return rows
}

func TestAuditChain_GenesisVerifies(t *testing.T) {
	rows := buildChain(t, 1)
	if rows[0].PrevHash != GenesisHash {
		t.Fatalf("genesis prev_hash must be all zero, got %x", rows[0].PrevHash)
	}
	idx, err := VerifyChain(rows)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if idx != -1 {
		t.Errorf("want -1, got %d", idx)
	}
}

func TestAuditChain_VerifyN(t *testing.T) {
	for _, n := range []int{1, 100, 1000} {
		rows := buildChain(t, n)
		idx, err := VerifyChain(rows)
		if err != nil {
			t.Errorf("n=%d verify error: %v", n, err)
		}
		if idx != -1 {
			t.Errorf("n=%d want -1, got %d", n, idx)
		}
	}
}

func TestAuditChain_PayloadTamperDetected(t *testing.T) {
	rows := buildChain(t, 5)
	// Mutate row 2's payload (in JSON form). The recompute will produce a
	// different RowHash; VerifyChain must surface row 2.
	rows[2].Payload = json.RawMessage(`{"i":99,"label":"tampered"}`)
	idx, err := VerifyChain(rows)
	if idx != 2 || err == nil {
		t.Errorf("tamper not detected: idx=%d err=%v", idx, err)
	}
}

func TestAuditChain_PrevHashTamperDetected(t *testing.T) {
	rows := buildChain(t, 5)
	rows[3].PrevHash[0] ^= 0xff
	idx, err := VerifyChain(rows)
	if idx != 3 || err == nil {
		t.Errorf("prev_hash tamper not detected: idx=%d err=%v", idx, err)
	}
}

func TestAuditChain_ActorKindTamperDetected(t *testing.T) {
	rows := buildChain(t, 3)
	rows[1].ActorKind = ActorKindHuman // was ActorKindAgent
	idx, err := VerifyChain(rows)
	if idx != 1 || err == nil {
		t.Errorf("actor_kind tamper not detected: idx=%d err=%v", idx, err)
	}
}

func TestCanonicalPayload_StableAcrossKeyOrder(t *testing.T) {
	a := json.RawMessage(`{"b":2,"a":1,"c":{"y":2,"x":1}}`)
	b := json.RawMessage(`{"a":1,"c":{"x":1,"y":2},"b":2}`)
	ca, err := CanonicalPayload(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := CanonicalPayload(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca) != string(cb) {
		t.Errorf("canonical not stable: %s vs %s", ca, cb)
	}
}

func TestCanonicalPayload_RejectsInvalidJSON(t *testing.T) {
	_, err := CanonicalPayload(json.RawMessage(`{not-json`))
	if err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

func TestAuditEventKind_IsValid(t *testing.T) {
	for _, k := range []AuditEventKind{AuditEventSubmitted, AuditEventApproved,
		AuditEventRejected, AuditEventEscalated, AuditEventExpired,
		AuditEventAutoApprovedNoRule, AuditEventAutoDenied} {
		if !k.IsValid() {
			t.Errorf("expected %q valid", k)
		}
	}
	if AuditEventKind("nope").IsValid() {
		t.Error("garbage kind reported valid")
	}
}

func TestActorKind_IsValid(t *testing.T) {
	for _, k := range []ActorKind{ActorKindHuman, ActorKindAgent, ActorKindService, ActorKindSystem} {
		if !k.IsValid() {
			t.Errorf("expected %q valid", k)
		}
	}
	if ActorKind("nope").IsValid() {
		t.Error("garbage actor_kind reported valid")
	}
}
