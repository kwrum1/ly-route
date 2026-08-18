package vpp

import (
	"fmt"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func GatewayDiffSnapshotRequest(transactionID string, prior, desired Plan) SnapshotRequest {
	request := SnapshotRequest{TransactionID: transactionID, ManagementInterface: desired.NativePath.ManagementInterface, LANVPPInterface: gatewayLANVPPInterface(desired), AllowMissing: true}
	request.LocalDestinations = routePolicyLocalDestinations(desired.AddressAssignments)
	if len(request.LocalDestinations) == 0 {
		request.LocalDestinations = routePolicyLocalDestinations(prior.AddressAssignments)
	}
	if request.LANVPPInterface == "" {
		request.LANVPPInterface = gatewayLANVPPInterface(prior)
	}
	request.NATIngressVPPInterface = gatewayLANVPPInterface(desired)
	if request.NATIngressVPPInterface == "" {
		request.NATIngressVPPInterface = gatewayLANVPPInterface(prior)
	}
	request.Interfaces = interfaceNames(prior.Interfaces)
	request.Candidates.Interfaces = unionCandidates(prior.Interfaces, desired.Interfaces, interfaceContract())
	request.Bonds = bondNames(prior.Bonds)
	request.Candidates.Bonds = unionCandidates(prior.Bonds, desired.Bonds, bondContract())
	request.RoutePolicies = routeIDs(prior.Policy.RoutePolicies)
	request.Candidates.RoutePolicies = unionCandidates(prior.Policy.RoutePolicies, desired.Policy.RoutePolicies, routeContract())
	request.WANGroups = wanGroupIDs(prior.Policy.WANGroups)
	request.Candidates.WANGroups = unionCandidates(prior.Policy.WANGroups, desired.Policy.WANGroups, wanGroupContract())
	if securityGenerationOwnsACLs(desired) {
		// A gateway upgraded from individual security ACL objects to the
		// aggregate security generation can legitimately have no legacy ACL tag
		// left in VPP. Keep the prior plan intact so reconciliation still emits
		// idempotent deletes and rollback can restore it, but do not require the
		// superseded object to exist before the migration can start.
		request.ACLs = nil
		request.Candidates.ACLs = nil
	} else {
		request.ACLs = aclIDs(prior.Policy.SecurityACLs)
		request.Candidates.ACLs = unionCandidates(prior.Policy.SecurityACLs, desired.Policy.SecurityACLs, aclContract())
	}
	request.QoS = qosIDs(prior.Flow.VPPGroups)
	request.Candidates.QoS = unionCandidates(prior.Flow.VPPGroups, desired.Flow.VPPGroups, qosContract())
	request.NATStaticMappings = natStaticIDs(prior.NAT.StaticMappings)
	request.Candidates.NATStaticMappings = unionCandidates(prior.NAT.StaticMappings, desired.NAT.StaticMappings, natStaticContract())
	request.NATPortMappings = portMapIDs(prior.NAT.PortMappings)
	request.Candidates.NATPortMappings = unionCandidates(prior.NAT.PortMappings, desired.NAT.PortMappings, portMapContract())
	request.NATBehavior = desired.NAT.Behavior
	request.VerifyNATReturnGuards = len(request.Candidates.NATStaticMappings)+len(request.Candidates.NATPortMappings) > 0
	if len(request.Interfaces) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityInterfaces)
	}
	if len(request.Bonds) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityBonds)
	}
	if len(request.WANGroups) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityWANGroups)
	}
	if len(request.RoutePolicies) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityRoutePolicies)
	}
	if len(request.ACLs) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityACLs)
	}
	if len(request.QoS) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityQoS)
	}
	if len(request.NATStaticMappings)+len(request.NATPortMappings) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityNAT44)
	}
	return request
}

func securityGenerationOwnsACLs(plan Plan) bool {
	return len(plan.Security.ACLs) > 0
}

func GatewayResourceSnapshotRequest(transactionID, resource string, desired Plan) (SnapshotRequest, error) {
	request := GatewayDiffSnapshotRequest(transactionID, desired, desired)
	capabilityByResource := map[string]SnapshotCapability{
		"interfaces": SnapshotCapabilityInterfaces,
		"bonds":      SnapshotCapabilityBonds,
		"wan-groups": SnapshotCapabilityWANGroups,
		"routes":     SnapshotCapabilityRoutePolicies,
		"acls":       SnapshotCapabilityACLs,
		"qos":        SnapshotCapabilityQoS,
		"nat44":      SnapshotCapabilityNAT44,
		"port-maps":  SnapshotCapabilityNAT44,
	}
	capability, found := capabilityByResource[resource]
	if !found {
		return SnapshotRequest{}, fmt.Errorf("%w: unsupported gateway resource %q", ErrSnapshotIncomplete, resource)
	}
	request.Capabilities = []SnapshotCapability{capability}
	if resource == "nat44" {
		request.NATPortMappings = nil
	}
	if resource == "port-maps" {
		request.NATStaticMappings = nil
	}
	return request, nil
}

func unionCandidates[T any](prior, desired []T, contract resourceContract[T]) []T {
	union := append([]T(nil), prior...)
	seen := make(map[string]struct{}, len(prior)+len(desired))
	for _, item := range prior {
		seen[contract.identity(item)] = struct{}{}
	}
	for _, item := range desired {
		if _, found := seen[contract.identity(item)]; found {
			continue
		}
		seen[contract.identity(item)] = struct{}{}
		union = append(union, item)
	}
	return union
}

func routeIDs(items []trafficpolicy.RoutePolicy) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func wanGroupIDs(items []trafficpolicy.WANGroup) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func aclIDs(items []trafficpolicy.SecurityACL) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func qosIDs(items []flow.VPPObjectGroup) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.Kind)
	}
	return ids
}

func natStaticIDs(items []nat.StaticMapping) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func portMapIDs(items []nat.PortMapping) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}
