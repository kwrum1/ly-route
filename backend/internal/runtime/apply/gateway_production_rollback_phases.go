package apply

import (
	"context"
	"fmt"

	"ly-route/backend/internal/runtime/vpp"
)

func (adapter *productionGatewayAdapter) RollbackCleanup(ctx context.Context, plan Plan) error {
	diff, _, found := adapter.reconciler.state(plan.Request.TransactionID)
	if !found {
		return fmt.Errorf("%s rollback cleanup: reconciliation is unavailable", adapter.name)
	}
	operations, err := adapter.cleanupOperations(diff)
	if err != nil {
		return err
	}
	owner := adapter.supplementalOwner()
	supplementalChanged, err := adapter.supplementalChanged(plan)
	if err != nil {
		return err
	}
	// ApplySupplemental uses the same applicability predicate. If plan-wide
	// prerequisites prevented a supplemental owner from running, rollback must
	// not rebuild that owner and turn an unapplied prerequisite into a cleanup
	// failure. Resource-diff cleanup below still removes any core mutations.
	if owner != "" && supplementalChanged && vpp.HasSupplementalOperations(plan.GatewayPlan, owner) {
		supplemental, supplementalErr := vpp.SupplementalCleanupOperations(plan.GatewayPlan, owner)
		if supplementalErr != nil {
			return supplementalErr
		}
		operations = append(supplemental, operations...)
	}
	return adapter.reconciler.adapter.ExecuteOperations(ctx, operations)
}

func (adapter *productionGatewayAdapter) RollbackRestore(ctx context.Context, plan Plan) error {
	_, prior, found := adapter.reconciler.state(plan.Request.TransactionID)
	if !found {
		return fmt.Errorf("%s rollback restore: prior snapshot is unavailable", adapter.name)
	}
	supplementalChanged, err := adapter.supplementalChanged(plan)
	if err != nil {
		return err
	}
	if !gatewaySnapshotHasResource(adapter.name, prior) {
		if !supplementalChanged || plan.Previous.GatewayPlan == nil {
			return nil
		}
		_, err := adapter.applySupplemental(ctx, *plan.Previous.GatewayPlan)
		return err
	}
	_, err = adapter.runPrior(ctx, productionPriorApply{transactionID: plan.Request.TransactionID, gateway: adapter.priorPlan(productionRollbackInput{transactionID: plan.Request.TransactionID, desired: plan.GatewayPlan, prior: prior}), prior: prior})
	if err != nil || !supplementalChanged || plan.Previous.GatewayPlan == nil {
		return err
	}
	_, err = adapter.applySupplemental(ctx, *plan.Previous.GatewayPlan)
	return err
}

func (adapter *productionGatewayAdapter) cleanupOperations(diff vpp.GatewayDiff) ([]vpp.Operation, error) {
	switch adapter.name {
	case "interfaces":
		states := diff.Interfaces.Interfaces
		return vpp.BuildInterfaceBondOperations(vpp.InterfaceBondPlan{TransactionID: diff.Interfaces.TransactionID, ManagementInterface: diff.Interfaces.ManagementInterface, DeleteInterfaces: interfaceStateNames(states), DeleteInterfaceState: states})
	case "bonds":
		return vpp.BuildInterfaceBondOperations(vpp.InterfaceBondPlan{TransactionID: diff.Bonds.TransactionID, ManagementInterface: diff.Bonds.ManagementInterface, DeleteBonds: bondStateNames(diff.Bonds.Bonds)})
	case "wan-groups":
		return vpp.BuildRouteWANGroupOperations(vpp.RouteWANGroupPlan{TransactionID: diff.WANGroups.TransactionID, DeleteWANGroups: wanGroupNames(diff.WANGroups.WANGroups), DeleteWANState: diff.WANGroups.WANGroups})
	case "routes":
		// Route cleanup must use the same resolved LAN ingress as the apply
		// plan.  Delete operations still need to detach the ABF policy from
		// its interface before removing the ACL; leaving this blank makes a
		// stale route fail reconciliation even though the desired route was
		// already removed from the configuration.
		return vpp.BuildRouteWANGroupOperations(vpp.RouteWANGroupPlan{
			TransactionID:       diff.Routes.TransactionID,
			IngressVPPInterface: diff.Routes.IngressVPPInterface,
			DeleteRoutes:        routeNames(diff.Routes.Routes),
			DeleteRouteState:    diff.Routes.Routes,
		})
	case "acls":
		return vpp.BuildACLQoSOperations(vpp.ACLQoSPlan{TransactionID: diff.ACLs.TransactionID, DeleteACLs: aclNames(diff.ACLs.ACLs), DeleteACLState: diff.ACLs.ACLs})
	case "qos":
		return vpp.BuildACLQoSOperations(vpp.ACLQoSPlan{TransactionID: diff.QoS.TransactionID, DeleteQoS: qosNames(diff.QoS.QoS), DeleteQoSState: diff.QoS.QoS})
	case "nat44":
		return vpp.BuildNAT44Operations(vpp.NAT44Plan{TransactionID: diff.NAT44.TransactionID, DeleteStaticMappings: staticMappingNames(diff.NAT44.StaticMappings), ReadbackStaticMappings: diff.NAT44.StaticMappings})
	case "port-maps":
		return vpp.BuildNAT44Operations(vpp.NAT44Plan{TransactionID: diff.PortMaps.TransactionID, DeletePortMappings: portMappingNames(diff.PortMaps.PortMappings), ReadbackPortMappings: diff.PortMaps.PortMappings})
	default:
		return nil, fmt.Errorf("unsupported production gateway resource %q", adapter.name)
	}
}

func interfaceStateNames(states []vpp.InterfaceState) []string {
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, state.Name)
	}
	return names
}

func bondStateNames(states []vpp.BondState) []string {
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, state.Name)
	}
	return names
}
