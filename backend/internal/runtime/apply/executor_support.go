package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

type LegacyGatewayPlanError struct {
	SnapshotID string
}

func (err *LegacyGatewayPlanError) Error() string {
	return fmt.Sprintf("apply: legacy snapshot %q has no gateway plan", err.SnapshotID)
}

func (e Executor) previousState(ctx context.Context, snapshotID string) (PreviousState, error) {
	if snapshotID == "" {
		return PreviousState{}, nil
	}
	snapshot, err := e.Store.RuntimeSnapshot(ctx, snapshotID)
	if err != nil {
		return PreviousState{}, fmt.Errorf("load previous snapshot %s: %w", snapshotID, err)
	}
	if err := persistence.VerifyPayload(snapshot.Payload, snapshot.PayloadHash); err != nil {
		return PreviousState{}, fmt.Errorf("verify previous snapshot %s: %w", snapshotID, err)
	}
	var payload SnapshotPayload
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		return PreviousState{}, fmt.Errorf("decode previous snapshot %s: %w", snapshotID, err)
	}
	if err := proxy.ValidateEgress(payload.Proxy); err != nil {
		return PreviousState{}, fmt.Errorf("validate previous snapshot proxy: %w", err)
	}
	if err := flow.ValidateIntent(payload.Flow); err != nil {
		return PreviousState{}, fmt.Errorf("validate previous snapshot flow: %w", err)
	}
	if err := validateGatewayEvidence(payload.GatewayEvidence, snapshot.SourceTransactionID, e.now()); err != nil {
		return PreviousState{}, fmt.Errorf("validate previous snapshot gateway evidence: %w", err)
	}
	return PreviousState{Available: true, ProxyEgress: payload.Proxy, FlowIntent: payload.Flow, GatewayPlan: payload.GatewayPlan, SnapshotHash: snapshot.PayloadHash}, nil
}

func (e Executor) persistAuditFailure(ctx context.Context, events []persistence.AuditEvent, cause error) (Result, error) {
	if err := e.Store.SaveAuditEvents(ctx, events); err != nil {
		return Result{Events: events}, err
	}
	return Result{Events: events}, cause
}

func (e Executor) fail(ctx context.Context, plan Plan, events recorder, snapshotHash string, phase Phase, cause error) (Result, error) {
	events.add(phase, StatusFailure, snapshotHash, "", cause)
	exactCause := fmt.Sprintf("%s: %s", phase, cause)
	rollback := persistence.RollbackMetadata{ID: plan.Request.RollbackID, TargetSnapshotID: plan.Request.PreviousSnapshotID, Reason: exactCause, Status: "running", RequestedAt: e.now()}
	receipt := RollbackReceipt{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, TargetSnapshotID: plan.Request.PreviousSnapshotID, Cause: exactCause, Timestamp: e.now()}
	rollbackErr := e.rollback(ctx, plan)
	var gatewayErr *GatewayTransactionError
	if errors.As(cause, &gatewayErr) && gatewayErr.RollbackErr != nil {
		rollbackErr = errors.Join(gatewayErr.RollbackErr, rollbackErr)
	}
	completed := e.now()
	if rollbackErr != nil {
		rollback.Status = "failed"
		rollback.Error = rollbackErr.Error()
		rollback.CompletedAt = &completed
		receipt.Status = ReceiptFailed
		receipt.RollbackError = rollbackErr.Error()
		events.add(PhaseRollback, StatusFailure, "", snapshotHash, rollbackErr)
	} else {
		rollback.Status = "succeeded"
		rollback.CompletedAt = &completed
		receipt.Status = ReceiptRolledBack
		events.add(PhaseRollback, StatusRollback, "", snapshotHash, cause)
	}
	if rollback.TargetSnapshotID == "" {
		if err := e.Store.SaveAuditEvents(ctx, events.events); err != nil {
			return Result{Plan: plan, Events: events.events, Rollback: rollback, RollbackReceipt: receipt}, err
		}
		return Result{Plan: plan, Events: events.events, Rollback: rollback, RollbackReceipt: receipt}, joinFailure(cause, rollbackErr)
	}
	record := persistence.ApplyRecord{Rollback: rollback, AuditEvents: events.events}
	if err := e.Store.SaveApply(ctx, record); err != nil {
		return Result{Plan: plan, Events: events.events, Rollback: rollback, RollbackReceipt: receipt}, err
	}
	return Result{Plan: plan, Events: events.events, Rollback: rollback, RollbackReceipt: receipt}, joinFailure(cause, rollbackErr)
}

func joinFailure(cause, rollbackErr error) error {
	if rollbackErr == nil {
		return cause
	}
	return errors.Join(cause, rollbackErr)
}

func (e Executor) apply(ctx context.Context, plan Plan) error {
	if e.Apply == nil {
		return nil
	}
	return e.Apply(ctx, plan)
}

func (e Executor) receipt(ctx context.Context, plan Plan) (ApplyReceipt, error) {
	if e.Receipt == nil {
		return ApplyReceipt{}, ErrIncompleteEvidence
	}
	return e.Receipt(ctx, plan)
}

func (e Executor) gateway(ctx context.Context, plan Plan) (GatewayTransactionResult, error) {
	if e.Gateway == nil {
		return GatewayTransactionResult{}, nil
	}
	result, err := e.Gateway.Run(ctx, plan)
	if err != nil {
		return result, fmt.Errorf("gateway multi-resource apply: %w", err)
	}
	return result, nil
}

func (e Executor) healthCheck(ctx context.Context, plan Plan) error {
	if e.HealthCheck == nil {
		return nil
	}
	return e.HealthCheck(ctx, plan)
}

func (e Executor) readback(ctx context.Context, plan Plan) (Readback, error) {
	if e.Readback == nil {
		return Readback{}, ErrIncompleteEvidence
	}
	return e.Readback(ctx, plan)
}

func (e Executor) rollback(ctx context.Context, plan Plan) error {
	if e.Rollback != nil {
		return e.Rollback(ctx, plan)
	}
	if e.Gateway != nil {
		return e.Gateway.Rollback(ctx, plan)
	}
	return errors.New("rollback handler is not configured")
}

func (e Executor) now() time.Time {
	if e.Now == nil {
		return time.Now().UTC()
	}
	return e.Now().UTC()
}

type recorder struct {
	request Request
	now     func() time.Time
	events  []persistence.AuditEvent
}

func (r *recorder) add(phase Phase, status, beforeHash, afterHash string, err error) {
	event := persistence.AuditEvent{
		ID:            fmt.Sprintf("%s-%02d-%s", r.request.TransactionID, len(r.events)+1, phase),
		Timestamp:     r.now(),
		Actor:         r.request.Actor,
		Role:          r.request.Role,
		Resource:      r.request.Resource,
		Action:        string(phase),
		BeforeHash:    beforeHash,
		AfterHash:     afterHash,
		Status:        status,
		TransactionID: r.request.TransactionID,
	}
	if err != nil {
		event.Error = err.Error()
	}
	r.events = append(r.events, event)
}
