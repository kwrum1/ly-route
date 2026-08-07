package vpp

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

func TestApplySupplementalAllowsEmptyInitialLifecycleInventory(t *testing.T) {
	observedAt := time.Date(2026, 8, 5, 6, 0, 0, 0, time.UTC)
	plan := nativeSupplementalPlan(observedAt)
	plan.AddressAssignments = []AddressAssignment{{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Role: "lan", CIDR: "192.0.2.1/24"}}
	plan.DNSInterception = true
	v4Policy := stableID("dns-transparent-v4", 9000, 999)
	v6Policy := stableID("dns-transparent-v6", 9000, 999)
	client := &supplementalReplyClient{results: []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: ""},
		{Command: "show abf policy " + strconv.Itoa(v4Policy), Stdout: "abf:[0]: policy:" + strconv.Itoa(v4Policy) + " acl:0"},
		{Command: "show abf policy " + strconv.Itoa(v6Policy), Stdout: "abf:[1]: policy:" + strconv.Itoa(v6Policy) + " acl:1"},
		{Command: "show abf attach lyroute-eth1", Stdout: "ipv4: policy:" + strconv.Itoa(v4Policy) + " priority:0\nipv6: policy:" + strconv.Itoa(v6Policy) + " priority:0"},
		{Command: "show acl-plugin acl", Stdout: "acl-index 0 count 2 tag {ly-route-dns-transparent-v4}\nacl-index 1 count 2 tag {ly-route-dns-transparent-v6}\n"},
		{Command: "show ip fib table 100", Stdout: "0.0.0.0/0\n192.0.2.0/24\n"},
		{Command: "show ip6 fib table 101", Stdout: "::/0\n2001:db8:1::/64\n"},
	}}

	readbacks, err := (Adapter{Client: client}).ApplySupplemental(context.Background(), plan, SupplementalRoutes)
	if err != nil {
		t.Fatalf("DNS lifecycle rejected valid empty initial inventory: %v", err)
	}
	if len(readbacks) != 1 || len(readbacks[0].Shows) != len(client.results) {
		t.Fatalf("supplemental readbacks = %#v, want all lifecycle show evidence", readbacks)
	}
}

func TestSupplementalOperationsEqualIgnoresTransactionIdentityAndDetectsPayloadChange(t *testing.T) {
	now := time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC)
	left := nativeSupplementalPlan(now)
	left.RequestID = "left"
	left.AddressAssignments = []AddressAssignment{{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Role: "lan", CIDR: "192.0.2.1/24"}}
	left.DNSInterception = true
	right := left
	right.RequestID = "right"

	equal, err := SupplementalOperationsEqual(left, right, SupplementalRoutes)
	if err != nil || !equal {
		t.Fatalf("equal supplemental routes = %t, %v", equal, err)
	}
	right.AddressAssignments = []AddressAssignment{{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Role: "lan", CIDR: "192.0.3.1/24"}}
	equal, err = SupplementalOperationsEqual(left, right, SupplementalRoutes)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("changed supplemental DNS ingress was treated as equal")
	}
}

type supplementalReplyClient struct {
	results []VPPCTLCommandResult
}

func (client *supplementalReplyClient) OpenChannel(context.Context) (Channel, error) {
	return supplementalReplyChannel{results: client.results}, nil
}

type supplementalReplyChannel struct {
	results []VPPCTLCommandResult
}

func (channel supplementalReplyChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: channel.results}}, nil
}

func (supplementalReplyChannel) Close() error { return nil }

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
	var native *Operation
	for index := range cleanup {
		if cleanup[index].Name == "vpp.dataplane.attach.rollback-delete" {
			native = &cleanup[index]
			break
		}
	}
	if native == nil || !operationHasCommand(*native, "?delete interface af_xdp lyroute-eth1") || !operationHasCommand(*native, "show interface") {
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

func TestSupplementalOwnersIncludeLANControlPlaneAndTransparentDNS(t *testing.T) {
	now := time.Date(2026, 8, 5, 5, 0, 0, 0, time.UTC)
	plan := nativeSupplementalPlan(now)
	plan.AddressAssignments = []AddressAssignment{{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Role: "lan", CIDR: "192.0.2.1/24"}}
	plan.DNSInterception = true

	interfaces, err := supplementalOperations(plan, SupplementalInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	wantInterfaces := []string{"vpp.dataplane.attach", "vpp.lan-control-lcp", "vpp.management-lcp"}
	if got := operationNames(interfaces); !reflect.DeepEqual(got, wantInterfaces) {
		t.Fatalf("interface supplemental operations = %#v, want %#v", got, wantInterfaces)
	}
	routes, err := supplementalOperations(plan, SupplementalRoutes)
	if err != nil {
		t.Fatal(err)
	}
	if got := operationNames(routes); !reflect.DeepEqual(got, []string{"vpp.dns-transparent-interception"}) {
		t.Fatalf("route supplemental operations = %#v", got)
	}
}

func TestSupplementalLCPCleanupDisablesDesiredPair(t *testing.T) {
	now := time.Date(2026, 8, 5, 5, 0, 0, 0, time.UTC)
	plan := nativeSupplementalPlan(now)
	plan.AddressAssignments = []AddressAssignment{{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Role: "lan", CIDR: "192.0.2.1/24"}}
	cleanup, err := SupplementalCleanupOperations(plan, SupplementalInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range cleanup {
		management, ok := operation.Payload.(ManagementLCP)
		if !ok {
			continue
		}
		if management.Enabled {
			t.Fatalf("LCP cleanup preserved enabled payload: %#v", operation)
		}
		if !operationHasCommand(operation, "show lcp") {
			t.Fatalf("LCP cleanup has no semantic readback: %#v", operation)
		}
	}
}

func TestVerifySupplementalManagementLCPReadback(t *testing.T) {
	payload := ManagementLCP{Enabled: true, VPPInterface: "lyroute-eth1", HostInterface: "lylan-eth1"}
	operation := Operation{Name: "vpp.lan-control-lcp", Resource: "lan", Payload: payload}
	results := []VPPCTLCommandResult{{Command: "show lcp", Stdout: "itf-pair: [0] lyroute-eth1 tap4096 lylan-eth1 36 type tap\n"}}
	if err := verifySupplementalOperation(operation, results); err != nil {
		t.Fatalf("valid LCP readback rejected: %v", err)
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
	fixturePlan := plan
	fixtureNow := time.Time{}
	for _, assignment := range fixturePlan.NativePath.Assignments {
		if assignment.Proof.ObservedAt.After(fixtureNow) {
			fixtureNow = assignment.Proof.ObservedAt
		}
	}
	if !fixtureNow.IsZero() {
		fixturePlan.NativePath.Now = fixtureNow
	}
	operations, err := supplementalOperations(fixturePlan, SupplementalInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	readbacks := make([]SupplementalOperationReadback, 0, len(operations))
	for _, operation := range operations {
		hash, err := supplementalOperationHash(operation)
		if err != nil {
			t.Fatal(err)
		}
		switch payload := operation.Payload.(type) {
		case NativeAttachment:
			readbacks = append(readbacks, SupplementalOperationReadback{Name: operation.Name, Resource: operation.Resource, PayloadHash: hash, Shows: []VPPCTLCommandResult{
				{Command: "show hardware-interfaces " + payload.VPPInterface, Stdout: payload.VPPInterface + "\n  netdev " + payload.LinuxInterface},
				{Command: "show interface " + payload.VPPInterface, Stdout: "interface " + payload.VPPInterface},
			}})
		case ManagementLCP:
			output := "lcp default netns '<unset>'\n"
			if payload.Enabled {
				output += "itf-pair: [0] " + payload.VPPInterface + " tap4096 " + payload.HostInterface + " 36 type tap\n"
			}
			shows := []VPPCTLCommandResult{{Command: "show lcp", Stdout: output}}
			if payload.Enabled && payload.IPv4BroadcastLocal {
				shows = append(shows, VPPCTLCommandResult{Command: "show ip fib 255.255.255.255", Stdout: "255.255.255.255/32\n  [@12]: dpo-receive: 0.0.0.0 on local0\n"})
			}
			readbacks = append(readbacks, SupplementalOperationReadback{Name: operation.Name, Resource: operation.Resource, PayloadHash: hash, Shows: shows})
		default:
			t.Fatalf("unsupported native supplemental fixture payload %T", operation.Payload)
		}
	}
	return readbacks
}

func operationNames(operations []Operation) []string {
	names := make([]string, 0, len(operations))
	for _, operation := range operations {
		names = append(names, operation.Name)
	}
	return names
}
