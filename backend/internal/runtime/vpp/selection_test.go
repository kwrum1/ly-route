package vpp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPath_empty_request_has_no_operations(t *testing.T) {
	// Given
	plan := Plan{RequestID: "req-empty"}

	// When
	operations, err := BuildOperations(plan)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 {
		t.Fatalf("operations = %#v, want none", operations)
	}
}

func TestPath_empty_request_does_not_open_vpp_channel(t *testing.T) {
	// Given
	client := &fakeClient{}

	// When
	receipt, err := (Adapter{Client: client}).Apply(context.Background(), Plan{RequestID: "req-empty"})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Operations) != 0 {
		t.Fatalf("operations = %#v, want none", receipt.Operations)
	}
	if client.opened() != 0 {
		t.Fatalf("opened channels = %d, want zero", client.opened())
	}
}

func TestNativePath_proven_assignment_builds_attach_with_semantic_readback(t *testing.T) {
	// Given
	plan := provenPlan(Plan{RequestID: "req-native", AddressAssignments: []AddressAssignment{{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", CIDR: "192.0.2.1/24"}}}, "eth1")

	// When
	operations, err := BuildOperations(plan)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) == 0 || operations[0].Name != "vpp.dataplane.attach" {
		t.Fatalf("operations = %#v, want native attach first", operations)
	}
	if got := operations[0].VPPCtlCommands; len(got) != 4 || got[0] != "?create interface af_xdp host-if eth1 name lyroute-eth1 zero-copy" || got[1] != "set interface state lyroute-eth1 up" || got[2] != "show hardware-interfaces lyroute-eth1" || got[3] != "show interface lyroute-eth1" {
		t.Fatalf("attach commands = %#v", got)
	}
}

func TestLocked_path_returns_no_forwarding_operations(t *testing.T) {
	// Given
	plan := Plan{RequestID: "req-locked", AddressAssignments: []AddressAssignment{{ID: "lan", LinuxInterface: "eth1", CIDR: "192.0.2.1/24"}}, NativePath: NativePathRequest{ManagementInterface: "eth0", Assignments: []NativeAssignment{{LinuxInterface: "eth1", Explicit: true}}}}

	// When
	operations, err := BuildOperations(plan)

	// Then
	if operations != nil {
		t.Fatalf("operations = %#v, want nil", operations)
	}
	assertDataplaneLocked(t, NativePath{}, err, "runtime_capability_proof")
}

func TestNativePath_proven_zero_copy_assignment_is_selected(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	request := NativePathRequest{
		ManagementInterface: "eth0",
		Now:                 now,
		Assignments: []NativeAssignment{{
			LinuxInterface: "eth1",
			Explicit:       true,
			Proof: CapabilityProof{
				Hook:            NativeHookAFXDP,
				Mode:            NativeModeZeroCopy,
				Source:          ProofSourceRuntimeProbe,
				RuntimeVerified: true,
				Native:          true,
				HighPerformance: true,
				ObservedAt:      now.Add(-time.Minute),
				ValidUntil:      now.Add(time.Minute),
			},
		}},
	}

	// When
	path, err := SelectNativePath(request)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(path.Attachments) != 1 || path.Attachments[0].LinuxInterface != "eth1" || path.Attachments[0].Hook != NativeHookAFXDP || path.Attachments[0].Mode != NativeModeZeroCopy {
		t.Fatalf("path = %#v, want proven eth1 AF_XDP zero-copy attachment", path)
	}
	for _, result := range path.Prerequisites {
		if !result.Passed {
			t.Fatalf("prerequisite failed in selected path: %#v", result)
		}
	}
}

func TestNativePath_unsupported_or_copy_only_hook_is_locked(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		hook  NativeHook
		mode  NativeMode
		proof CapabilityProof
	}{
		{name: "unsupported hook", hook: NativeHook("idpf"), mode: NativeMode("pci")},
		{name: "AF_XDP copy mode", hook: NativeHookAFXDP, mode: NativeModeCopy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			proof := CapabilityProof{Hook: test.hook, Mode: test.mode, Source: ProofSourceRuntimeProbe, RuntimeVerified: true, Native: true, HighPerformance: true, ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
			request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Proof: proof}}}

			// When
			path, err := SelectNativePath(request)

			// Then
			assertDataplaneLocked(t, path, err, "approved_native_mode")
		})
	}
}

func TestNativePath_vmxnet3AFPacketAcceptanceBuildsHostInterface(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	proof := CapabilityProof{
		Tier: DataplaneTierNative, Hook: NativeHookAFPacket, Mode: NativeModeAFPacket,
		Source: ProofSourceRuntimeProbe, RuntimeVerified: true, Native: true,
		ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute),
		KernelDriver: "vmxnet3", MACAddress: "00:0c:29:16:5b:1e", AcceptanceOnly: true, SmartQoSPluginAvailable: true,
	}
	request := NativePathRequest{
		ManagementInterface: "eth0", AcceptanceAFPacket: true, RequireSmartQoS: true, Now: now,
		Assignments: []NativeAssignment{
			{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{proof}},
			{LinuxInterface: "eth2", Explicit: true, Candidates: []CapabilityProof{proof}},
		},
	}
	path, err := SelectNativePath(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(path.Attachments) != 2 || path.Attachments[0].Hook != NativeHookAFPacket || path.Attachments[1].Hook != NativeHookAFPacket || !path.SmartQoS {
		t.Fatalf("path = %#v", path)
	}
	operations, err := BuildOperations(Plan{RequestID: "vmxnet3-af-packet", NativePath: request, AddressAssignments: []AddressAssignment{
		{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", CIDR: "192.0.2.1/24", Role: "lan", BandwidthKbps: 100000},
		{ID: "wan", LinuxInterface: "eth2", VPPInterface: "lyroute-eth2", CIDR: "198.51.100.2/24", Role: "wan", BandwidthKbps: 100000},
	}})
	if err != nil {
		t.Fatal(err)
	}
	foundHostInterface := false
	foundMACRewrite := false
	for _, operation := range operations {
		if operation.Name == "vpp.dataplane.attach" && len(operation.VPPCtlCommands) > 0 && operation.VPPCtlCommands[0] == "?create host-interface name eth1 cksum-gso-disable" {
			foundHostInterface = true
			for _, command := range operation.VPPCtlCommands {
				if strings.HasPrefix(command, "set interface mac address ") {
					foundMACRewrite = true
				}
			}
		}
	}
	if !foundHostInterface || !foundMACRewrite {
		t.Fatalf("operations = %#v", operations)
	}
}

func TestNativePath_vmxnet3AFPacketAcceptanceRequiresLinuxMAC(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	proof := CapabilityProof{
		Tier: DataplaneTierNative, Hook: NativeHookAFPacket, Mode: NativeModeAFPacket,
		Source: ProofSourceRuntimeProbe, RuntimeVerified: true, Native: true,
		ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute),
		KernelDriver: "vmxnet3", AcceptanceOnly: true, SmartQoSPluginAvailable: true,
	}
	request := NativePathRequest{ManagementInterface: "eth0", AcceptanceAFPacket: true, Now: now, Assignments: []NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{proof}}}}
	path, err := SelectNativePath(request)
	assertDataplaneLocked(t, path, err, "common_dataplane_tier")
	var locked *DataplaneLockedError
	if !errors.As(err, &locked) || len(locked.Candidates) != 1 || locked.Candidates[0].Eligible {
		t.Fatalf("candidates = %#v, want rejected AF_PACKET candidate", locked)
	}
	if !strings.Contains(strings.Join(locked.Candidates[0].Reasons, " "), "MAC") {
		t.Fatalf("reasons = %#v, want missing MAC", locked.Candidates[0].Reasons)
	}
}

func TestNativePath_missing_hook_proof_is_locked(t *testing.T) {
	// Given
	request := NativePathRequest{ManagementInterface: "eth0", Now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC), Assignments: []NativeAssignment{{LinuxInterface: "eth1", Explicit: true}}}

	// When
	path, err := SelectNativePath(request)

	// Then
	assertDataplaneLocked(t, path, err, "runtime_capability_proof")
}

func TestManagement_assignment_attack_is_locked(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{{LinuxInterface: "eth0", Explicit: true, Proof: provenAFXDPProof(now)}}}

	// When
	path, err := SelectNativePath(request)

	// Then
	assertDataplaneLocked(t, path, err, "management_excluded")
}

func TestNativePath_unsafe_interface_name_is_locked(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{{LinuxInterface: "eth1;show version", Explicit: true, Proof: provenAFXDPProof(now)}}}

	path, err := SelectNativePath(request)

	assertDataplaneLocked(t, path, err, "interface_name_safe")
}

func TestNativePath_proof_for_different_management_interface_is_discarded(t *testing.T) {
	proofPath := filepath.Join(t.TempDir(), "proof.json")
	proof := `{"management_interface":"eth9","proofs":[{"linux_interface":"eth1","proof":{"hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"2026-07-21T11:59:00Z","valid_until":"2026-07-21T12:01:00Z"}}]}`
	if err := os.WriteFile(proofPath, []byte(proof), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	request := LoadNativePathRequest(proofPath, "eth0", []string{"eth1"}, now)

	path, err := SelectNativePath(request)

	assertDataplaneLocked(t, path, err, "capability_report_management_matches")
}

func TestManagement_forwarding_operation_attack_is_locked_before_channel_open(t *testing.T) {
	// Given
	plan := provenPlan(Plan{RequestID: "req-management-operation", AddressAssignments: []AddressAssignment{{ID: "attack", LinuxInterface: "eth0", VPPInterface: "lyroute-eth0", CIDR: "192.0.2.1/24"}}}, "eth1")
	client := &fakeClient{}

	// When
	operations, err := BuildOperations(plan)
	_, applyErr := (Adapter{Client: client}).Apply(context.Background(), plan)

	// Then
	if operations != nil {
		t.Fatalf("operations = %#v, want nil", operations)
	}
	assertDataplaneLocked(t, NativePath{}, err, "address_assignment_management_excluded")
	assertDataplaneLocked(t, NativePath{}, applyErr, "address_assignment_management_excluded")
	if client.opened() != 0 {
		t.Fatalf("opened channels = %d, want zero", client.opened())
	}
}

func TestNativePath_stale_or_static_driver_hint_is_locked(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		mutate     func(CapabilityProof) CapabilityProof
		failedName string
	}{
		{name: "stale runtime proof", failedName: "fresh_runtime_proof", mutate: func(proof CapabilityProof) CapabilityProof {
			proof.ValidUntil = now.Add(-time.Second)
			return proof
		}},
		{name: "static driver hint", failedName: "runtime_capability_proof", mutate: func(proof CapabilityProof) CapabilityProof {
			proof.Source = ProofSource("driver_hint")
			return proof
		}},
		{name: "invalid runtime proof lifetime", failedName: "valid_runtime_proof_lifetime", mutate: func(proof CapabilityProof) CapabilityProof {
			proof.ValidUntil = proof.ObservedAt
			return proof
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			proof := test.mutate(provenAFXDPProof(now))
			request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Proof: proof}}}

			// When
			path, err := SelectNativePath(request)

			// Then
			assertDataplaneLocked(t, path, err, test.failedName)
		})
	}
}

func TestNativePath_orders_multiple_assignments_deterministically(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{
		{LinuxInterface: "eth2", Explicit: true, Proof: provenAFXDPProof(now)},
		{LinuxInterface: "eth1", Explicit: true, Proof: provenAFXDPProof(now)},
	}}

	// When
	path, err := SelectNativePath(request)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(path.Attachments) != 2 || path.Attachments[0].LinuxInterface != "eth1" || path.Attachments[1].LinuxInterface != "eth2" {
		t.Fatalf("attachments = %#v, want eth1 then eth2", path.Attachments)
	}
}

func TestNativePath_malformed_capability_report_is_locked_with_typed_prerequisite(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "capabilities.json")
	if err := os.WriteFile(path, []byte(`{"management_interface":`), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	request := LoadNativePathRequest(path, "eth0", []string{"eth1"}, now)

	// When
	selected, err := SelectNativePath(request)

	// Then
	assertDataplaneLocked(t, selected, err, "capability_report_loaded")
}

func provenAFXDPProof(now time.Time) CapabilityProof {
	return CapabilityProof{Hook: NativeHookAFXDP, Mode: NativeModeZeroCopy, Source: ProofSourceRuntimeProbe, RuntimeVerified: true, Native: true, HighPerformance: true, ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
}

func assertDataplaneLocked(t *testing.T, path NativePath, err error, failedName string) {
	t.Helper()
	if len(path.Attachments) != 0 || len(path.Prerequisites) != 0 {
		t.Fatalf("path = %#v, want empty path", path)
	}
	var locked *DataplaneLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %T %v, want DataplaneLockedError", err, err)
	}
	if locked.Code() != "dataplane_locked" {
		t.Fatalf("code = %q, want dataplane_locked", locked.Code())
	}
	for _, result := range locked.Prerequisites {
		if result.Name == failedName && !result.Passed {
			return
		}
	}
	t.Fatalf("locked prerequisites missing failed %q: %#v", failedName, locked.Prerequisites)
}
