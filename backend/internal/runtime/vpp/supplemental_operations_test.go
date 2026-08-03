package vpp

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

func TestValidateSupplementalReadbackAcceptsPersistedProofsObservedAtDifferentTimes(t *testing.T) {
	// Given
	firstObserved := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	secondObserved := firstObserved.Add(30 * time.Second)
	plan := nativeSupplementalPlan(firstObserved, secondObserved)
	plan.NativePath.Now = secondObserved.Add(30 * time.Second)
	readbacks := matchingNativeReadbacks(t, plan)

	// When
	err := ValidateSupplementalReadback(plan, SupplementalInterfaces, readbacks, plan.NativePath.Now)

	// Then
	if err != nil {
		t.Fatalf("persisted supplemental readback rejected after proof expiry: %v", err)
	}
}

func TestValidateSupplementalReadbackRejectsWrongPersistedShowIdentity(t *testing.T) {
	// Given
	observedAt := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	plan := nativeSupplementalPlan(observedAt)
	readbacks := matchingNativeReadbacks(t, plan)
	readbacks[0].Shows[0].Stdout = "interface lyroute-eth9"

	// When
	err := ValidateSupplementalReadback(plan, SupplementalInterfaces, readbacks, plan.NativePath.Now)

	// Then
	if err == nil || !strings.Contains(err.Error(), "lyroute-eth1") {
		t.Fatalf("wrong persisted show identity error = %v, want semantic identity rejection", err)
	}
}

func TestValidateSupplementalReadbackRejectsProofStaleAtPersistedTransactionTime(t *testing.T) {
	// Given
	observedAt := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	plan := nativeSupplementalPlan(observedAt)
	plan.NativePath.Now = observedAt.Add(time.Hour)
	readbacks := matchingNativeReadbacks(t, plan)

	// When
	err := ValidateSupplementalReadback(plan, SupplementalInterfaces, readbacks, plan.NativePath.Now)

	// Then
	if err == nil {
		t.Fatal("stale persisted proof was accepted after timestamp rewriting")
	}
}

func TestValidateSupplementalReadbackRejectsPayloadHashMismatch(t *testing.T) {
	// Given
	observedAt := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	plan := nativeSupplementalPlan(observedAt, observedAt)
	readbacks := matchingNativeReadbacks(t, plan)
	readbacks[0].PayloadHash = "tampered"

	// When
	err := ValidateSupplementalReadback(plan, SupplementalInterfaces, readbacks, plan.NativePath.Now)

	// Then
	if err == nil || !strings.Contains(err.Error(), "does not match desired operation") {
		t.Fatalf("payload hash mismatch error = %v, want desired-operation rejection", err)
	}
}

func TestValidateSupplementalReadbackRejectsOperationOrderMismatch(t *testing.T) {
	// Given
	observedAt := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	plan := nativeSupplementalPlan(observedAt, observedAt)
	readbacks := matchingNativeReadbacks(t, plan)
	readbacks[0], readbacks[1] = readbacks[1], readbacks[0]

	// When
	err := ValidateSupplementalReadback(plan, SupplementalInterfaces, readbacks, plan.NativePath.Now)

	// Then
	if err == nil || !strings.Contains(err.Error(), "does not match desired operation") {
		t.Fatalf("operation order mismatch error = %v, want desired-operation rejection", err)
	}
}

func TestVerifySupplementalOperationRejectsMissingNativeShowOutput(t *testing.T) {
	// Given
	operation := DataplaneAttachOperation("txn-native", NativeAttachment{LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Hook: NativeHookAFXDP, Mode: NativeModeZeroCopy})

	// When
	err := verifySupplementalOperation(operation, nil)

	// Then
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("missing native show error = %v, want typed missing-output rejection", err)
	}
}

func TestVerifySupplementalOperationRejectsWrongAFXDPHostNetdev(t *testing.T) {
	operation := DataplaneAttachOperation("txn-native", NativeAttachment{LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Hook: NativeHookAFXDP, Mode: NativeModeZeroCopy})
	results := []VPPCTLCommandResult{
		{Command: "show hardware-interfaces lyroute-eth1", Stdout: "lyroute-eth1\n  netdev eth9"},
		{Command: "show interface lyroute-eth1", Stdout: "lyroute-eth1 up"},
	}
	if err := verifySupplementalOperation(operation, results); err == nil || !strings.Contains(err.Error(), "eth1") {
		t.Fatalf("wrong AF_XDP netdev error = %v, want semantic host-interface rejection", err)
	}
}

func TestNativeSupplementalCleanupUsesStockVPPDeleteCommands(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	plan := nativeSupplementalPlan(now)
	cleanup, err := SupplementalCleanupOperations(plan, SupplementalInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup) != 1 || !operationHasCommand(cleanup[0], "?delete interface af_xdp lyroute-eth1") || !operationHasCommand(cleanup[0], "show interface") {
		t.Fatalf("AF_XDP cleanup = %#v, want stock VPP delete and inventory readback", cleanup)
	}
}

func TestRDMADVAttachUsesStockVPPCLI(t *testing.T) {
	operation := DataplaneAttachOperation("txn-rdma", NativeAttachment{LinuxInterface: "enp1s0", VPPInterface: "lyroute-enp1s0", Hook: NativeHookRDMA, Mode: NativeModeRDMADV})
	want := []string{
		"?create interface rdma host-if enp1s0 name lyroute-enp1s0 mode dv",
		"set interface state lyroute-enp1s0 up",
		"show hardware-interfaces lyroute-enp1s0",
		"show interface lyroute-enp1s0",
	}
	if !reflect.DeepEqual(operation.VPPCtlCommands, want) {
		t.Fatalf("RDMA commands = %#v, want %#v", operation.VPPCtlCommands, want)
	}
}

func TestVerifySupplementalOperationRejectsWrongProxyAttachmentIdentity(t *testing.T) {
	// Given
	steering := proxy.VPPSteeringInstruction{EgressID: "proxy-media", TargetKind: "vpp.abf.policy"}
	aclID := stableID("acl:"+steering.EgressID, 1000, 8999)
	policyID := stableID("abf:"+steering.EgressID, 1000, 8999)
	operation := Operation{Name: steering.TargetKind, Resource: steering.EgressID, Payload: steering}
	results := []VPPCTLCommandResult{
		{Command: "show acl-plugin acl index " + strconv.Itoa(aclID), Stdout: "acl " + strconv.Itoa(aclID)},
		{Command: "show abf policy " + strconv.Itoa(policyID), Stdout: "policy " + strconv.Itoa(policyID)},
		{Command: "show abf attach lyroute-eth1", Stdout: "lyroute-eth2 attached"},
	}

	// When
	err := verifySupplementalOperation(operation, results)

	// Then
	if err == nil || !strings.Contains(err.Error(), "lyroute-eth1") {
		t.Fatalf("wrong proxy identity error = %v, want exact attachment rejection", err)
	}
}

func TestSupplementalOperationsIncludesOnlyUngroupedDirectFlowTargets(t *testing.T) {
	// Given
	observedAt := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	grouped := flow.Target{Kind: "vpp.policer", RuleID: "grouped", Action: flow.ActionPolicer}
	standalone := flow.Target{Kind: "vpp.policer", RuleID: "standalone", Action: flow.ActionPolicer}
	plan := nativeSupplementalPlan(observedAt)
	plan.Flow = flow.CompiledIntent{
		Targets:   []flow.Target{grouped, standalone},
		VPPGroups: []flow.VPPObjectGroup{{Kind: grouped.Kind, Objects: []flow.VPPObject{{RuleID: grouped.RuleID, Action: grouped.Action}}}},
	}

	// When
	operations, err := supplementalOperations(plan, SupplementalQoS)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Resource != standalone.RuleID {
		t.Fatalf("supplemental direct targets = %#v, want only %q", operations, standalone.RuleID)
	}
}

func TestSupplementalQoSOwnsSmartQoSApplyAndRollbackDisable(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	proof := provenAFXDPProof(now)
	proof.SmartQoSPluginAvailable = true
	plan := Plan{
		RequestID: "txn-smart-qos-supplemental",
		NativePath: NativePathRequest{ManagementInterface: "eth0", RequireSmartQoS: true, Now: now, Assignments: []NativeAssignment{
			{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{proof}},
			{LinuxInterface: "eth2", Explicit: true, Candidates: []CapabilityProof{proof}},
		}},
		AddressAssignments: []AddressAssignment{
			{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Role: "lan", CIDR: "192.0.2.1/24", BandwidthKbps: 100000},
			{ID: "wan", LinuxInterface: "eth2", VPPInterface: "lyroute-eth2", Role: "wan", CIDR: "198.51.100.1/24", BandwidthKbps: 50000},
		},
	}
	operations, err := supplementalOperations(plan, SupplementalQoS)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || operations[0].Name != "vpp.smart-qos" || operations[1].Name != "vpp.smart-qos" {
		t.Fatalf("Smart QoS supplemental operations = %#v, want two interface operations", operations)
	}
	cleanup, err := SupplementalCleanupOperations(plan, SupplementalQoS)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup) != 2 {
		t.Fatalf("Smart QoS cleanup count = %d, want 2", len(cleanup))
	}
	for _, operation := range cleanup {
		if operation.Name != "vpp.smart-qos.rollback-delete" || !operationHasCommand(operation, " disable") || !operationHasCommand(operation, "show ly-route smart-qos") {
			t.Fatalf("Smart QoS cleanup operation = %#v, want disable plus semantic readback", operation)
		}
	}
}

func TestVerifySupplementalOperationAcceptsExactDirectFlowTargetReadback(t *testing.T) {
	// Given
	target := flow.Target{Kind: "vpp.policer", RuleID: "direct", Action: flow.ActionPolicer, Policer: &flow.Policer{RateBPS: 1_000_000, BurstBPS: 100_000}}
	operation := Operation{Name: target.Kind, Resource: target.RuleID, Payload: target}
	results := []VPPCTLCommandResult{{
		Command: "show policer name ly_route_direct",
		Stdout:  "Name \"ly_route_direct\" type 1r2c cir 1000 eir 0 cb 100 eb 0\n-----------\n",
	}}

	// When
	err := verifySupplementalOperation(operation, results)

	// Then
	if err != nil {
		t.Fatalf("exact direct flow readback rejected: %v", err)
	}
}

func nativeSupplementalPlan(observedTimes ...time.Time) Plan {
	assignments := make([]NativeAssignment, 0, len(observedTimes))
	for index, observedAt := range observedTimes {
		assignments = append(assignments, NativeAssignment{LinuxInterface: "eth" + strconv.Itoa(index+1), Explicit: true, Proof: provenAFXDPProof(observedAt)})
	}
	return Plan{RequestID: "txn-supplemental", NativePath: NativePathRequest{ManagementInterface: "eth0", Assignments: assignments, Now: observedTimes[0]}}
}

func matchingNativeReadbacks(t *testing.T, plan Plan) []SupplementalOperationReadback {
	t.Helper()
	readbacks := make([]SupplementalOperationReadback, 0, len(plan.NativePath.Assignments))
	for _, assignment := range plan.NativePath.Assignments {
		operation := DataplaneAttachOperation(plan.RequestID, NativeAttachment{LinuxInterface: assignment.LinuxInterface, VPPInterface: "lyroute-" + assignment.LinuxInterface, Hook: assignment.Proof.Hook, Mode: assignment.Proof.Mode})
		hash, err := supplementalOperationHash(operation)
		if err != nil {
			t.Fatal(err)
		}
		attachment := operation.Payload.(NativeAttachment)
		readbacks = append(readbacks, SupplementalOperationReadback{Name: operation.Name, Resource: operation.Resource, PayloadHash: hash, Shows: []VPPCTLCommandResult{
			{Command: "show hardware-interfaces " + attachment.VPPInterface, Stdout: attachment.VPPInterface + "\n  netdev " + attachment.LinuxInterface},
			{Command: "show interface " + attachment.VPPInterface, Stdout: "interface " + attachment.VPPInterface},
		}})
	}
	return readbacks
}
