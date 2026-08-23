package httpapi

import (
	"context"

	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func (server *Server) decorateRoutePolicyRuntimeStates(ctx context.Context, items []map[string]any) []map[string]any {
	applied := server.appliedRoutePolicyIDs(ctx)
	decorated := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := cloneObject(item)
		clone["runtime_state"] = "desired_not_applied"
		if _, ok := applied[stringField(clone, "id")]; ok {
			clone["runtime_state"] = "applied"
		}
		decorated = append(decorated, clone)
	}
	return decorated
}

func (server *Server) decorateRoutePolicyRuntimeState(ctx context.Context, item map[string]any) map[string]any {
	items := server.decorateRoutePolicyRuntimeStates(ctx, []map[string]any{item})
	return items[0]
}

func (server *Server) decorateWANGroupRuntimeStates(ctx context.Context, items []map[string]any) []map[string]any {
	applied := server.appliedWANGroupIDs(ctx)
	decorated := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := cloneObject(item)
		clone["runtime_state"] = "desired_not_applied"
		if _, ok := applied[stringField(clone, "id")]; ok {
			clone["runtime_state"] = "applied"
		}
		decorated = append(decorated, clone)
	}
	return decorated
}

func (server *Server) appliedWANGroupIDs(ctx context.Context) map[string]struct{} {
	result := server.runtimeEvidence()
	if result.Status != RuntimeStatusCommitted || result.RuntimeState != RuntimeStateRunning || result.GatewayPlan == nil {
		return nil
	}
	if result.TransactionID == "" || result.Receipt.Status != apply.ReceiptApplied || result.Receipt.TransactionID != result.TransactionID {
		return nil
	}
	for _, evidence := range result.GatewayEvidence {
		if evidence.Resource == "wan-groups" && evidence.TransactionID == result.TransactionID && evidence.ApplyReceipt.Status == apply.ReceiptApplied && evidence.ApplyReceipt.TransactionID == result.TransactionID && equalGatewaySlice(evidence.After.WANGroups, result.GatewayPlan.Policy.WANGroups) {
			current, err := server.currentTrafficPolicyConfig(ctx)
			if err != nil || !equalGatewaySliceUnordered(current.WANGroups, result.GatewayPlan.Policy.WANGroups) {
				return nil
			}
			applied := make(map[string]struct{}, len(current.WANGroups))
			for _, group := range current.WANGroups {
				applied[group.ID] = struct{}{}
			}
			return applied
		}
	}
	return nil
}

func (server *Server) appliedRoutePolicyIDs(ctx context.Context) map[string]struct{} {
	result := server.runtimeEvidence()
	if result.Status != RuntimeStatusCommitted || result.RuntimeState != RuntimeStateRunning || result.GatewayPlan == nil {
		return nil
	}
	if result.TransactionID == "" || result.Receipt.Status != apply.ReceiptApplied || result.Receipt.TransactionID != result.TransactionID {
		return nil
	}

	var appliedSnapshot []trafficpolicy.RoutePolicy
	for _, evidence := range result.GatewayEvidence {
		if evidence.Resource != "routes" || evidence.TransactionID != result.TransactionID {
			continue
		}
		if evidence.ApplyReceipt.Status != apply.ReceiptApplied || evidence.ApplyReceipt.TransactionID != result.TransactionID {
			continue
		}
		appliedSnapshot = evidence.After.RoutePolicies
		break
	}
	if appliedSnapshot == nil {
		return nil
	}

	current, err := server.currentTrafficPolicyConfig(ctx)
	if err != nil {
		return nil
	}
	applied := make(map[string]struct{}, len(current.RoutePolicies))
	for _, currentPolicy := range current.RoutePolicies {
		plannedPolicy, planned := routePolicyWithID(result.GatewayPlan.Policy.RoutePolicies, currentPolicy.ID)
		observedPolicy, observed := routePolicyWithID(appliedSnapshot, currentPolicy.ID)
		if planned && observed && equalGatewaySlice([]trafficpolicy.RoutePolicy{currentPolicy}, []trafficpolicy.RoutePolicy{plannedPolicy}) && equalGatewaySlice([]trafficpolicy.RoutePolicy{currentPolicy}, []trafficpolicy.RoutePolicy{observedPolicy}) {
			applied[currentPolicy.ID] = struct{}{}
		}
	}
	return applied
}

func routePolicyWithID(items []trafficpolicy.RoutePolicy, id string) (trafficpolicy.RoutePolicy, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return trafficpolicy.RoutePolicy{}, false
}
