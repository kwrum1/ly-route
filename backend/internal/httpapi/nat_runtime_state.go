package httpapi

import (
	"context"

	"ly-route/backend/internal/runtime/apply"
)

// decoratePortMapRuntimeStates keeps the port-map list consistent with the
// route-policy list: a desired resource is only shown as applied after the
// same transaction has a committed typed VPP readback and the current desired
// NAT plan still matches that evidence.
func (server *Server) decoratePortMapRuntimeStates(ctx context.Context, items []map[string]any) []map[string]any {
	applied := server.appliedNATPortMapIDs(ctx)
	decorated := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := decoratePortMapItem(item)
		clone["runtime_state"] = "desired_not_applied"
		if _, ok := applied[stringField(clone, "id")]; ok {
			clone["runtime_state"] = "applied"
			clone["dataplane_observed"] = true
		}
		decorated = append(decorated, clone)
	}
	return decorated
}

func (server *Server) appliedNATPortMapIDs(ctx context.Context) map[string]struct{} {
	result := server.runtimeEvidence()
	if result.Status != RuntimeStatusCommitted || result.RuntimeState != RuntimeStateRunning || result.GatewayPlan == nil {
		return nil
	}
	if result.TransactionID == "" || result.Receipt.Status != apply.ReceiptApplied || result.Receipt.TransactionID != result.TransactionID {
		return nil
	}
	matched := false
	for _, evidence := range result.GatewayEvidence {
		if evidence.Resource != "port-maps" || evidence.TransactionID != result.TransactionID {
			continue
		}
		if evidence.ApplyReceipt.Status != apply.ReceiptApplied || evidence.ApplyReceipt.TransactionID != result.TransactionID {
			continue
		}
		if equalGatewaySliceUnordered(evidence.After.NAT.PortMappings, result.GatewayPlan.NAT.PortMappings) {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}
	current, err := server.currentNATConfig(ctx)
	if err != nil || !equalGatewaySliceUnordered(current.PortMappings, result.GatewayPlan.NAT.PortMappings) {
		return nil
	}
	applied := make(map[string]struct{}, len(current.PortMappings))
	for _, mapping := range current.PortMappings {
		applied[mapping.ID] = struct{}{}
	}
	return applied
}
