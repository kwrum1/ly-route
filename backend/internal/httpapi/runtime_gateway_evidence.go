package httpapi

import (
	"reflect"
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
		matched = reflect.DeepEqual(evidence.After.Interfaces, desired.Interfaces)
	case "bonds":
		matched = reflect.DeepEqual(evidence.After.Bonds, desired.Bonds)
	case "wan-groups":
		matched = reflect.DeepEqual(evidence.After.WANGroups, desired.Policy.WANGroups)
	case "routes":
		matched = reflect.DeepEqual(evidence.After.RoutePolicies, desired.Policy.RoutePolicies)
	case "acls":
		matched = reflect.DeepEqual(evidence.After.ACLs, desired.Policy.SecurityACLs)
	case "qos":
		matched = reflect.DeepEqual(evidence.After.QoS, desired.Flow.VPPGroups)
	case "nat44":
		matched = reflect.DeepEqual(evidence.After.NAT.StaticMappings, desired.NAT.StaticMappings)
	case "port-maps":
		matched = reflect.DeepEqual(evidence.After.NAT.PortMappings, desired.NAT.PortMappings)
	}
	if matched {
		owner := vpp.SupplementalOwner(evidence.Resource)
		if owner == vpp.SupplementalInterfaces || owner == vpp.SupplementalRoutes || owner == vpp.SupplementalQoS {
			if err := vpp.ValidateSupplementalReadback(desired, owner, evidence.SupplementalReadback, evidence.Readback.Timestamp); err != nil {
				return gatewayEvidenceError(apply.GatewayEvidenceSnapshotMismatch, evidence.Resource, "supplemental_readback", "persisted desired operation payloads", err.Error())
			}
		}
		return nil
	}
	return gatewayEvidenceError(apply.GatewayEvidenceSnapshotMismatch, evidence.Resource, "after", "persisted desired resource payload", "different live payload")
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
	if len(plan.Interfaces) > 0 {
		resources["interfaces"] = struct{}{}
	}
	if len(plan.Bonds) > 0 {
		resources["bonds"] = struct{}{}
	}
	if len(plan.Policy.WANGroups) > 0 {
		resources["wan-groups"] = struct{}{}
	}
	if len(plan.Policy.RoutePolicies) > 0 {
		resources["routes"] = struct{}{}
	}
	if len(plan.Policy.SecurityACLs) > 0 {
		resources["acls"] = struct{}{}
	}
	if len(plan.Flow.VPPGroups) > 0 {
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
