package httpapi

import (
	"context"

	"ly-route/backend/internal/runtime/apply"
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

func (server *Server) appliedRoutePolicyIDs(ctx context.Context) map[string]struct{} {
	result := server.runtimeEvidence()
	if result.Status != RuntimeStatusCommitted || result.RuntimeState != RuntimeStateRunning || result.GatewayPlan == nil {
		return nil
	}
	if result.TransactionID == "" || result.Receipt.Status != apply.ReceiptApplied || result.Receipt.TransactionID != result.TransactionID {
		return nil
	}

	routeEvidenceMatches := false
	for _, evidence := range result.GatewayEvidence {
		if evidence.Resource != "routes" || evidence.Deleted || evidence.TransactionID != result.TransactionID {
			continue
		}
		if evidence.ApplyReceipt.Status != apply.ReceiptApplied || evidence.ApplyReceipt.TransactionID != result.TransactionID {
			continue
		}
		if equalGatewaySlice(evidence.After.RoutePolicies, result.GatewayPlan.Policy.RoutePolicies) {
			routeEvidenceMatches = true
			break
		}
	}
	if !routeEvidenceMatches {
		return nil
	}

	current, err := server.currentTrafficPolicyConfig(ctx)
	if err != nil || !equalGatewaySlice(current.RoutePolicies, result.GatewayPlan.Policy.RoutePolicies) {
		return nil
	}
	applied := make(map[string]struct{}, len(current.RoutePolicies))
	for _, policy := range current.RoutePolicies {
		applied[policy.ID] = struct{}{}
	}
	return applied
}
