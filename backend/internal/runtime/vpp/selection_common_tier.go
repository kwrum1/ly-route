package vpp

import (
	"sort"
	"strings"
	"time"
)

func selectCommonDataplanePath(request NativePathRequest) (NativePath, error) {
	management := strings.TrimSpace(request.ManagementInterface)
	results := append([]PrerequisiteResult(nil), request.ReportPrerequisites...)
	results = append(results,
		prerequisite("management_identified", "", management != "", "management interface is required"),
		prerequisite("data_assignment_present", "", len(request.Assignments) > 0, "at least one explicit data interface assignment is required"),
	)

	assignments := append([]NativeAssignment(nil), request.Assignments...)
	sort.Slice(assignments, func(i, j int) bool {
		return strings.TrimSpace(assignments[i].LinuxInterface) < strings.TrimSpace(assignments[j].LinuxInterface)
	})

	type eligibleByTier map[DataplaneTier][]CapabilityProof
	eligible := make(map[string]eligibleByTier, len(assignments))
	evaluations := make([]CandidateEvaluation, 0)
	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		name := strings.TrimSpace(assignment.LinuxInterface)
		_, duplicate := seen[name]
		seen[name] = struct{}{}
		results = append(results,
			prerequisite("interface_assigned", name, name != "", "data interface name is required"),
			prerequisite("interface_name_safe", name, interfaceNameSafe(name), "data interface name contains unsupported characters"),
			prerequisite("assignment_unique", name, !duplicate, "data interface assignment is duplicated"),
			prerequisite("explicit_assignment", name, assignment.Explicit, "automatic interface assignment is forbidden"),
			prerequisite("management_excluded", name, management != "" && (request.ManagementShared || name != management), "management interface is permanently excluded"),
		)
		eligible[name] = eligibleByTier{}
		for _, proof := range assignment.Candidates {
			tier := proofTier(proof)
			reasons := candidateRejectionReasons(proof, request.Now, tier, request.RequireSmartQoS)
			evaluation := CandidateEvaluation{LinuxInterface: name, Tier: tier, Hook: proof.Hook, Mode: proof.Mode, PerformanceScore: proof.PerformanceScore, Eligible: len(reasons) == 0, Reasons: reasons}
			evaluations = append(evaluations, evaluation)
			if evaluation.Eligible {
				eligible[name][tier] = append(eligible[name][tier], proof)
			}
		}
	}

	for _, result := range results {
		if !result.Passed {
			return NativePath{}, &DataplaneLockedError{Prerequisites: results, Candidates: evaluations}
		}
	}

	selectedTier := DataplaneTier("")
	for _, tier := range []DataplaneTier{DataplaneTierNative, DataplaneTierDPDK} {
		allEligible := true
		for _, assignment := range assignments {
			if len(eligible[strings.TrimSpace(assignment.LinuxInterface)][tier]) == 0 {
				allEligible = false
				break
			}
		}
		if allEligible {
			selectedTier = tier
			break
		}
	}
	if selectedTier == "" {
		reason := "no single high-performance tier is eligible on every active data interface"
		if request.RequireSmartQoS {
			reason = "smart QoS requires the production VPP smart-QoS plugin on every active data interface"
		}
		results = append(results, prerequisite("common_dataplane_tier", "", false, reason))
		return NativePath{}, &DataplaneLockedError{Prerequisites: results, Candidates: evaluations}
	}

	attachments := make([]NativeAttachment, 0, len(assignments))
	for _, assignment := range assignments {
		name := strings.TrimSpace(assignment.LinuxInterface)
		candidates := eligible[name][selectedTier]
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].PerformanceScore != candidates[j].PerformanceScore {
				return candidates[i].PerformanceScore > candidates[j].PerformanceScore
			}
			if candidates[i].Hook != candidates[j].Hook {
				return candidates[i].Hook < candidates[j].Hook
			}
			return candidates[i].Mode < candidates[j].Mode
		})
		selected := candidates[0]
		attachments = append(attachments, proveNativeAttachment(NativeAttachment{
			LinuxInterface: name,
			VPPInterface:   "lyroute-" + name,
			Tier:           selectedTier,
			Hook:           selected.Hook,
			Mode:           selected.Mode,
			PCIAddress:     strings.TrimSpace(selected.PCIAddress),
			KernelDriver:   strings.TrimSpace(selected.KernelDriver),
			IOMMUGroup:     strings.TrimSpace(selected.IOMMUGroup),
		}))
	}
	results = append(results, prerequisite("common_dataplane_tier", "", true, ""))
	if request.RequireSmartQoS {
		results = append(results, prerequisite("smart_qos_scheduler", "", true, ""))
	}
	return NativePath{Tier: selectedTier, SmartQoS: request.RequireSmartQoS, Attachments: attachments, Prerequisites: results, Candidates: evaluations}, nil
}

func proofTier(proof CapabilityProof) DataplaneTier {
	if proof.Tier != "" {
		return proof.Tier
	}
	if proof.Hook == NativeHookDPDK {
		return DataplaneTierDPDK
	}
	return DataplaneTierNative
}

func candidateRejectionReasons(proof CapabilityProof, now time.Time, tier DataplaneTier, requireSmartQoS bool) []string {
	reasons := make([]string, 0)
	runtimeProof := (proof.Source == ProofSourceRuntimeProbe || proof.Source == ProofSourceActiveRuntimeReadback) && proof.RuntimeVerified && !proof.ObservedAt.IsZero()
	if !runtimeProof {
		reasons = append(reasons, "runtime capability proof is required")
	}
	if now.IsZero() || !runtimeProof || proof.ValidUntil.Before(now) || proof.ObservedAt.After(now) {
		reasons = append(reasons, "runtime capability proof is missing or stale")
	}
	lifetime := proof.ValidUntil.Sub(proof.ObservedAt)
	if !runtimeProof || lifetime <= 0 || lifetime > 10*time.Minute {
		reasons = append(reasons, "runtime capability proof validity exceeds the allowed window")
	}
	if !proof.HighPerformance {
		reasons = append(reasons, "proof does not establish high-performance mode")
	}
	switch tier {
	case DataplaneTierNative:
		if !approvedNativeMode(proof.Hook, proof.Mode) || !proof.Native {
			reasons = append(reasons, "candidate is not an approved VPP-native high-performance path")
		}
	case DataplaneTierDPDK:
		if proof.Hook != NativeHookDPDK || proof.Mode != NativeModeDPDKVFIO || proof.Native {
			reasons = append(reasons, "candidate is not an approved VPP DPDK VFIO path")
		}
		if !pciAddressSafe(proof.PCIAddress) {
			reasons = append(reasons, "DPDK candidate has no safe PCI address")
		}
		if strings.TrimSpace(proof.KernelDriver) == "" {
			reasons = append(reasons, "DPDK candidate has no current kernel driver")
		}
		if !decimalIdentifierSafe(proof.IOMMUGroup) || !proof.IOMMUProtected {
			reasons = append(reasons, "DPDK candidate is not protected by an IOMMU group")
		}
		if !proof.VFIOAvailable {
			reasons = append(reasons, "vfio-pci is unavailable")
		}
		if !proof.HugepagesAvailable {
			reasons = append(reasons, "VPP hugepages are unavailable")
		}
		if !proof.DPDKPluginAvailable {
			reasons = append(reasons, "VPP DPDK plugin is unavailable")
		}
	default:
		reasons = append(reasons, "unknown dataplane tier")
	}
	if requireSmartQoS && !proof.SmartQoSPluginAvailable {
		reasons = append(reasons, "production VPP smart-QoS plugin is unavailable")
	}
	return reasons
}

func pciAddressSafe(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 12 || value[4] != ':' || value[7] != ':' || value[10] != '.' {
		return false
	}
	for index, char := range value {
		if index == 4 || index == 7 || index == 10 {
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return false
		}
	}
	return true
}

func decimalIdentifierSafe(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
