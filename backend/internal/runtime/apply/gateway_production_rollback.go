package apply

import (
	"context"
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

type productionRollbackInput struct {
	transactionID string
	desired       vpp.Plan
	prior         vpp.Snapshot
	attempted     vpp.GatewayDiff
}

type productionPriorApply struct {
	transactionID string
	gateway       vpp.Plan
	prior         vpp.Snapshot
	attempted     vpp.GatewayDiff
}

func vppGatewayLANInterface(gateway vpp.Plan) string {
	for _, assignment := range gateway.AddressAssignments {
		if strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			return strings.TrimSpace(assignment.VPPInterface)
		}
	}
	return ""
}

func (adapter *productionGatewayAdapter) rollback(ctx context.Context, input productionRollbackInput) error {
	_, err := adapter.runPrior(ctx, productionPriorApply{transactionID: input.transactionID, gateway: adapter.priorPlan(input), prior: input.prior, attempted: input.attempted})
	return err
}

func (adapter *productionGatewayAdapter) runPrior(ctx context.Context, input productionPriorApply) (vpp.Snapshot, error) {
	transactionID, gateway, prior, attempted := input.transactionID, input.gateway, input.prior, input.attempted
	switch adapter.name {
	case "interfaces":
		result, err := adapter.reconciler.adapter.ApplyInterfaceBond(ctx, vpp.InterfaceBondPlan{TransactionID: transactionID, ManagementInterface: gateway.NativePath.ManagementInterface, Interfaces: gateway.Interfaces}, prior, attempted.Interfaces)
		return result.Readback, err
	case "bonds":
		result, err := adapter.reconciler.adapter.ApplyInterfaceBond(ctx, vpp.InterfaceBondPlan{TransactionID: transactionID, ManagementInterface: gateway.NativePath.ManagementInterface, Bonds: gateway.Bonds}, prior, attempted.Bonds)
		return result.Readback, err
	case "routes":
		result, err := adapter.reconciler.adapter.ApplyRouteWANGroup(ctx, vpp.RouteWANGroupPlan{TransactionID: transactionID, IngressVPPInterface: attempted.Routes.IngressVPPInterface, Routes: gateway.Policy.RoutePolicies, RoutePolicyContext: append([]trafficpolicy.RoutePolicy(nil), gateway.Policy.RoutePolicies...), WANGroupsContext: adapter.routeWANGroupsContext()}, prior, attempted.Routes)
		return result.Readback, err
	case "wan-groups":
		result, err := adapter.reconciler.adapter.ApplyRouteWANGroup(ctx, vpp.RouteWANGroupPlan{TransactionID: transactionID, WANGroups: gateway.Policy.WANGroups}, prior, attempted.WANGroups)
		return result.Readback, err
	case "acls":
		deleteACLs := make([]string, 0, len(attempted.ACLs.ACLs))
		for _, acl := range attempted.ACLs.ACLs {
			deleteACLs = append(deleteACLs, acl.ID)
		}
		if len(deleteACLs) > 0 {
			if _, err := adapter.reconciler.adapter.ApplyACLQoS(ctx, vpp.ACLQoSPlan{TransactionID: transactionID, IngressVPPInterface: vppGatewayLANInterface(gateway), DeleteACLs: deleteACLs}, prior); err != nil {
				return vpp.Snapshot{}, err
			}
		}
		result, err := adapter.reconciler.adapter.ApplyACLQoS(ctx, vpp.ACLQoSPlan{TransactionID: transactionID, IngressVPPInterface: vppGatewayLANInterface(gateway), ACLs: gateway.Policy.SecurityACLs}, prior, attempted.ACLs)
		return result.Readback, err
	case "qos":
		result, err := adapter.reconciler.adapter.ApplyACLQoS(ctx, vpp.ACLQoSPlan{TransactionID: transactionID, IngressVPPInterface: vppGatewayLANInterface(gateway), QoS: gateway.Flow.VPPGroups}, prior, attempted.QoS)
		return result.Readback, err
	case "nat44":
		result, err := adapter.reconciler.adapter.ApplyNAT44(ctx, vpp.NAT44Plan{TransactionID: transactionID, Behavior: gateway.NAT.Behavior, IngressVPPInterface: vppGatewayLANInterface(gateway), StaticMappings: gateway.NAT.StaticMappings}, prior, attempted.NAT44)
		return result.Readback, err
	case "port-maps":
		result, err := adapter.reconciler.adapter.ApplyNAT44(ctx, vpp.NAT44Plan{TransactionID: transactionID, Behavior: gateway.NAT.Behavior, IngressVPPInterface: vppGatewayLANInterface(gateway), PortMappings: gateway.NAT.PortMappings}, prior, attempted.PortMaps)
		return result.Readback, err
	default:
		return vpp.Snapshot{}, fmt.Errorf("unsupported production gateway resource %q", adapter.name)
	}
}

func (adapter *productionGatewayAdapter) priorPlan(input productionRollbackInput) vpp.Plan {
	gateway, prior := input.desired, input.prior
	gateway.RequestID = input.transactionID
	switch adapter.name {
	case "interfaces":
		gateway.Interfaces, gateway.Bonds = prior.Interfaces, nil
	case "bonds":
		gateway.Interfaces, gateway.Bonds = nil, prior.Bonds
	case "routes":
		gateway.Policy.RoutePolicies, gateway.Policy.WANGroups = prior.RoutePolicies, nil
	case "wan-groups":
		gateway.Policy.RoutePolicies, gateway.Policy.WANGroups = nil, prior.WANGroups
	case "acls":
		gateway.Policy.SecurityACLs, gateway.Flow.VPPGroups = prior.ACLs, nil
	case "qos":
		gateway.Policy.SecurityACLs, gateway.Flow.VPPGroups = nil, prior.QoS
	case "nat44":
		gateway.NAT.StaticMappings, gateway.NAT.PortMappings = prior.NAT.StaticMappings, nil
	case "port-maps":
		gateway.NAT.StaticMappings, gateway.NAT.PortMappings = nil, prior.NAT.PortMappings
	}
	return gateway
}
