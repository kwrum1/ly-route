package httpapi

import (
	"context"

	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/flow"
)

func (server *Server) decorateTrafficControlRuntimeStates(ctx context.Context, items []map[string]any) []map[string]any {
	applied := server.appliedTrafficControlIDs(ctx)
	decorated := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := decorateTrafficControlItem(item)
		clone["runtime_state"] = "desired_not_applied"
		clone["dataplane_observed"] = false
		if _, ok := applied[stringField(clone, "id")]; ok {
			clone["runtime_state"] = "applied"
			clone["dataplane_observed"] = true
		}
		decorated = append(decorated, clone)
	}
	return decorated
}

func (server *Server) decorateTrafficControlRuntimeState(ctx context.Context, item map[string]any) map[string]any {
	items := server.decorateTrafficControlRuntimeStates(ctx, []map[string]any{item})
	return items[0]
}

func (server *Server) appliedTrafficControlIDs(ctx context.Context) map[string]struct{} {
	result := server.runtimeEvidence()
	if result.Status != RuntimeStatusCommitted || result.RuntimeState != RuntimeStateRunning || result.GatewayPlan == nil {
		return nil
	}
	if result.TransactionID == "" || result.Receipt.Status != apply.ReceiptApplied || result.Receipt.TransactionID != result.TransactionID {
		return nil
	}

	var observed []flow.VPPObjectGroup
	for _, evidence := range result.GatewayEvidence {
		if evidence.Resource != "qos" || evidence.TransactionID != result.TransactionID {
			continue
		}
		if evidence.ApplyReceipt.Status != apply.ReceiptApplied || evidence.ApplyReceipt.TransactionID != result.TransactionID {
			continue
		}
		observed = evidence.After.QoS
		break
	}
	if observed == nil || !equalGatewaySlice(observed, result.GatewayPlan.Flow.VPPGroups) {
		return nil
	}

	current, err := server.currentFlowIntent(ctx)
	if err != nil {
		return nil
	}
	expanded, err := server.expandFlowIntentAddressGroups(ctx, current)
	if err != nil {
		return nil
	}
	compiled, err := flow.CompileIntent(expanded)
	if err != nil || compiled.ID != result.GatewayPlan.Flow.ID {
		return nil
	}
	if !equalGatewaySlice(compiled.Targets, result.GatewayPlan.Flow.Targets) || !equalGatewaySlice(compiled.VPPGroups, result.GatewayPlan.Flow.VPPGroups) {
		return nil
	}
	return map[string]struct{}{current.ID: {}}
}
