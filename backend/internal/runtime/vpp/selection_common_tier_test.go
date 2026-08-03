package vpp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCommonTier_prefers_native_on_every_active_interface(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{
		{LinuxInterface: "eth2", Explicit: true, Candidates: []CapabilityProof{candidateProof(now, DataplaneTierDPDK, NativeHookDPDK, NativeModeDPDKVFIO, 100), candidateProof(now, DataplaneTierNative, NativeHookAFXDP, NativeModeZeroCopy, 10)}},
		{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{candidateProof(now, DataplaneTierNative, NativeHookRDMA, NativeModeRDMADV, 20), candidateProof(now, DataplaneTierDPDK, NativeHookDPDK, NativeModeDPDKVFIO, 200)}},
	}}

	path, err := SelectNativePath(request)

	if err != nil {
		t.Fatal(err)
	}
	if path.Tier != DataplaneTierNative {
		t.Fatalf("tier = %q, want native", path.Tier)
	}
	if len(path.Attachments) != 2 || path.Attachments[0].LinuxInterface != "eth1" || path.Attachments[1].LinuxInterface != "eth2" {
		t.Fatalf("attachments = %#v, want deterministic eth1/eth2", path.Attachments)
	}
	for _, attachment := range path.Attachments {
		if attachment.Tier != DataplaneTierNative || attachment.Hook == NativeHookDPDK {
			t.Fatalf("attachment = %#v, want native common tier", attachment)
		}
	}
}

func TestCommonTierRejectsDPDKWithoutSafeRuntimePrerequisites(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	proof := eligibleDPDKProof(now, 80)
	proof.IOMMUProtected = false
	proof.IOMMUGroup = ""

	_, err := SelectNativePath(NativePathRequest{
		ManagementInterface: "eth0",
		Now:                 now,
		Assignments:         []NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{proof}}},
	})

	var locked *DataplaneLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %v, want DataplaneLockedError", err)
	}
	if len(locked.Candidates) != 1 || locked.Candidates[0].Eligible || !containsString(locked.Candidates[0].Reasons, "DPDK candidate is not protected by an IOMMU group") {
		t.Fatalf("candidate evaluation = %#v", locked.Candidates)
	}
}

func TestCommonTierCarriesDPDKIdentityIntoAttachment(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	path, err := SelectNativePath(NativePathRequest{
		ManagementInterface: "eth0",
		Now:                 now,
		Assignments:         []NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{eligibleDPDKProof(now, 80)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(path.Attachments) != 1 || path.Attachments[0].PCIAddress != "0000:03:00.0" || path.Attachments[0].KernelDriver != "ixgbe" || path.Attachments[0].IOMMUGroup != "17" {
		t.Fatalf("attachments = %#v", path.Attachments)
	}
}

func TestCommonTierSmartQoSPreservesPreferredNativeTierWhenPluginIsAvailable(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	nativeOne := candidateProof(now, DataplaneTierNative, NativeHookAFXDP, NativeModeZeroCopy, 100)
	nativeOne.SmartQoSPluginAvailable = true
	nativeTwo := candidateProof(now, DataplaneTierNative, NativeHookRDMA, NativeModeRDMADV, 90)
	nativeTwo.SmartQoSPluginAvailable = true
	request := NativePathRequest{ManagementInterface: "eth0", RequireSmartQoS: true, Now: now, Assignments: []NativeAssignment{
		{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{nativeOne, eligibleDPDKProof(now, 60)}},
		{LinuxInterface: "eth2", Explicit: true, Candidates: []CapabilityProof{nativeTwo, eligibleDPDKProof(now, 50)}},
	}}

	path, err := SelectNativePath(request)
	if err != nil {
		t.Fatal(err)
	}
	if path.Tier != DataplaneTierNative {
		t.Fatalf("smart QoS tier = %q, want preferred VPP-native tier", path.Tier)
	}
	for _, attachment := range path.Attachments {
		if attachment.Tier != DataplaneTierNative {
			t.Fatalf("smart QoS attachment = %#v", attachment)
		}
	}
}

func TestCommonTierSmartQoSLocksWhenProductionPluginIsMissing(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	proof := eligibleDPDKProof(now, 80)
	proof.SmartQoSPluginAvailable = false
	_, err := SelectNativePath(NativePathRequest{ManagementInterface: "eth0", RequireSmartQoS: true, Now: now, Assignments: []NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{proof}}}})

	var locked *DataplaneLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %v, want DataplaneLockedError", err)
	}
	if len(locked.Candidates) != 1 || !containsString(locked.Candidates[0].Reasons, "production VPP smart-QoS plugin is unavailable") {
		t.Fatalf("candidate evaluation = %#v", locked.Candidates)
	}
}

func eligibleDPDKProof(now time.Time, score float64) CapabilityProof {
	return CapabilityProof{
		Tier: DataplaneTierDPDK, Hook: NativeHookDPDK, Mode: NativeModeDPDKVFIO,
		Source: ProofSourceRuntimeProbe, RuntimeVerified: true, HighPerformance: true,
		ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute), PerformanceScore: score,
		PCIAddress: "0000:03:00.0", KernelDriver: "ixgbe", IOMMUGroup: "17",
		IOMMUProtected: true, VFIOAvailable: true, HugepagesAvailable: true, DPDKPluginAvailable: true, HQoSAvailable: true, SmartQoSPluginAvailable: true,
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestCommonTier_falls_back_to_dpdk_for_whole_device(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{
		{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{candidateProof(now, DataplaneTierNative, NativeHookAFXDP, NativeModeZeroCopy, 50), candidateProof(now, DataplaneTierDPDK, NativeHookDPDK, NativeModeDPDKVFIO, 30)}},
		{LinuxInterface: "eth2", Explicit: true, Candidates: []CapabilityProof{candidateProof(now, DataplaneTierDPDK, NativeHookDPDK, NativeModeDPDKVFIO, 40)}},
	}}

	path, err := SelectNativePath(request)

	if err != nil {
		t.Fatal(err)
	}
	if path.Tier != DataplaneTierDPDK {
		t.Fatalf("tier = %q, want DPDK fallback", path.Tier)
	}
	for _, attachment := range path.Attachments {
		if attachment.Tier != DataplaneTierDPDK || attachment.Hook != NativeHookDPDK || attachment.Mode != NativeModeDPDKVFIO {
			t.Fatalf("attachment = %#v, want DPDK VFIO", attachment)
		}
	}
}

func TestBuildOperations_dpdk_selection_fails_closed_until_runtime_adapter_is_ready(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	plan := Plan{RequestID: "req-dpdk-gate", AddressAssignments: []AddressAssignment{{ID: "lan", LinuxInterface: "eth1", CIDR: "192.0.2.1/24"}}, NativePath: NativePathRequest{
		ManagementInterface: "eth0", Now: now,
		Assignments: []NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{candidateProof(now, DataplaneTierDPDK, NativeHookDPDK, NativeModeDPDKVFIO, 30)}}},
	}}

	operations, err := BuildOperations(plan)

	if operations != nil {
		t.Fatalf("operations = %#v, want fail-closed before any mutation", operations)
	}
	var locked *DataplaneLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %T %v, want locked", err, err)
	}
	assertFailedPrerequisite(t, locked.Prerequisites, "dpdk_runtime_adapter_ready")
}

func TestCommonTier_locks_when_interfaces_have_no_common_tier(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{
		{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{candidateProof(now, DataplaneTierNative, NativeHookAFXDP, NativeModeZeroCopy, 10)}},
		{LinuxInterface: "eth2", Explicit: true, Candidates: []CapabilityProof{candidateProof(now, DataplaneTierDPDK, NativeHookDPDK, NativeModeDPDKVFIO, 10)}},
	}}

	path, err := SelectNativePath(request)

	if len(path.Attachments) != 0 {
		t.Fatalf("path = %#v, want locked", path)
	}
	var locked *DataplaneLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %T %v, want DataplaneLockedError", err, err)
	}
	assertFailedPrerequisite(t, locked.Prerequisites, "common_dataplane_tier")
}

func TestCommonTier_selects_highest_measured_candidate_within_tier(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{{
		LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{
			candidateProof(now, DataplaneTierNative, NativeHookAFXDP, NativeModeZeroCopy, 40),
			candidateProof(now, DataplaneTierNative, NativeHookRDMA, NativeModeRDMADV, 90),
		},
	}}}

	path, err := SelectNativePath(request)

	if err != nil {
		t.Fatal(err)
	}
	if len(path.Attachments) != 1 || path.Attachments[0].Hook != NativeHookRDMA {
		t.Fatalf("attachments = %#v, want highest measured RDMA candidate", path.Attachments)
	}
}

func TestCommonTier_ignores_stale_candidate_when_fresh_candidate_exists(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	stale := candidateProof(now, DataplaneTierNative, NativeHookRDMA, NativeModeRDMADV, 100)
	stale.ValidUntil = now.Add(-time.Second)
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{{
		LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{stale, candidateProof(now, DataplaneTierNative, NativeHookAFXDP, NativeModeZeroCopy, 10)},
	}}}

	path, err := SelectNativePath(request)

	if err != nil {
		t.Fatal(err)
	}
	if path.Attachments[0].Hook != NativeHookAFXDP {
		t.Fatalf("attachment = %#v, want fresh AF_XDP", path.Attachments[0])
	}
	if len(path.Candidates) != 2 || path.Candidates[0].Eligible {
		t.Fatalf("evaluations = %#v, want stale candidate rejection", path.Candidates)
	}
}

func TestCommonTier_rejects_fake_dpdk_candidate(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	fake := candidateProof(now, DataplaneTierDPDK, NativeHookAFXDP, NativeModeZeroCopy, 100)
	fake.Native = true
	request := NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{fake}}}}

	_, err := SelectNativePath(request)

	var locked *DataplaneLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %T %v, want locked", err, err)
	}
	if len(locked.Candidates) != 1 || locked.Candidates[0].Eligible || len(locked.Candidates[0].Reasons) == 0 {
		t.Fatalf("candidate diagnostics = %#v", locked.Candidates)
	}
}

func TestLoadNativePathRequest_reads_multi_candidate_report(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "capabilities.json")
	report := `{"management_interface":"eth0","proofs":[{"linux_interface":"eth1","proof":{},"candidates":[{"tier":"vpp_native","hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"2026-07-30T11:59:00Z","valid_until":"2026-07-30T12:01:00Z","performance_score":42},{"tier":"vpp_dpdk","hook":"dpdk","mode":"vfio_pci","source":"runtime_probe","runtime_verified":true,"native":false,"high_performance":true,"observed_at":"2026-07-30T11:59:00Z","valid_until":"2026-07-30T12:01:00Z","performance_score":30}]}]}`
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}

	request := LoadNativePathRequest(path, "eth0", []string{"eth1"}, now)
	selected, err := SelectNativePath(request)

	if err != nil {
		t.Fatal(err)
	}
	if selected.Tier != DataplaneTierNative || len(selected.Attachments) != 1 || selected.Attachments[0].Hook != NativeHookAFXDP {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestLoadNativePathRequest_rejects_duplicate_interface_entries(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "capabilities.json")
	report := `{"management_interface":"eth0","proofs":[{"linux_interface":"eth1","proof":{"hook":"af_xdp","mode":"zero_copy"}},{"linux_interface":"eth1","proof":{"hook":"af_xdp","mode":"zero_copy"}}]}`
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}

	request := LoadNativePathRequest(path, "eth0", []string{"eth1"}, now)
	_, err := SelectNativePath(request)

	var locked *DataplaneLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("error = %T %v, want locked", err, err)
	}
	assertFailedPrerequisite(t, locked.Prerequisites, "capability_report_interface_unique")
}

func candidateProof(now time.Time, tier DataplaneTier, hook NativeHook, mode NativeMode, score float64) CapabilityProof {
	proof := CapabilityProof{
		Tier: tier, Hook: hook, Mode: mode, Source: ProofSourceRuntimeProbe,
		RuntimeVerified: true, Native: tier == DataplaneTierNative, HighPerformance: true,
		ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute), PerformanceScore: score,
	}
	if tier == DataplaneTierDPDK {
		proof.PCIAddress = "0000:03:00.0"
		proof.KernelDriver = "ixgbe"
		proof.IOMMUGroup = "17"
		proof.IOMMUProtected = true
		proof.VFIOAvailable = true
		proof.HugepagesAvailable = true
		proof.DPDKPluginAvailable = true
	}
	return proof
}

func assertFailedPrerequisite(t *testing.T, results []PrerequisiteResult, name string) {
	t.Helper()
	for _, result := range results {
		if result.Name == name && !result.Passed {
			return
		}
	}
	t.Fatalf("missing failed prerequisite %q in %#v", name, results)
}
