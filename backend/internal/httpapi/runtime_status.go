package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/apply"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

const (
	RuntimeStatusCommitted = "committed"
	RuntimeStateRunning    = "running"
)

type RuntimeEvidenceProvider = serviceRuntime.RuntimeEvidenceProvider

type RuntimeEvidenceRequest = serviceRuntime.EvidenceRequest

func (server *Server) setRuntimeEvidence(result RuntimeApplyResult) {
	if (result.Status == RuntimeStatusCommitted || result.RuntimeState == RuntimeStateRunning) && !completeRuntimeEvidence(result, server.now().UTC()) {
		result.Status = apply.ReceiptDegraded
		result.RuntimeState = "degraded"
		result.Reason = runtimeEvidenceReason(result)
	}
	server.runtimeMu.Lock()
	server.lastRuntime = &result
	server.runtimeMu.Unlock()
}

func (server *Server) runtimeEvidence() RuntimeApplyResult {
	server.runtimeMu.Lock()
	defer server.runtimeMu.Unlock()
	if server.lastRuntime != nil {
		return *server.lastRuntime
	}
	now := server.now().UTC()
	return RuntimeApplyResult{
		Status:        "unreconciled",
		RuntimeState:  "degraded",
		TransactionID: "unreconciled",
		Reason:        "no complete runtime apply receipt or readback",
		Receipt: apply.ApplyReceipt{
			TransactionID: "unreconciled",
			Status:        apply.ReceiptMissing,
			AppliedAt:     now,
		},
		Readback: apply.Readback{
			TransactionID: "unreconciled",
			Timestamp:     now,
			Fresh:         false,
			Reason:        "no complete runtime apply receipt or readback",
		},
		ReconciliationReceipt: apply.ReconciliationReceipt{TransactionID: "unreconciled", Capability: "runtime.reconciliation", Status: apply.ReceiptDegraded, Cause: "no persisted runtime evidence", Timestamp: now},
		AppliedAt:             now,
	}
}

func (server *Server) applyRuntimeEvidence(components []RuntimeComponentState) []RuntimeComponentState {
	result := server.runtimeEvidence()
	now := server.now().UTC()
	chainComplete := completeRuntimeEvidence(result, now)
	for index := range components {
		component := &components[index]
		capability := component.Name
		component.TransactionID = result.TransactionID
		component.Capability = capability
		component.ApplyReceipt = result.Receipt
		component.ApplyReceipt.Capability = capability
		component.ReadbackAt = result.Readback.Timestamp
		component.Fresh = chainComplete
		if component.State == "running" && !chainComplete {
			component.State = "degraded"
			component.Available = false
			component.Reason = runtimeEvidenceReason(result)
		} else if component.Reason == "" && chainComplete {
			component.Reason = "apply receipt and fresh readback verified"
		}
	}
	return components
}

func completeRuntimeEvidence(result RuntimeApplyResult, now time.Time) bool {
	if result.Receipt.Status != apply.ReceiptApplied || result.TransactionID == "" || result.Receipt.TransactionID != result.TransactionID || result.Receipt.Capability == "" || result.Readback.Capability != result.Receipt.Capability || result.Receipt.AppliedAt.IsZero() || result.Receipt.AppliedAt.After(result.Readback.Timestamp) {
		return false
	}
	if !result.Readback.Fresh || result.Readback.TransactionID != result.TransactionID || result.Readback.Timestamp.IsZero() || now.Sub(result.Readback.Timestamp) < 0 || now.Sub(result.Readback.Timestamp) > apply.ReadbackFreshnessWindow {
		return false
	}
	return completeGatewayRuntimeEvidence(result, now) == nil
}

func runtimeEvidenceReason(result RuntimeApplyResult) string {
	if strings.TrimSpace(result.Reason) != "" {
		return result.Reason
	}
	if result.Receipt.Status != apply.ReceiptApplied {
		return "runtime apply receipt is incomplete"
	}
	if result.Readback.Capability != result.Receipt.Capability {
		return "runtime receipt/readback capability mismatch"
	}
	if !result.Readback.Fresh {
		if strings.TrimSpace(result.Readback.Reason) != "" {
			return result.Readback.Reason
		}
		return "runtime readback is stale"
	}
	return "runtime apply receipt/readback chain is incomplete"
}

func (server *Server) reconcileRuntime(ctx context.Context) {
	if server.store == nil {
		return
	}
	snapshots, err := server.store.RuntimeSnapshots(ctx)
	if err != nil || len(snapshots) == 0 {
		return
	}
	snapshot := snapshots[0]
	if err := persistence.VerifyPayload(snapshot.Payload, snapshot.PayloadHash); err != nil {
		server.setRuntimeEvidence(degradedRuntimeResult(snapshot.SourceTransactionID, fmt.Sprintf("runtime snapshot integrity failed: %v", err), server.now().UTC()))
		return
	}
	result, err := runtimeResultFromSnapshot(snapshot)
	if err != nil || !completeRuntimeEvidence(result, server.now().UTC()) {
		reason := "incomplete persisted runtime evidence"
		if err != nil {
			reason = fmt.Sprintf("incomplete persisted runtime evidence: %v", err)
		} else if !result.Readback.Timestamp.IsZero() && server.now().UTC().Sub(result.Readback.Timestamp) > apply.ReadbackFreshnessWindow {
			reason = "stale persisted runtime readback"
		} else if gatewayErr := completeGatewayRuntimeEvidence(result, server.now().UTC()); gatewayErr != nil {
			reason = fmt.Sprintf("incomplete persisted gateway resource evidence: %v", gatewayErr)
		}
		degraded := degradedRuntimeResult(snapshot.SourceTransactionID, reason, server.now().UTC())
		degraded.CapabilityFailures = append([]apply.CapabilityFailureEvidence(nil), result.CapabilityFailures...)
		degraded.ReconciliationReceipt = apply.ReconciliationReceipt{TransactionID: snapshot.SourceTransactionID, Capability: "runtime.reconciliation", Status: apply.ReceiptDegraded, Cause: reason, Timestamp: server.now().UTC()}
		server.setRuntimeEvidence(degraded)
		return
	}
	result.ReconciliationReceipt = apply.ReconciliationReceipt{TransactionID: result.TransactionID, Capability: "runtime.reconciliation", Status: apply.ReceiptApplied, Cause: "persisted runtime evidence reconciled", Timestamp: server.now().UTC()}
	server.setRuntimeEvidence(result)
}

func runtimeResultFromSnapshot(snapshot persistence.RuntimeSnapshot) (RuntimeApplyResult, error) {
	var result RuntimeApplyResult
	if err := json.Unmarshal(snapshot.Payload, &result); err != nil {
		return RuntimeApplyResult{}, err
	}
	if result.TransactionID != "" && result.Receipt.TransactionID != "" {
		return result, nil
	}
	var payload apply.SnapshotPayload
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		return RuntimeApplyResult{}, err
	}
	if payload.Receipt.TransactionID == "" || payload.Readback.TransactionID == "" {
		return RuntimeApplyResult{}, fmt.Errorf("snapshot has no receipt/readback envelope")
	}
	return RuntimeApplyResult{
		Status:             "committed",
		RuntimeState:       "running",
		TransactionID:      snapshot.SourceTransactionID,
		Receipt:            payload.Receipt,
		Readback:           payload.Readback,
		GatewayPlan:        payload.GatewayPlan,
		GatewayEvidence:    payload.GatewayEvidence,
		CapabilityFailures: payload.CapabilityFailures,
		AppliedAt:          payload.Receipt.AppliedAt,
	}, nil
}

func degradedRuntimeResult(transactionID, reason string, now time.Time) RuntimeApplyResult {
	if transactionID == "" {
		transactionID = "reconcile"
	}
	return RuntimeApplyResult{
		Status:                "reconciled_degraded",
		RuntimeState:          "degraded",
		TransactionID:         transactionID,
		Reason:                reason,
		Receipt:               apply.ApplyReceipt{TransactionID: transactionID, Capability: "runtime.reconciliation", Status: apply.ReceiptFailed, AppliedAt: now, Cause: reason},
		Readback:              apply.Readback{TransactionID: transactionID, Timestamp: now, Fresh: false, Reason: reason},
		ReconciliationReceipt: apply.ReconciliationReceipt{TransactionID: transactionID, Capability: "runtime.reconciliation", Status: apply.ReceiptDegraded, Cause: reason, Timestamp: now},
		AppliedAt:             now,
	}
}

func (server *Server) runtimeReceipt(ctx context.Context, request RuntimeEvidenceRequest) (apply.ApplyReceipt, error) {
	if server.services == nil || server.services.Controller == nil {
		return apply.ApplyReceipt{}, fmt.Errorf("runtime apply receipt provider is not configured: %w", apply.ErrIncompleteEvidence)
	}
	provider, ok := server.services.Controller.(RuntimeEvidenceProvider)
	if !ok {
		return apply.ApplyReceipt{}, fmt.Errorf("runtime apply receipt provider is not configured: %w", apply.ErrIncompleteEvidence)
	}
	return provider.Receipt(ctx, request)
}

func (server *Server) runtimeReadback(ctx context.Context, request RuntimeEvidenceRequest) (apply.Readback, error) {
	if server.services == nil || server.services.Controller == nil {
		return apply.Readback{}, fmt.Errorf("runtime readback provider is not configured: %w", apply.ErrIncompleteEvidence)
	}
	provider, ok := server.services.Controller.(RuntimeEvidenceProvider)
	if !ok {
		return apply.Readback{}, fmt.Errorf("runtime readback provider is not configured: %w", apply.ErrIncompleteEvidence)
	}
	return provider.Readback(ctx, request)
}

func (server *Server) latestRuntimeSnapshotID(ctx context.Context) string {
	if server.store == nil {
		return ""
	}
	snapshots, err := server.store.RuntimeSnapshots(ctx)
	if err != nil || len(snapshots) == 0 {
		return ""
	}
	return snapshots[0].ID
}
