package apply

import (
	"context"
	"fmt"

	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func (adapter *productionGatewayAdapter) deleteDiff(ctx context.Context, diff vpp.GatewayDiff, prior vpp.Snapshot) (vpp.Snapshot, error) {
	switch adapter.name {
	case "interfaces":
		plan := diff.Interfaces
		plan.Interfaces = nil
		result, err := adapter.reconciler.adapter.ApplyInterfaceBond(ctx, plan, prior)
		return result.Readback, err
	case "bonds":
		plan := diff.Bonds
		plan.Bonds = nil
		result, err := adapter.reconciler.adapter.ApplyInterfaceBond(ctx, plan, prior)
		return result.Readback, err
	case "wan-groups":
		plan := diff.WANGroups
		plan.WANGroups = nil
		result, err := adapter.reconciler.adapter.ApplyRouteWANGroup(ctx, plan, prior)
		return result.Readback, err
	case "routes":
		plan := diff.Routes
		plan.Routes = nil
		result, err := adapter.reconciler.adapter.ApplyRouteWANGroup(ctx, plan, prior)
		return result.Readback, err
	case "acls":
		plan := diff.ACLs
		plan.ACLs = nil
		result, err := adapter.reconciler.adapter.ApplyACLQoS(ctx, plan, prior)
		return result.Readback, err
	case "qos":
		plan := diff.QoS
		plan.QoS = nil
		result, err := adapter.reconciler.adapter.ApplyACLQoS(ctx, plan, prior)
		return result.Readback, err
	case "nat44":
		plan := diff.NAT44
		plan.StaticMappings = nil
		result, err := adapter.reconciler.adapter.ApplyNAT44(ctx, plan, prior)
		return result.Readback, err
	case "port-maps":
		plan := diff.PortMaps
		plan.PortMappings = nil
		plan.ReadbackStaticMappings = prior.NAT.StaticMappings
		result, err := adapter.reconciler.adapter.ApplyNAT44(ctx, plan, prior)
		return result.Readback, err
	default:
		return vpp.Snapshot{}, fmt.Errorf("unsupported production gateway resource %q", adapter.name)
	}
}

func (adapter *productionGatewayAdapter) applyDiff(ctx context.Context, diff vpp.GatewayDiff, prior vpp.Snapshot, desired vpp.Plan) (vpp.Snapshot, error) {
	switch adapter.name {
	case "interfaces":
		plan := diff.Interfaces
		plan.DeleteInterfaces = nil
		result, err := adapter.reconciler.adapter.ApplyInterfaceBond(ctx, plan, prior)
		return result.Readback, err
	case "bonds":
		plan := diff.Bonds
		plan.DeleteBonds = nil
		result, err := adapter.reconciler.adapter.ApplyInterfaceBond(ctx, plan, prior)
		return result.Readback, err
	case "wan-groups":
		plan := diff.WANGroups
		plan.DeleteWANGroups = nil
		result, err := adapter.reconciler.adapter.ApplyRouteWANGroup(ctx, plan, prior)
		if err == nil && adapter.wanGroups != nil {
			*adapter.wanGroups = vpp.NewWANGroupsContext(prior.WANGroups, plan.WANGroups)
		}
		return result.Readback, err
	case "routes":
		plan := diff.Routes
		plan.DeleteRoutes = nil
		// Route compilation and readback need the complete desired WAN-group
		// inventory even when no group changed in this transaction.  Relying on
		// the preceding wan-groups adapter left this context empty on route-only
		// repairs, so a valid lookup-in-table path was compared with the literal
		// group ID and rejected as drift.
		plan.WANGroupsContext = vpp.NewWANGroupsContext(prior.WANGroups, desired.Policy.WANGroups)
		plan.RoutePolicyContext = append([]trafficpolicy.RoutePolicy(nil), desired.Policy.RoutePolicies...)
		result, err := adapter.reconciler.adapter.ApplyRouteWANGroup(ctx, plan, prior)
		return result.Readback, err
	case "acls":
		plan := diff.ACLs
		plan.DeleteACLs = nil
		result, err := adapter.reconciler.adapter.ApplyACLQoS(ctx, plan, prior)
		return result.Readback, err
	case "qos":
		plan := diff.QoS
		plan.DeleteQoS = nil
		result, err := adapter.reconciler.adapter.ApplyACLQoS(ctx, plan, prior)
		return result.Readback, err
	case "nat44":
		plan := diff.NAT44
		plan.DeleteStaticMappings = nil
		result, err := adapter.reconciler.adapter.ApplyNAT44(ctx, plan, prior)
		return result.Readback, err
	case "port-maps":
		plan := diff.PortMaps
		plan.DeletePortMappings = nil
		plan.ReadbackStaticMappings = desired.NAT.StaticMappings
		result, err := adapter.reconciler.adapter.ApplyNAT44(ctx, plan, prior)
		return result.Readback, err
	default:
		return vpp.Snapshot{}, fmt.Errorf("unsupported production gateway resource %q", adapter.name)
	}
}

func (adapter *productionGatewayAdapter) routeWANGroupsContext() vpp.WANGroupsContext {
	if adapter.wanGroups == nil {
		return vpp.NewWANGroupsContext(nil, nil)
	}
	return *adapter.wanGroups
}
