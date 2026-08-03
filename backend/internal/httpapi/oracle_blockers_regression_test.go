package httpapi

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/vpp"
)

func TestRuntimeApplyExecutesGatewayTransactionBeforeCommittedRunning(t *testing.T) {
	// Given
	proof := newProductionVPPProof(t, true)
	if err := os.WriteFile(proof.trace, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	response := proof.request(t, http.MethodPost, "/api/v1/runtime/apply", `{}`, proof.cookie)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := os.ReadFile(proof.trace)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if response.StatusCode == http.StatusOK && strings.Contains(string(body), `"status":"committed"`) && !strings.Contains(string(trace), "show interface address") {
		t.Fatalf("runtime apply reported committed without Gateway transaction: body=%s trace=%s", body, trace)
	}
	if !strings.Contains(string(trace), "show interface address") {
		t.Fatalf("runtime apply emitted no typed Gateway vppctl readback: status=%d body=%s trace=%s", response.StatusCode, body, trace)
	}
}

func TestRestartEvidenceRejectsAfterPayloadDifferentFromPersistedDesiredResource(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	desired := vpp.Plan{Interfaces: []vpp.InterfaceState{{Name: "lyroute-eth1", AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.2/24"}}}}
	before := completeOracleSnapshot(t, vpp.Snapshot{RequestID: "txn-semantic", TransactionID: "txn-semantic", ReadbackAt: now})
	after := completeOracleSnapshot(t, vpp.Snapshot{RequestID: "txn-semantic", TransactionID: "txn-semantic", ReadbackAt: now, Interfaces: []vpp.InterfaceState{{Name: "lyroute-eth1", AdminState: "up", LinkState: "up", Addresses: []string{"198.51.100.2/24"}}}})
	evidence := apply.GatewayResourceEvidence{
		Resource: "interfaces", Capability: "interfaces", TransactionID: "txn-semantic",
		Before: before, After: after, BeforeHash: before.Hash, AfterHash: after.Hash,
		ApplyReceipt: apply.ApplyReceipt{TransactionID: "txn-semantic", Capability: "interfaces", Status: apply.ReceiptApplied, AppliedAt: now},
		Readback:     apply.Readback{TransactionID: "txn-semantic", Capability: "interfaces", Timestamp: now, Fresh: true},
	}
	result := RuntimeApplyResult{TransactionID: "txn-semantic", GatewayPlan: &desired, GatewayEvidence: []apply.GatewayResourceEvidence{evidence}}

	// When
	err := completeGatewayRuntimeEvidence(result, now)

	// Then
	if err == nil || !strings.Contains(err.Error(), "after") {
		t.Fatalf("semantic mismatch error = %v, want typed after-payload rejection", err)
	}
}

func TestRestartEvidenceRejectsSupplementalPayloadHashDifferentFromPersistedDesiredPlan(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	desired := vpp.Plan{RequestID: "txn-supplemental-semantic", Interfaces: []vpp.InterfaceState{{Name: "lyroute-eth1"}}, NativePath: vpp.NativePathRequest{
		ManagementInterface: "eth0",
		Now:                 now,
		Assignments: []vpp.NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Proof: vpp.CapabilityProof{
			Hook: vpp.NativeHookAFXDP, Mode: vpp.NativeModeZeroCopy, Source: vpp.ProofSourceRuntimeProbe,
			RuntimeVerified: true, Native: true, HighPerformance: true, ObservedAt: now, ValidUntil: now.Add(time.Minute),
		}}},
	}}
	before := completeOracleSnapshot(t, vpp.Snapshot{RequestID: desired.RequestID, TransactionID: desired.RequestID, ReadbackAt: now})
	after := completeOracleSnapshot(t, vpp.Snapshot{RequestID: desired.RequestID, TransactionID: desired.RequestID, ReadbackAt: now, Interfaces: desired.Interfaces})
	evidence := apply.GatewayResourceEvidence{
		Resource: "interfaces", Capability: "interfaces", TransactionID: desired.RequestID,
		Before: before, After: after, BeforeHash: before.Hash, AfterHash: after.Hash,
		ApplyReceipt: apply.ApplyReceipt{TransactionID: desired.RequestID, Capability: "interfaces", Status: apply.ReceiptApplied, AppliedAt: now},
		Readback:     apply.Readback{TransactionID: desired.RequestID, Capability: "interfaces", Timestamp: now, Fresh: true},
		SupplementalReadback: []vpp.SupplementalOperationReadback{{
			Name: "vpp.dataplane.attach", Resource: "eth1", PayloadHash: "tampered",
			Shows: []vpp.VPPCTLCommandResult{{Command: "show interface lyroute-eth1", Stdout: "lyroute-eth1 up"}},
		}},
	}
	result := RuntimeApplyResult{TransactionID: desired.RequestID, GatewayPlan: &desired, GatewayEvidence: []apply.GatewayResourceEvidence{evidence}}

	// When
	err := completeGatewayRuntimeEvidence(result, now)

	// Then
	if err == nil || !strings.Contains(err.Error(), "supplemental_readback") {
		t.Fatalf("supplemental payload mismatch error = %v, want persisted desired-operation rejection", err)
	}
}

func completeOracleSnapshot(t *testing.T, snapshot vpp.Snapshot) vpp.Snapshot {
	t.Helper()
	hash, err := vpp.CanonicalSnapshotHash(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Hash = hash
	return snapshot
}
