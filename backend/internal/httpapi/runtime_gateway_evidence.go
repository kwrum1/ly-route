package httpapi

import (
	"bytes"
	"encoding/json"
	"sort"
	"time"

	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/vpp"
)

func completeGatewayRuntimeEvidence(result RuntimeApplyResult, now time.Time) error {
	if result.GatewayPlan == nil {
		if len(result.GatewayEvidence) == 0 {
			return nil
		}
		seen := make(map[string]struct{}, len(result.GatewayEvidence))
		for _, evidence := range result.GatewayEvidence {
			if _, duplicate := seen[evidence.Resource]; duplicate {
				return gatewayEvidenceError(apply.GatewayEvidenceIncomplete, evidence.Resource, "resource", "unique", evidence.Resource)
			}
			seen[evidence.Resource] = struct{}{}
		}
		return apply.ValidateGatewayEvidence(result.GatewayEvidence, result.TransactionID, now)
	}
	if err := apply.ValidateGatewayEvidence(result.GatewayEvidence, result.TransactionID, now); err != nil {
		return err
	}
	planned := gatewayPlanResources(*result.GatewayPlan)
	expected := make(map[string]struct{}, len(planned)+len(result.GatewayEvidence))
	for resource := range planned {
		expected[resource] = struct{}{}
	}
	seen := make(map[string]struct{}, len(result.GatewayEvidence))
	for _, evidence := range result.GatewayEvidence {
		if _, duplicate := seen[evidence.Resource]; duplicate {
			return gatewayEvidenceError(apply.GatewayEvidenceIncomplete, evidence.Resource, "resource", "unique", evidence.Resource)
		}
		seen[evidence.Resource] = struct{}{}
		_, resourcePlanned := planned[evidence.Resource]
		if !resourcePlanned {
			if !evidence.Deleted {
				return gatewayEvidenceError(apply.GatewayEvidenceIncomplete, evidence.Resource, "deleted", "true for resource absent from persisted plan", "false")
			}
			if err := proveDeletedGatewayResource(evidence); err != nil {
				return err
			}
		} else {
			if err := proveDesiredGatewayResource(evidence, *result.GatewayPlan); err != nil {
				return err
			}
		}
		expected[evidence.Resource] = struct{}{}
	}
	for resource := range expected {
		if _, present := seen[resource]; !present {
			return gatewayEvidenceError(apply.GatewayEvidenceIncomplete, resource, "resource", "persisted evidence entry", "missing")
		}
	}
	return nil
}

func proveDesiredGatewayResource(evidence apply.GatewayResourceEvidence, desired vpp.Plan) error {
	matched := false
	switch evidence.Resource {
	case "interfaces":
		matched = vpp.InterfaceStatesMatchDesired(evidence.After.Interfaces, desired.Interfaces)
	case "bonds":
		matched = equalGatewaySlice(evidence.After.Bonds, desired.Bonds)
	case "wan-groups":
		matched = equalGatewaySlice(evidence.After.WANGroups, desired.Policy.WANGroups)
	case "routes":
		// VPP readback emits route policies in its own stable order, while the
		// persisted plan keeps product priority order. Priority is a field, not
		// slice order, so a harmless readback reorder must not mark the apply
		// degraded.
		matched = equalGatewaySliceUnordered(evidence.After.RoutePolicies, desired.Policy.RoutePolicies)
	case "acls":
		matched = equalGatewaySlice(evidence.After.ACLs, desired.Policy.SecurityACLs)
	case "qos":
		matched = equalGatewaySlice(evidence.After.QoS, desired.Flow.VPPGroups)
	case "nat44":
		matched = equalGatewaySlice(evidence.After.NAT.StaticMappings, desired.NAT.StaticMappings)
	case "port-maps":
		// VPP lists static mappings in its own order. Port-map identity is its
		// fields, not database insertion order, so a reorder is not degradation.
		matched = equalGatewaySliceUnordered(evidence.After.NAT.PortMappings, desired.NAT.PortMappings)
	}
	if matched {
		owner := vpp.SupplementalOwner(evidence.Resource)
		if owner == vpp.SupplementalInterfaces || owner == vpp.SupplementalRoutes || owner == vpp.SupplementalQoS {
			evidencePlan := desired
			// A committed gateway transaction has already completed the privileged
			// DPDK bind/restart/readback phase. Reconstruct supplemental operations
			// in that prepared state instead of re-triggering the pre-apply gate.
			evidencePlan.DataplanePrepared = true
			if err := vpp.ValidateSupplementalReadback(evidencePlan, owner, evidence.SupplementalReadback, evidence.Readback.Timestamp); err != nil {
				return gatewayEvidenceError(apply.GatewayEvidenceSnapshotMismatch, evidence.Resource, "supplemental_readback", "persisted desired operation payloads", err.Error())
			}
		}
		return nil
	}
	return gatewayEvidenceError(apply.GatewayEvidenceSnapshotMismatch, evidence.Resource, "after", "persisted desired resource payload", "different live payload")
}

func equalGatewaySlice[T any](actual, desired []T) bool {
	if len(actual) == 0 && len(desired) == 0 {
		return true
	}
	actualJSON, actualErr := json.Marshal(actual)
	desiredJSON, desiredErr := json.Marshal(desired)
	return actualErr == nil && desiredErr == nil && bytes.Equal(actualJSON, desiredJSON)
}

func equalGatewaySliceUnordered[T any](actual, desired []T) bool {
	if len(actual) != len(desired) {
		return false
	}
	canonical := func(items []T) ([]string, bool) {
		values := make([]string, 0, len(items))
		for _, item := range items {
			encoded, err := json.Marshal(item)
			if err != nil {
				return nil, false
			}
			values = append(values, string(encoded))
		}
		sort.Strings(values)
		return values, true
	}
	actualValues, actualOK := canonical(actual)
	desiredValues, desiredOK := canonical(desired)
	if !actualOK || !desiredOK {
		return false
	}
	for index := range actualValues {
		if actualValues[index] != desiredValues[index] {
			return false
		}
	}
	return true
}

func proveDeletedGatewayResource(evidence apply.GatewayResourceEvidence) error {
	beforePresent, afterPresent := gatewaySnapshotPayloadPresence(evidence.Resource, evidence.Before), gatewaySnapshotPayloadPresence(evidence.Resource, evidence.After)
	if beforePresent && !afterPresent {
		return nil
	}
	return gatewayEvidenceError(apply.GatewayEvidenceSnapshotMismatch, evidence.Resource, "deleted", "typed prior payload present and after payload absent", "prior or after payload is invalid")
}

func gatewaySnapshotPayloadPresence(resource string, snapshot vpp.Snapshot) bool {
	switch resource {
	case "interfaces":
		return len(snapshot.Interfaces) > 0
	case "bonds":
		return len(snapshot.Bonds) > 0
	case "wan-groups":
		return len(snapshot.WANGroups) > 0
	case "routes":
		return len(snapshot.RoutePolicies) > 0
	case "acls":
		return len(snapshot.ACLs) > 0
	case "qos":
		return len(snapshot.QoS) > 0
	case "nat44":
		return len(snapshot.NAT.StaticMappings) > 0
	case "port-maps":
		return len(snapshot.NAT.PortMappings) > 0
	default:
		return false
	}
}

func gatewayPlanResources(plan vpp.Plan) map[string]struct{} {
	resources := make(map[string]struct{})
	// Resource inventory is evaluated after the privileged dataplane phase. The
	// persisted plan intentionally does not carry that transient flag, so use a
	// prepared copy only for discovering supplemental-only resource ownership.
	prepared := plan
	prepared.DataplanePrepared = true
	hasSupplemental := func(owner vpp.SupplementalOwner) bool {
		return vpp.HasSupplementalOperations(prepared, owner)
	}
	if len(plan.Interfaces) > 0 || hasSupplemental(vpp.SupplementalInterfaces) {
		resources["interfaces"] = struct{}{}
	}
	if len(plan.Bonds) > 0 {
		resources["bonds"] = struct{}{}
	}
	if len(plan.Policy.WANGroups) > 0 {
		resources["wan-groups"] = struct{}{}
	}
	// DNS interception and proxy steering are route-owned VPP operations even
	// when the persisted core route-policy list is empty.
	if len(plan.Policy.RoutePolicies) > 0 || hasSupplemental(vpp.SupplementalRoutes) {
		resources["routes"] = struct{}{}
	}
	if len(plan.Policy.SecurityACLs) > 0 || hasSupplemental(vpp.SupplementalSecurity) {
		resources["acls"] = struct{}{}
	}
	if len(plan.Flow.VPPGroups) > 0 || len(plan.SmartQoSAssignments) > 0 || hasSupplemental(vpp.SupplementalQoS) {
		resources["qos"] = struct{}{}
	}
	if len(plan.NAT.StaticMappings) > 0 {
		resources["nat44"] = struct{}{}
	}
	if len(plan.NAT.PortMappings) > 0 {
		resources["port-maps"] = struct{}{}
	}
	return resources
}

func gatewayEvidenceError(code apply.GatewayEvidenceValidationCode, resource, field, expected, actual string) error {
	return &apply.GatewayEvidenceValidationError{Code: code, Resource: resource, Field: field, Expected: expected, Actual: actual}
}
