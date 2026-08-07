package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/vpp"
)

func TestRuntimeStateIgnoresOptionalComponentsThatAreNotConfigured(t *testing.T) {
	components := []RuntimeComponentState{
		{Name: "smartdns", State: "running", Available: true},
		{Name: "kea", State: "not_configured"},
		{Name: "xray", State: "not_configured"},
		{Name: "pppd", State: "running", Available: true},
		{Name: "vpp", State: "running", Available: true},
		{Name: "persistence", State: "running", Available: true},
	}
	if state := runtimeState(components); state != RuntimeStateRunning {
		t.Fatalf("runtime state = %q, want running for healthy configured components", state)
	}
}

func TestAppliedRuntimeDoesNotDegradeOnlyBecauseCommitEvidenceAges(t *testing.T) {
	now := fixedClock()()
	clock := now
	server := New(WithClock(func() time.Time { return clock }))
	transactionID := "txn-aged-in-memory"
	server.setRuntimeEvidence(RuntimeApplyResult{
		Status:        RuntimeStatusCommitted,
		RuntimeState:  RuntimeStateRunning,
		TransactionID: transactionID,
		Receipt:       apply.ApplyReceipt{TransactionID: transactionID, Capability: "runtime.apply", Status: apply.ReceiptApplied, AppliedAt: now},
		Readback:      apply.Readback{TransactionID: transactionID, Capability: "runtime.apply", Timestamp: now, Fresh: true},
		AppliedAt:     now,
	})

	clock = now.Add(apply.ReadbackFreshnessWindow + time.Minute)
	components := server.applyRuntimeEvidence([]RuntimeComponentState{{Name: "vpp", State: "running", Available: true}})
	if components[0].State != RuntimeStateRunning || !components[0].Available {
		t.Fatalf("aged committed evidence degraded a healthy component: %#v", components[0])
	}
	if components[0].Fresh {
		t.Fatalf("aged transaction evidence was reported as currently fresh: %#v", components[0])
	}
}

func TestReconcileReceiptExposesStaleReadbackCause(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-reconcile-receipt-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := fixedClock()()
	transactionID := "txn-reconcile-stale"
	result := RuntimeApplyResult{
		Status:        RuntimeStatusCommitted,
		RuntimeState:  RuntimeStateRunning,
		TransactionID: transactionID,
		Receipt:       apply.ApplyReceipt{TransactionID: transactionID, Capability: "/api/v1/config/apply", Status: apply.ReceiptApplied, AppliedAt: now.Add(-10 * time.Minute)},
		Readback:      apply.Readback{TransactionID: transactionID, Capability: "/api/v1/config/apply", Timestamp: now.Add(-10 * time.Minute), Fresh: true},
		AppliedAt:     now.Add(-10 * time.Minute),
	}
	payload, hash, err := persistence.MarshalPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveApply(ctx, persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: "snapshot-" + transactionID, SourceTransactionID: transactionID, Payload: payload, PayloadHash: hash, CreatedAt: result.AppliedAt}}); err != nil {
		t.Fatal(err)
	}

	// When
	response := request(t, New(WithStore(store), WithClock(fixedClock())), http.MethodGet, "/api/v1/runtime/status")

	// Then
	var body struct {
		LastApply RuntimeApplyResult `json:"last_apply"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.LastApply.ReconciliationReceipt.Status != apply.ReceiptDegraded || body.LastApply.ReconciliationReceipt.Cause != "stale persisted runtime readback" {
		t.Fatalf("reconciliation receipt = %#v", body.LastApply.ReconciliationReceipt)
	}
	if body.LastApply.ReconciliationReceipt.TransactionID != transactionID || body.LastApply.ReconciliationReceipt.Timestamp.IsZero() {
		t.Fatalf("reconciliation receipt identity = %#v", body.LastApply.ReconciliationReceipt)
	}
}

func TestFreshStatusRejectsCapabilityMismatch(t *testing.T) {
	// Given
	now := fixedClock()()
	result := RuntimeApplyResult{
		Status:        RuntimeStatusCommitted,
		RuntimeState:  RuntimeStateRunning,
		TransactionID: "txn-capability-mismatch",
		Receipt:       apply.ApplyReceipt{TransactionID: "txn-capability-mismatch", Capability: "runtime-a", Status: apply.ReceiptApplied, AppliedAt: now},
		Readback:      apply.Readback{TransactionID: "txn-capability-mismatch", Capability: "runtime-b", Timestamp: now, Fresh: true},
		AppliedAt:     now,
	}
	server := New(WithClock(fixedClock()))
	server.setRuntimeEvidence(result)

	// When
	response := request(t, server, http.MethodGet, "/api/v1/runtime/status")

	// Then
	if strings.Contains(response.Body.String(), `"state":"running"`) || strings.Contains(response.Body.String(), `"status":"committed"`) {
		t.Fatalf("status accepted mismatched receipt/readback capabilities: %s", response.Body.String())
	}
}

func TestReconcileRuntimeRequiresPersistedGatewayResourceEvidence(t *testing.T) {
	// Given
	now := fixedClock()()
	transactionID := "txn-gateway-restart"
	base := persistedGatewaySnapshotPayload(t, transactionID, now, false)
	cases := []struct {
		name          string
		edit          func(*apply.SnapshotPayload)
		wantCommitted bool
	}{
		{name: "complete", wantCommitted: true},
		{name: "missing", edit: func(payload *apply.SnapshotPayload) { payload.GatewayEvidence = nil }},
		{name: "stale", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence[0].Readback.Timestamp = now.Add(-apply.ReadbackFreshnessWindow - time.Second)
		}},
		{name: "extra", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence = append(payload.GatewayEvidence, payload.GatewayEvidence[0])
			payload.GatewayEvidence[1].Resource = "bonds"
			payload.GatewayEvidence[1].Capability = "bonds"
		}},
		{name: "duplicate", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence = append(payload.GatewayEvidence, payload.GatewayEvidence[0])
		}},
		{name: "wrong transaction", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence[0].TransactionID = "other"
		}},
		{name: "wrong capability", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence[0].Capability = "bonds"
		}},
		{name: "wrong hash", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence[0].AfterHash = "wrong"
		}},
		{name: "incomplete", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence[0].After.ReadbackAt = time.Time{}
		}},
		{name: "deletion only", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence[0].Deleted = true
		}, wantCommitted: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// When
			payload := cloneGatewaySnapshotPayload(t, base)
			if testCase.edit != nil {
				testCase.edit(&payload)
			}
			server := serverWithPersistedGatewayPayload(t, payload, transactionID)
			server.reconcileRuntime(context.Background())
			result := server.runtimeEvidence()

			// Then
			if (result.Status == RuntimeStatusCommitted) != testCase.wantCommitted || (result.RuntimeState == RuntimeStateRunning) != testCase.wantCommitted {
				t.Fatalf("reconciled result = %#v, want committed=%t", result, testCase.wantCommitted)
			}
			if !testCase.wantCommitted && result.ReconciliationReceipt.Status != apply.ReceiptDegraded {
				t.Fatalf("degraded reconciliation receipt = %#v", result.ReconciliationReceipt)
			}
			if testCase.wantCommitted && len(result.GatewayEvidence) != 1 {
				t.Fatalf("persisted gateway evidence = %#v", result.GatewayEvidence)
			}
		})
	}
}

func TestReconcileRuntimeAcceptsDeletionOnlyEvidenceAbsentFromDesiredPlan(t *testing.T) {
	// Given
	now := fixedClock()()
	transactionID := "txn-gateway-delete-restart"
	base := persistedGatewaySnapshotPayload(t, transactionID, now, true)
	base.GatewayPlan.Interfaces = nil
	base.GatewayEvidence[0].After.Interfaces = nil
	hash, err := vpp.CanonicalSnapshotHash(base.GatewayEvidence[0].After)
	if err != nil {
		t.Fatal(err)
	}
	base.GatewayEvidence[0].After.Hash = hash
	base.GatewayEvidence[0].AfterHash = hash
	cases := []struct {
		name          string
		edit          func(*apply.SnapshotPayload)
		wantCommitted bool
	}{
		{name: "valid deletion", wantCommitted: true},
		{name: "wrong or missing deletion outcome", edit: func(payload *apply.SnapshotPayload) { payload.GatewayEvidence[0].Deleted = false }},
		{name: "unknown deleted resource", edit: func(payload *apply.SnapshotPayload) {
			unknown := payload.GatewayEvidence[0]
			unknown.Resource = "unknown"
			unknown.Capability = "unknown"
			unknown.ApplyReceipt.Capability = "unknown"
			unknown.Readback.Capability = "unknown"
			payload.GatewayEvidence = append(payload.GatewayEvidence, unknown)
		}},
		{name: "deleted resource missing prior object", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence[0].Before.Interfaces = nil
			hash, hashErr := vpp.CanonicalSnapshotHash(payload.GatewayEvidence[0].Before)
			if hashErr != nil {
				t.Fatalf("hash before snapshot: %v", hashErr)
			}
			payload.GatewayEvidence[0].Before.Hash = hash
			payload.GatewayEvidence[0].BeforeHash = hash
		}},
		{name: "deleted resource remains after", edit: func(payload *apply.SnapshotPayload) {
			payload.GatewayEvidence[0].After.Interfaces = []vpp.InterfaceState{{Name: "eth0", AdminState: "up"}}
			hash, hashErr := vpp.CanonicalSnapshotHash(payload.GatewayEvidence[0].After)
			if hashErr != nil {
				t.Fatalf("hash after snapshot: %v", hashErr)
			}
			payload.GatewayEvidence[0].After.Hash = hash
			payload.GatewayEvidence[0].AfterHash = hash
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// When
			payload := cloneGatewaySnapshotPayload(t, base)
			if testCase.edit != nil {
				testCase.edit(&payload)
			}
			server := serverWithPersistedGatewayPayload(t, payload, transactionID)
			server.reconcileRuntime(context.Background())
			result := server.runtimeEvidence()

			// Then
			committed := result.Status == RuntimeStatusCommitted && result.RuntimeState == RuntimeStateRunning
			if committed != testCase.wantCommitted {
				t.Fatalf("reconciled result = %#v, want committed=%t", result, testCase.wantCommitted)
			}
			if !testCase.wantCommitted && result.ReconciliationReceipt.Status != apply.ReceiptDegraded {
				t.Fatalf("degraded reconciliation receipt = %#v", result.ReconciliationReceipt)
			}
		})
	}
}

func persistedGatewaySnapshotPayload(t *testing.T, transactionID string, now time.Time, deleted bool) apply.SnapshotPayload {
	t.Helper()
	snapshot := vpp.Snapshot{RequestID: transactionID, TransactionID: transactionID, ReadbackAt: now, Interfaces: []vpp.InterfaceState{{Name: "eth0", AdminState: "up"}}}
	hash, err := vpp.CanonicalSnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Hash = hash
	return apply.SnapshotPayload{
		GatewayPlan: &vpp.Plan{RequestID: transactionID, Interfaces: []vpp.InterfaceState{{Name: "eth0", AdminState: "up"}}},
		Receipt:     apply.ApplyReceipt{TransactionID: transactionID, Capability: "runtime.apply", Status: apply.ReceiptApplied, AppliedAt: now},
		Readback:    apply.Readback{TransactionID: transactionID, Capability: "runtime.apply", Timestamp: now, Fresh: true},
		GatewayEvidence: []apply.GatewayResourceEvidence{{
			Resource: "interfaces", Capability: "interfaces", TransactionID: transactionID,
			ApplyReceipt: apply.ApplyReceipt{TransactionID: transactionID, Capability: "interfaces", Status: apply.ReceiptApplied, AppliedAt: now},
			Readback:     apply.Readback{TransactionID: transactionID, Capability: "interfaces", Timestamp: now, Fresh: true},
			Deleted:      deleted, Before: snapshot, BeforeHash: hash, After: snapshot, AfterHash: hash,
		}},
	}
}

func cloneGatewaySnapshotPayload(t *testing.T, payload apply.SnapshotPayload) apply.SnapshotPayload {
	t.Helper()
	bytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var clone apply.SnapshotPayload
	if err := json.Unmarshal(bytes, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func serverWithPersistedGatewayPayload(t *testing.T, payload apply.SnapshotPayload, transactionID string) *Server {
	t.Helper()
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-gateway-reconcile-"+strings.ReplaceAll(transactionID, "-", "")+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bytes, hash, err := persistence.MarshalPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveApply(ctx, persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: "snapshot-" + transactionID, SourceTransactionID: transactionID, Payload: bytes, PayloadHash: hash, CreatedAt: payload.Receipt.AppliedAt}}); err != nil {
		t.Fatal(err)
	}
	return New(WithStore(store), WithClock(fixedClock()))
}
