package vpp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

type NativeHook string

const (
	NativeHookAFXDP NativeHook = "af_xdp"
	NativeHookRDMA  NativeHook = "rdma"
	NativeHookDPDK  NativeHook = "dpdk"
)

type NativeMode string

const (
	NativeModeZeroCopy NativeMode = "zero_copy"
	NativeModeRDMADV   NativeMode = "rdma_dv"
	NativeModeCopy     NativeMode = "copy"
	NativeModeDPDKVFIO NativeMode = "vfio_pci"
)

type DataplaneTier string

const (
	DataplaneTierNative DataplaneTier = "vpp_native"
	DataplaneTierDPDK   DataplaneTier = "vpp_dpdk"
)

func approvedNativeMode(hook NativeHook, mode NativeMode) bool {
	return hook == NativeHookAFXDP && mode == NativeModeZeroCopy || hook == NativeHookRDMA && mode == NativeModeRDMADV
}

func proveNativeAttachment(attachment NativeAttachment) NativeAttachment {
	attachment.capabilityFingerprint = nativeAttachmentFingerprint(attachment)
	return attachment
}

func nativeAttachmentFingerprint(attachment NativeAttachment) string {
	// Hook and mode already distinguish native from DPDK attachments. Keep the
	// persisted fingerprint stable for pre-tier native receipts during upgrade.
	digest := sha256.Sum256([]byte(strings.Join([]string{attachment.LinuxInterface, attachment.VPPInterface, string(attachment.Hook), string(attachment.Mode)}, "\x00")))
	return fmt.Sprintf("%x", digest)
}

type ProofSource string

const (
	ProofSourceRuntimeProbe          ProofSource = "runtime_probe"
	ProofSourceActiveRuntimeReadback ProofSource = "active_runtime_readback"
)

type CapabilityProof struct {
	Tier                    DataplaneTier `json:"tier,omitempty"`
	Hook                    NativeHook    `json:"hook"`
	Mode                    NativeMode    `json:"mode"`
	Source                  ProofSource   `json:"source"`
	RuntimeVerified         bool          `json:"runtime_verified"`
	Native                  bool          `json:"native"`
	HighPerformance         bool          `json:"high_performance"`
	ObservedAt              time.Time     `json:"observed_at"`
	ValidUntil              time.Time     `json:"valid_until"`
	PerformanceScore        float64       `json:"performance_score,omitempty"`
	PCIAddress              string        `json:"pci_address,omitempty"`
	KernelDriver            string        `json:"kernel_driver,omitempty"`
	IOMMUGroup              string        `json:"iommu_group,omitempty"`
	IOMMUProtected          bool          `json:"iommu_protected,omitempty"`
	VFIOAvailable           bool          `json:"vfio_available,omitempty"`
	HugepagesAvailable      bool          `json:"hugepages_available,omitempty"`
	DPDKPluginAvailable     bool          `json:"dpdk_plugin_available,omitempty"`
	HQoSAvailable           bool          `json:"hqos_available,omitempty"`
	SmartQoSPluginAvailable bool          `json:"smart_qos_plugin_available,omitempty"`
}

type NativeAssignment struct {
	LinuxInterface string            `json:"linux_interface"`
	Explicit       bool              `json:"explicit"`
	Proof          CapabilityProof   `json:"proof"`
	Candidates     []CapabilityProof `json:"candidates,omitempty"`
}

type NativePathRequest struct {
	ManagementInterface string               `json:"management_interface"`
	ManagementShared    bool                 `json:"management_shared,omitempty"`
	RequireSmartQoS     bool                 `json:"require_smart_qos,omitempty"`
	Assignments         []NativeAssignment   `json:"assignments"`
	ReportPrerequisites []PrerequisiteResult `json:"report_prerequisites,omitempty"`
	Now                 time.Time            `json:"-"`
}

type NativeAttachment struct {
	LinuxInterface        string        `json:"linux_interface"`
	VPPInterface          string        `json:"vpp_interface"`
	Tier                  DataplaneTier `json:"tier,omitempty"`
	Hook                  NativeHook    `json:"hook"`
	Mode                  NativeMode    `json:"mode"`
	PCIAddress            string        `json:"pci_address,omitempty"`
	KernelDriver          string        `json:"kernel_driver,omitempty"`
	IOMMUGroup            string        `json:"iommu_group,omitempty"`
	capabilityFingerprint string
}

type PrerequisiteResult struct {
	Name      string `json:"name"`
	Interface string `json:"interface,omitempty"`
	Passed    bool   `json:"passed"`
	Reason    string `json:"reason,omitempty"`
}

type NativePath struct {
	Tier          DataplaneTier         `json:"tier,omitempty"`
	SmartQoS      bool                  `json:"smart_qos,omitempty"`
	Attachments   []NativeAttachment    `json:"attachments"`
	Prerequisites []PrerequisiteResult  `json:"prerequisites"`
	Candidates    []CandidateEvaluation `json:"candidates,omitempty"`
}

type CandidateEvaluation struct {
	LinuxInterface   string        `json:"linux_interface"`
	Tier             DataplaneTier `json:"tier"`
	Hook             NativeHook    `json:"hook"`
	Mode             NativeMode    `json:"mode"`
	PerformanceScore float64       `json:"performance_score,omitempty"`
	Eligible         bool          `json:"eligible"`
	Reasons          []string      `json:"reasons,omitempty"`
}

type CapabilityReport struct {
	ManagementInterface string           `json:"management_interface"`
	Proofs              []InterfaceProof `json:"proofs"`
}

type InterfaceProof struct {
	LinuxInterface string            `json:"linux_interface"`
	Proof          CapabilityProof   `json:"proof"`
	Candidates     []CapabilityProof `json:"candidates,omitempty"`
}

type DataplaneLockedError struct {
	Prerequisites []PrerequisiteResult  `json:"prerequisites"`
	Candidates    []CandidateEvaluation `json:"candidates,omitempty"`
}

func (err *DataplaneLockedError) Error() string {
	failed := make([]string, 0, len(err.Prerequisites))
	for _, result := range err.Prerequisites {
		if !result.Passed {
			failed = append(failed, result.Name)
		}
	}
	return fmt.Sprintf("dataplane_locked: failed prerequisites: %s", strings.Join(failed, ", "))
}

func (*DataplaneLockedError) Code() string { return "dataplane_locked" }

func SelectNativePath(request NativePathRequest) (NativePath, error) {
	for _, assignment := range request.Assignments {
		if len(assignment.Candidates) > 0 {
			return selectCommonDataplanePath(request)
		}
	}
	management := strings.TrimSpace(request.ManagementInterface)
	results := append([]PrerequisiteResult(nil), request.ReportPrerequisites...)
	if request.RequireSmartQoS {
		results = append(results, prerequisite("smart_qos_candidate_report", "", false, "smart QoS requires a multi-candidate production VPP smart-QoS plugin capability proof"))
		return NativePath{}, &DataplaneLockedError{Prerequisites: results}
	}
	results = append(results,
		prerequisite("management_identified", "", management != "", "management interface is required"),
		prerequisite("data_assignment_present", "", len(request.Assignments) > 0, "at least one explicit data interface assignment is required"),
	)
	assignments := append([]NativeAssignment(nil), request.Assignments...)
	sort.Slice(assignments, func(left, right int) bool {
		return strings.TrimSpace(assignments[left].LinuxInterface) < strings.TrimSpace(assignments[right].LinuxInterface)
	})
	attachments := make([]NativeAttachment, 0, len(assignments))
	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		name := strings.TrimSpace(assignment.LinuxInterface)
		proof := assignment.Proof
		_, duplicate := seen[name]
		seen[name] = struct{}{}
		runtimeProof := proof.Source == ProofSourceRuntimeProbe && proof.RuntimeVerified && !proof.ObservedAt.IsZero()
		freshProof := runtimeProof && !request.Now.IsZero() && !proof.ValidUntil.Before(request.Now) && !proof.ObservedAt.After(request.Now)
		proofLifetime := proof.ValidUntil.Sub(proof.ObservedAt)
		shortLivedProof := runtimeProof && proofLifetime > 0 && proofLifetime <= 10*time.Minute
		approvedMode := approvedNativeMode(proof.Hook, proof.Mode)
		results = append(results,
			prerequisite("interface_assigned", name, name != "", "data interface name is required"),
			prerequisite("interface_name_safe", name, interfaceNameSafe(name), "data interface name contains unsupported characters"),
			prerequisite("assignment_unique", name, !duplicate, "data interface assignment is duplicated"),
			prerequisite("explicit_assignment", name, assignment.Explicit, "automatic interface assignment is forbidden"),
			prerequisite("management_excluded", name, management != "" && (request.ManagementShared || name != management), "management interface is permanently excluded"),
			prerequisite("runtime_capability_proof", name, runtimeProof, "runtime capability proof is required"),
			prerequisite("fresh_runtime_proof", name, freshProof, "runtime capability proof is missing or stale"),
			prerequisite("short_lived_runtime_proof", name, shortLivedProof, "runtime capability proof validity exceeds the allowed window"),
			prerequisite("approved_native_mode", name, approvedMode, "hook and mode are not an approved VPP-native high-performance path"),
			prerequisite("native_hook", name, proof.Native, "proof does not identify a native hook"),
			prerequisite("high_performance", name, proof.HighPerformance, "proof does not establish high-performance mode"),
		)
		// Preserve the legacy attachment payload shape until a multi-candidate
		// report opts this interface into explicit tier negotiation.
		attachments = append(attachments, proveNativeAttachment(NativeAttachment{LinuxInterface: name, VPPInterface: "lyroute-" + name, Hook: proof.Hook, Mode: proof.Mode}))
	}
	for _, result := range results {
		if !result.Passed {
			return NativePath{}, &DataplaneLockedError{Prerequisites: results}
		}
	}
	return NativePath{Tier: DataplaneTierNative, Attachments: attachments, Prerequisites: results}, nil
}

func LoadNativePathRequest(path, management string, interfaces []string, now time.Time) NativePathRequest {
	return LoadNativePathRequestWithSharedManagement(path, management, interfaces, now, false)
}

func LoadNativePathRequestWithSharedManagement(path, management string, interfaces []string, now time.Time, managementShared bool) NativePathRequest {
	proofs := map[string]CapabilityProof{}
	candidates := map[string][]CapabilityProof{}
	proofCounts := map[string]int{}
	reportPrerequisites := []PrerequisiteResult{}
	file, err := os.Open(path)
	if err != nil {
		reportPrerequisites = append(reportPrerequisites, prerequisite("capability_report_loaded", "", false, "runtime capability report is unavailable"))
	} else {
		defer file.Close()
		info, statErr := file.Stat()
		securePermissions := statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o022 == 0
		reportPrerequisites = append(reportPrerequisites, prerequisite("capability_report_permissions", "", securePermissions, "runtime capability report must not be group or world writable"))
		var report CapabilityReport
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&report)
		if decodeErr == nil {
			var trailing json.RawMessage
			if trailingErr := decoder.Decode(&trailing); trailingErr != io.EOF {
				decodeErr = fmt.Errorf("capability report has trailing data")
			}
		}
		reportPrerequisites = append(reportPrerequisites, prerequisite("capability_report_loaded", "", decodeErr == nil, "runtime capability report is malformed"))
		managementMatches := decodeErr == nil && securePermissions && strings.TrimSpace(report.ManagementInterface) == strings.TrimSpace(management)
		reportPrerequisites = append(reportPrerequisites, prerequisite("capability_report_management_matches", "", managementMatches, "runtime capability report belongs to a different management interface"))
		if managementMatches {
			for _, item := range report.Proofs {
				name := strings.TrimSpace(item.LinuxInterface)
				proofCounts[name]++
				if proofCounts[name] > 1 {
					continue
				}
				if len(item.Candidates) > 0 {
					candidates[name] = append([]CapabilityProof(nil), item.Candidates...)
				} else {
					proofs[name] = item.Proof
				}
			}
		}
	}
	assignments := make([]NativeAssignment, 0, len(interfaces))
	for _, interfaceName := range interfaces {
		name := strings.TrimSpace(interfaceName)
		if name == "" {
			continue
		}
		reportPrerequisites = append(reportPrerequisites, prerequisite("capability_report_interface_unique", name, proofCounts[name] <= 1, "runtime capability report contains duplicate interface entries"))
		assignments = append(assignments, NativeAssignment{LinuxInterface: name, Explicit: true, Proof: proofs[name], Candidates: candidates[name]})
	}
	return NativePathRequest{ManagementInterface: strings.TrimSpace(management), ManagementShared: managementShared, Assignments: assignments, ReportPrerequisites: reportPrerequisites, Now: now}
}

func interfaceNameSafe(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func prerequisite(name, interfaceName string, passed bool, reason string) PrerequisiteResult {
	if passed {
		reason = ""
	}
	return PrerequisiteResult{Name: name, Interface: interfaceName, Passed: passed, Reason: reason}
}
