package vpp

import (
	"errors"
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

var ErrGatewayLiveMismatch = errors.New("gateway live state does not match prior plan")

type GatewayDiff struct {
	Interfaces InterfaceBondPlan
	Bonds      InterfaceBondPlan
	WANGroups  RouteWANGroupPlan
	Routes     RouteWANGroupPlan
	ACLs       ACLQoSPlan
	QoS        ACLQoSPlan
	NAT44      NAT44Plan
	PortMaps   NAT44Plan
}

type GatewayReconciliationInput struct {
	TransactionID       string
	Prior               Plan
	Desired             Plan
	Live                Snapshot
	RepairVerifiedDrift bool
}

type resourceStates[T any] struct {
	prior   []T
	desired []T
	live    []T
}

type resourceContract[T any] struct {
	kind     string
	identity func(T) string
	equal    func(T, T) bool
	// liveMatchesDesired permits a verified live snapshot to contain
	// runtime-owned state that is intentionally absent from the persisted
	// static plan.  It is deliberately separate from equal: prior/desired
	// comparisons must remain exact, while live reconciliation may account for
	// state owned by another runtime component.
	liveMatchesDesired func(observed, wanted T) bool
	repairInPlace      func(observed, wanted T) (T, bool)
}

type resourceDiff[T any] struct {
	apply  []T
	delete []string
}

func ReconcileGatewayPlan(input GatewayReconciliationInput) (GatewayDiff, error) {
	transactionID, prior, desired, live := input.TransactionID, input.Prior, input.Desired, input.Live
	routeIngressVPPInterface := gatewayLANVPPInterface(desired)
	if routeIngressVPPInterface == "" {
		// A cleanup/recovery transaction can be built from a desired snapshot
		// that predates logical LAN assignments. Reuse the prior configured
		// LAN before allowing route deletion to emit an unresolved ABF attach.
		routeIngressVPPInterface = gatewayLANVPPInterface(prior)
	}
	interfaces, err := diffResources(resourceStates[InterfaceState]{prior: prior.Interfaces, desired: desired.Interfaces, live: live.Interfaces}, interfaceContract(), input.RepairVerifiedDrift)
	if err != nil {
		return GatewayDiff{}, err
	}
	bonds, err := diffResources(resourceStates[BondState]{prior: prior.Bonds, desired: desired.Bonds, live: live.Bonds}, bondContract(), input.RepairVerifiedDrift)
	if err != nil {
		return GatewayDiff{}, err
	}
	wanGroups, err := diffResources(resourceStates[trafficpolicy.WANGroup]{prior: prior.Policy.WANGroups, desired: desired.Policy.WANGroups, live: live.WANGroups}, wanGroupContract(), input.RepairVerifiedDrift)
	if err != nil {
		return GatewayDiff{}, err
	}
	routes, err := diffResources(resourceStates[trafficpolicy.RoutePolicy]{prior: prior.Policy.RoutePolicies, desired: desired.Policy.RoutePolicies, live: live.RoutePolicies}, routeContract(), input.RepairVerifiedDrift)
	if err != nil {
		return GatewayDiff{}, err
	}
	acls, err := diffResources(resourceStates[trafficpolicy.SecurityACL]{prior: prior.Policy.SecurityACLs, desired: desired.Policy.SecurityACLs, live: live.ACLs}, aclContract(), input.RepairVerifiedDrift)
	if err != nil {
		return GatewayDiff{}, err
	}
	qos, err := diffResources(resourceStates[flow.VPPObjectGroup]{prior: prior.Flow.VPPGroups, desired: desired.Flow.VPPGroups, live: live.QoS}, qosContract(), input.RepairVerifiedDrift)
	if err != nil {
		return GatewayDiff{}, err
	}
	staticMappings, err := diffResources(resourceStates[nat.StaticMapping]{prior: prior.NAT.StaticMappings, desired: desired.NAT.StaticMappings, live: live.NAT.StaticMappings}, natStaticContract(), input.RepairVerifiedDrift)
	if err != nil {
		return GatewayDiff{}, err
	}
	portMappings, err := diffResources(resourceStates[nat.PortMapping]{prior: prior.NAT.PortMappings, desired: desired.NAT.PortMappings, live: live.NAT.PortMappings}, portMapContract(), input.RepairVerifiedDrift)
	if err != nil {
		return GatewayDiff{}, err
	}
	return GatewayDiff{
		Interfaces: InterfaceBondPlan{TransactionID: transactionID, ManagementInterface: desired.NativePath.ManagementInterface, Interfaces: interfaces.apply, DeleteInterfaces: interfaces.delete},
		Bonds:      InterfaceBondPlan{TransactionID: transactionID, ManagementInterface: desired.NativePath.ManagementInterface, Bonds: bonds.apply, DeleteBonds: bonds.delete},
		WANGroups:  RouteWANGroupPlan{TransactionID: transactionID, WANGroups: wanGroups.apply, DeleteWANGroups: wanGroups.delete},
		Routes:     RouteWANGroupPlan{TransactionID: transactionID, IngressVPPInterface: routeIngressVPPInterface, Routes: routes.apply, RoutePolicyContext: append([]trafficpolicy.RoutePolicy(nil), desired.Policy.RoutePolicies...), DeleteRoutes: routes.delete},
		ACLs:       ACLQoSPlan{TransactionID: transactionID, ACLs: acls.apply, DeleteACLs: acls.delete},
		QoS:        ACLQoSPlan{TransactionID: transactionID, QoS: qos.apply, DeleteQoS: qos.delete},
		NAT44:      NAT44Plan{TransactionID: transactionID, StaticMappings: staticMappings.apply, DeleteStaticMappings: staticMappings.delete},
		PortMaps:   NAT44Plan{TransactionID: transactionID, PortMappings: portMappings.apply, DeletePortMappings: portMappings.delete},
	}, nil
}

func gatewayLANVPPInterface(plan Plan) string {
	for _, assignment := range plan.AddressAssignments {
		if strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			return strings.TrimSpace(assignment.VPPInterface)
		}
	}
	return ""
}

func diffResources[T any](states resourceStates[T], contract resourceContract[T], repairVerifiedDrift bool) (resourceDiff[T], error) {
	prior, err := indexResources(states.prior, contract)
	if err != nil {
		return resourceDiff[T]{}, err
	}
	desired, err := indexResources(states.desired, contract)
	if err != nil {
		return resourceDiff[T]{}, err
	}
	live, err := indexResources(states.live, contract)
	if err != nil {
		return resourceDiff[T]{}, err
	}
	if repairVerifiedDrift {
		return diffVerifiedLiveResources(states, desired, live, contract), nil
	}
	if len(live) != len(prior) {
		return resourceDiff[T]{}, fmt.Errorf("%w: %s identity count is %d, want %d", ErrGatewayLiveMismatch, contract.kind, len(live), len(prior))
	}
	for id, planned := range prior {
		observed, found := live[id]
		if !found || !contract.equal(planned, observed) {
			return resourceDiff[T]{}, fmt.Errorf("%w: %s %q is missing or mismatched", ErrGatewayLiveMismatch, contract.kind, id)
		}
	}
	diff := resourceDiff[T]{}
	for _, planned := range states.prior {
		id := contract.identity(planned)
		wanted, found := desired[id]
		if !found {
			diff.delete = append(diff.delete, id)
			continue
		}
		if !contract.equal(planned, wanted) {
			diff.delete = append(diff.delete, id)
			diff.apply = append(diff.apply, wanted)
		}
	}
	for _, wanted := range states.desired {
		if _, found := prior[contract.identity(wanted)]; !found {
			diff.apply = append(diff.apply, wanted)
		}
	}
	return diff, nil
}

// diffVerifiedLiveResources is only used after the production snapshot adapter
// has completed all typed readbacks. At that point a missing or mismatched
// known resource is observed drift, not an incomplete read. Diff from the live
// generation so the transaction can repair it while preserving the verified
// live snapshot for rollback.
func diffVerifiedLiveResources[T any](states resourceStates[T], desired, live map[string]T, contract resourceContract[T]) resourceDiff[T] {
	diff := resourceDiff[T]{}
	for _, observed := range states.live {
		id := contract.identity(observed)
		wanted, found := desired[id]
		if !found {
			diff.delete = append(diff.delete, id)
			continue
		}
		matches := contract.equal(observed, wanted)
		if !matches && contract.liveMatchesDesired != nil {
			matches = contract.liveMatchesDesired(observed, wanted)
		}
		if !matches {
			if contract.repairInPlace != nil {
				if repair, ok := contract.repairInPlace(observed, wanted); ok {
					diff.apply = append(diff.apply, repair)
					continue
				}
			}
			diff.delete = append(diff.delete, id)
			diff.apply = append(diff.apply, wanted)
		}
	}
	for _, wanted := range states.desired {
		if _, found := live[contract.identity(wanted)]; !found {
			diff.apply = append(diff.apply, wanted)
		}
	}
	return diff
}

func indexResources[T any](resources []T, contract resourceContract[T]) (map[string]T, error) {
	indexed := make(map[string]T, len(resources))
	for _, resource := range resources {
		id := strings.TrimSpace(contract.identity(resource))
		if id == "" {
			return nil, fmt.Errorf("%w: %s identity is empty", ErrGatewayLiveMismatch, contract.kind)
		}
		if _, duplicate := indexed[id]; duplicate {
			return nil, fmt.Errorf("%w: %s identity %q is duplicated", ErrGatewayLiveMismatch, contract.kind, id)
		}
		indexed[id] = resource
	}
	return indexed, nil
}
