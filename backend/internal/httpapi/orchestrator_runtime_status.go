package httpapi

import (
	"context"
	"fmt"
	"strings"

	"ly-route/backend/internal/orchestratorapi"
	"ly-route/backend/internal/product"
	"ly-route/backend/internal/runtime/apply"
)

func (server *Server) orchestratorTransparentRuntimeComponents(ctx context.Context) ([]RuntimeComponentState, bool) {
	if server.profile.ID() != product.Orchestrator().ID() {
		return nil, false
	}
	provider, ok := server.orchestratorRuntime.(orchestratorapi.TransparentRuntimeEvidenceProvider)
	if !ok {
		return nil, false
	}
	evidence, err := provider.TransparentRuntimeEvidence(ctx)
	if err != nil {
		now := server.now().UTC()
		reason := "transparent orchestrator runtime readback failed: " + err.Error()
		server.setRuntimeEvidence(degradedRuntimeResult("orchestrator-observe", reason, now))
		components := []RuntimeComponentState{
			{Name: "vpp", State: "degraded", Available: false, Reason: reason},
			{Name: "persistence", State: "running", Available: server.store != nil, Reason: persistenceRuntimeReason(server.store != nil)},
		}
		return server.applyRuntimeEvidence(components), true
	}
	if strings.TrimSpace(evidence.TransactionID) == "" || strings.TrimSpace(evidence.Generation) == "" || evidence.State != "running" || evidence.AppliedAt.IsZero() || evidence.ObservedAt.IsZero() || evidence.ObservedAt.Before(evidence.AppliedAt) {
		now := server.now().UTC()
		reason := fmt.Sprintf("transparent orchestrator runtime evidence is incomplete: transaction=%q generation=%q state=%q", evidence.TransactionID, evidence.Generation, evidence.State)
		server.setRuntimeEvidence(degradedRuntimeResult(nonEmpty(evidence.TransactionID, "orchestrator-observe"), reason, now))
		return server.applyRuntimeEvidence([]RuntimeComponentState{{Name: "vpp", State: "degraded", Reason: reason}, {Name: "persistence", State: "running", Available: server.store != nil, Reason: persistenceRuntimeReason(server.store != nil)}}), true
	}
	capability := "vpp.transparent-orchestrator"
	result := RuntimeApplyResult{
		Status: RuntimeStatusCommitted, RuntimeState: RuntimeStateRunning,
		TransactionID: evidence.TransactionID, AppliedAt: evidence.AppliedAt,
		Receipt:               apply.ApplyReceipt{TransactionID: evidence.TransactionID, Capability: capability, Status: apply.ReceiptApplied, AppliedAt: evidence.AppliedAt},
		Readback:              apply.Readback{TransactionID: evidence.TransactionID, Capability: capability, Timestamp: evidence.ObservedAt, Fresh: true},
		ReconciliationReceipt: apply.ReconciliationReceipt{TransactionID: evidence.TransactionID, Capability: "runtime.reconciliation", Status: apply.ReceiptApplied, Cause: "live transparent VPP generation verified", Timestamp: evidence.ObservedAt},
	}
	server.setRuntimeEvidence(result)
	components := []RuntimeComponentState{
		{Name: "vpp", State: "running", Available: true, Reason: "transparent VPP generation " + evidence.Generation + " verified"},
		{Name: "persistence", State: "running", Available: server.store != nil, Reason: persistenceRuntimeReason(server.store != nil)},
	}
	return server.applyRuntimeEvidence(components), true
}

func persistenceRuntimeReason(available bool) string {
	if available {
		return "persisted topology and policy are available"
	}
	return "local store is not configured"
}
