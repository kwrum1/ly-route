package httpapi

import (
	"time"

	"ly-route/backend/internal/runtime/apply"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func applyCapabilityFailures(components []RuntimeComponentState, failures []apply.CapabilityFailureEvidence, transactionID string, now time.Time) []RuntimeComponentState {
	byName := make(map[string]apply.CapabilityFailureEvidence, len(failures))
	for _, failure := range failures {
		name := failure.Capability
		switch serviceRuntime.ServiceName(failure.Capability) {
		case serviceRuntime.Nftables:
			name = "nftables_tproxy"
		case serviceRuntime.LinuxRouting:
			name = "linux_routing"
		}
		byName[name] = failure
	}
	for index := range components {
		failure, exists := byName[components[index].Name]
		if !exists {
			continue
		}
		components[index].State = "degraded"
		components[index].Available = false
		components[index].Fresh = false
		components[index].Reason = failure.Reason
		components[index].ApplyReceipt = apply.ApplyReceipt{
			TransactionID: transactionID,
			Capability:    components[index].Name,
			Status:        apply.ReceiptFailed,
			AppliedAt:     now,
			Cause:         failure.Reason,
		}
	}
	return components
}

func serviceFailureEvidence(failures []serviceRuntime.CapabilityFailure) []apply.CapabilityFailureEvidence {
	evidence := make([]apply.CapabilityFailureEvidence, 0, len(failures))
	for _, failure := range failures {
		evidence = append(evidence, apply.CapabilityFailureEvidence{Capability: string(failure.Service), Reason: failure.Error()})
	}
	return evidence
}

func markComponentDegraded(components []RuntimeComponentState, name, reason string) []RuntimeComponentState {
	for index := range components {
		if components[index].Name != name {
			continue
		}
		components[index].State = "degraded"
		components[index].Available = false
		components[index].Fresh = false
		components[index].Reason = reason
		return components
	}
	return append(components, RuntimeComponentState{Name: name, State: "degraded", Reason: reason})
}
