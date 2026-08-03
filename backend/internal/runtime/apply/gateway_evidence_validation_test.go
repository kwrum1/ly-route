package apply

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/vpp"
)

func TestPreviousStateRejectsEachMalformedGatewayEvidenceField(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	clock := deterministicClock(time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, _ := gatewayEvidenceAdapters(clock, &trace, "")
	executor := gatewayEvidenceExecutor(store, clock, &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock})
	request := validRequest("txn-evidence-validation")
	if _, err := executor.Run(ctx, request); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.RuntimeSnapshot(ctx, request.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	var payload SnapshotPayload
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	// When / Then
	cases := []struct {
		name string
		code GatewayEvidenceValidationCode
		edit func(*GatewayResourceEvidence)
	}{
		{"missing resource", GatewayEvidenceIncomplete, func(item *GatewayResourceEvidence) { item.Resource = "" }},
		{"capability mismatch", GatewayEvidenceCapabilityMismatch, func(item *GatewayResourceEvidence) { item.Capability = "other" }},
		{"aggregate transaction mismatch", GatewayEvidenceTransactionMismatch, func(item *GatewayResourceEvidence) { item.TransactionID = "other" }},
		{"receipt transaction mismatch", GatewayEvidenceReceiptMismatch, func(item *GatewayResourceEvidence) { item.ApplyReceipt.TransactionID = "other" }},
		{"receipt capability mismatch", GatewayEvidenceReceiptMismatch, func(item *GatewayResourceEvidence) { item.ApplyReceipt.Capability = "other" }},
		{"receipt status invalid", GatewayEvidenceReceiptInvalid, func(item *GatewayResourceEvidence) { item.ApplyReceipt.Status = ReceiptFailed }},
		{"receipt timestamp missing", GatewayEvidenceReceiptInvalid, func(item *GatewayResourceEvidence) { item.ApplyReceipt.AppliedAt = time.Time{} }},
		{"receipt timestamp future", GatewayEvidenceReceiptInvalid, func(item *GatewayResourceEvidence) {
			item.ApplyReceipt.AppliedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"readback transaction mismatch", GatewayEvidenceReadbackMismatch, func(item *GatewayResourceEvidence) { item.Readback.TransactionID = "other" }},
		{"readback capability mismatch", GatewayEvidenceReadbackMismatch, func(item *GatewayResourceEvidence) { item.Readback.Capability = "other" }},
		{"readback freshness false", GatewayEvidenceReadbackStale, func(item *GatewayResourceEvidence) { item.Readback.Fresh = false }},
		{"readback timestamp missing", GatewayEvidenceReadbackStale, func(item *GatewayResourceEvidence) { item.Readback.Timestamp = time.Time{} }},
		{"readback timestamp stale", GatewayEvidenceReadbackStale, func(item *GatewayResourceEvidence) {
			item.Readback.Timestamp = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"readback timestamp future", GatewayEvidenceReadbackStale, func(item *GatewayResourceEvidence) {
			item.Readback.Timestamp = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		}},
		{"before transaction mismatch", GatewayEvidenceSnapshotMismatch, func(item *GatewayResourceEvidence) { item.Before.TransactionID = "other" }},
		{"before request mismatch", GatewayEvidenceSnapshotMismatch, func(item *GatewayResourceEvidence) { item.Before.RequestID = "other" }},
		{"before timestamp missing", GatewayEvidenceIncomplete, func(item *GatewayResourceEvidence) { item.Before.ReadbackAt = time.Time{} }},
		{"before hash mismatch", GatewayEvidenceSnapshotHash, func(item *GatewayResourceEvidence) { item.BeforeHash = "other" }},
		{"before canonical hash mismatch", GatewayEvidenceSnapshotHash, func(item *GatewayResourceEvidence) { item.Before.Interfaces = []vpp.InterfaceState{{Name: "changed"}} }},
		{"after transaction mismatch", GatewayEvidenceSnapshotMismatch, func(item *GatewayResourceEvidence) { item.After.TransactionID = "other" }},
		{"after request mismatch", GatewayEvidenceSnapshotMismatch, func(item *GatewayResourceEvidence) { item.After.RequestID = "other" }},
		{"after timestamp missing", GatewayEvidenceIncomplete, func(item *GatewayResourceEvidence) { item.After.ReadbackAt = time.Time{} }},
		{"after hash mismatch", GatewayEvidenceSnapshotHash, func(item *GatewayResourceEvidence) { item.AfterHash = "other" }},
		{"after canonical hash mismatch", GatewayEvidenceSnapshotHash, func(item *GatewayResourceEvidence) { item.After.Bonds = []vpp.BondState{{Name: "changed"}} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := cloneSnapshotPayload(t, payload)
			testCase.edit(&candidate.GatewayEvidence[0])
			bytes, hash, marshalErr := persistence.MarshalPayload(candidate)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			id := "snapshot-evidence-validation-" + testCase.name
			if saveErr := store.SaveApply(ctx, runtimeSnapshotRecord(id, request.TransactionID, bytes, hash)); saveErr != nil {
				t.Fatal(saveErr)
			}
			_, loadErr := executor.previousState(ctx, id)
			var validationErr *GatewayEvidenceValidationError
			if !errors.As(loadErr, &validationErr) || validationErr.Code != testCase.code {
				t.Fatalf("load error = %v, typed error = %#v, want code %q", loadErr, validationErr, testCase.code)
			}
			if !errors.Is(loadErr, ErrInvalidGatewayEvidence) {
				t.Fatalf("load error = %v, want invalid evidence sentinel", loadErr)
			}
			unchanged, marshalErr := json.Marshal(payload)
			if marshalErr != nil || string(unchanged) != string(original) {
				t.Fatalf("base fixture mutated: got %s want %s", unchanged, original)
			}
		})
	}
}

func cloneSnapshotPayload(t *testing.T, payload SnapshotPayload) SnapshotPayload {
	t.Helper()
	bytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var clone SnapshotPayload
	if err := json.Unmarshal(bytes, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
