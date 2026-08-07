package apply

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/vpp"
)

type Phase string

const (
	PhaseValidate    Phase = "validate"
	PhaseCompile     Phase = "compile"
	PhaseSnapshot    Phase = "snapshot"
	PhaseApply       Phase = "apply"
	PhaseHealthCheck Phase = "health-check"
	PhaseReadback    Phase = "readback"
	PhaseCommit      Phase = "commit"
	PhaseRollback    Phase = "rollback"
)

const (
	StatusSuccess  = "success"
	StatusFailure  = "failure"
	StatusRollback = "rollback"
)

type StepFunc func(context.Context, Plan) error

type Clock func() time.Time

type Executor struct {
	Store              *persistence.Store
	Apply              StepFunc
	Receipt            ReceiptFunc
	HealthCheck        StepFunc
	Readback           ReadbackFunc
	Rollback           StepFunc
	Gateway            GatewayTransactionRunner
	Now                Clock
	ApplyMu            *sync.Mutex
	CapabilityFailures func() []CapabilityFailureEvidence
}

type Request struct {
	TransactionID      string
	Actor              string
	Role               string
	Resource           string
	ProxyEgress        proxy.Egress
	FlowIntent         flow.Intent
	SnapshotID         string
	PreviousSnapshotID string
	RollbackID         string
	GatewayPlan        vpp.Plan
}

type Plan struct {
	Request       Request
	CompiledProxy proxy.CompiledEgress
	CompiledFlow  flow.CompiledIntent
	SnapshotHash  string
	Previous      PreviousState
	GatewayPlan   vpp.Plan
}

type PreviousState struct {
	Available    bool
	ProxyEgress  proxy.Egress
	FlowIntent   flow.Intent
	GatewayPlan  *vpp.Plan
	SnapshotHash string
}

type Result struct {
	Plan            Plan
	Events          []persistence.AuditEvent
	Rollback        persistence.RollbackMetadata
	RollbackReceipt RollbackReceipt
	Receipt         ApplyReceipt
	Readback        Readback
	GatewayResult   GatewayTransactionResult
}

func (e Executor) Run(ctx context.Context, request Request) (Result, error) {
	if e.ApplyMu != nil {
		e.ApplyMu.Lock()
		defer e.ApplyMu.Unlock()
	}
	if e.Store == nil {
		return Result{}, fmt.Errorf("apply executor requires persistence store")
	}
	now := e.now
	events := recorder{request: request, now: now}

	if err := proxy.ValidateEgress(request.ProxyEgress); err != nil {
		events.add(PhaseValidate, StatusFailure, "", "", err)
		return e.persistAuditFailure(ctx, events.events, err)
	}
	if err := flow.ValidateIntent(request.FlowIntent); err != nil {
		events.add(PhaseValidate, StatusFailure, "", "", err)
		return e.persistAuditFailure(ctx, events.events, err)
	}
	events.add(PhaseValidate, StatusSuccess, "", "", nil)

	compiledProxy, err := proxy.CompileEgress(request.ProxyEgress)
	if err != nil {
		events.add(PhaseCompile, StatusFailure, "", "", err)
		return e.persistAuditFailure(ctx, events.events, err)
	}
	compiledFlow, err := flow.CompileIntent(request.FlowIntent)
	if err != nil {
		events.add(PhaseCompile, StatusFailure, "", "", err)
		return e.persistAuditFailure(ctx, events.events, err)
	}
	compiledPayload := struct {
		Proxy proxy.CompiledEgress `json:"proxy"`
		Flow  flow.CompiledIntent  `json:"flow"`
	}{Proxy: compiledProxy, Flow: compiledFlow}
	_, compiledHash, err := persistence.MarshalPayload(compiledPayload)
	if err != nil {
		events.add(PhaseCompile, StatusFailure, "", "", err)
		return e.persistAuditFailure(ctx, events.events, err)
	}
	events.add(PhaseCompile, StatusSuccess, "", compiledHash, nil)

	snapshotPayload := SnapshotPayload{Proxy: request.ProxyEgress, Flow: request.FlowIntent, GatewayPlan: &request.GatewayPlan}
	snapshotJSON, snapshotHash, err := persistence.MarshalPayload(snapshotPayload)
	if err != nil {
		events.add(PhaseSnapshot, StatusFailure, "", "", err)
		return e.persistAuditFailure(ctx, events.events, err)
	}
	events.add(PhaseSnapshot, StatusSuccess, "", snapshotHash, nil)

	previous, err := e.previousState(ctx, request.PreviousSnapshotID)
	if err != nil {
		events.add(PhaseSnapshot, StatusFailure, "", snapshotHash, err)
		return e.persistAuditFailure(ctx, events.events, err)
	}
	if e.Gateway != nil && previous.Available && previous.GatewayPlan == nil {
		err := &LegacyGatewayPlanError{SnapshotID: request.PreviousSnapshotID}
		events.add(PhaseSnapshot, StatusFailure, snapshotHash, snapshotHash, err)
		return e.persistAuditFailure(ctx, events.events, err)
	}
	plan := Plan{Request: request, CompiledProxy: compiledProxy, CompiledFlow: compiledFlow, SnapshotHash: snapshotHash, Previous: previous, GatewayPlan: request.GatewayPlan}
	gatewayResult, err := e.gateway(ctx, plan)
	if err != nil {
		return e.fail(ctx, plan, events, snapshotHash, PhaseApply, err)
	}
	// VPP owns production data interfaces. Apply the gateway graph first so
	// Linux control-plane peers (Kea, SmartDNS, PPPoE and Xray) can bind to
	// their committed LCP/TAP handoff instead of racing a nonexistent netdev.
	if err := e.apply(ctx, plan); err != nil {
		return e.fail(ctx, plan, events, snapshotHash, PhaseApply, err)
	}
	receipt, err := e.receipt(ctx, plan)
	if err != nil {
		return e.fail(ctx, plan, events, snapshotHash, PhaseApply, err)
	}
	if err := receipt.validate(request, now()); err != nil {
		return e.fail(ctx, plan, events, snapshotHash, PhaseApply, err)
	}
	events.add(PhaseApply, StatusSuccess, snapshotHash, compiledHash, nil)

	if err := e.healthCheck(ctx, plan); err != nil {
		return e.fail(ctx, plan, events, snapshotHash, PhaseHealthCheck, err)
	}
	events.add(PhaseHealthCheck, StatusSuccess, compiledHash, compiledHash, nil)
	readback, err := e.readback(ctx, plan)
	if err != nil {
		return e.fail(ctx, plan, events, snapshotHash, PhaseReadback, err)
	}
	if err := readback.validate(request, now()); err != nil {
		return e.fail(ctx, plan, events, snapshotHash, PhaseReadback, err)
	}
	events.add(PhaseReadback, StatusSuccess, compiledHash, compiledHash, nil)
	snapshotPayload.Receipt = receipt
	snapshotPayload.Readback = readback
	snapshotPayload.GatewayEvidence = gatewayResult.Evidence
	if e.CapabilityFailures != nil {
		snapshotPayload.CapabilityFailures = append([]CapabilityFailureEvidence(nil), e.CapabilityFailures()...)
	}
	snapshotJSON, snapshotHash, err = persistence.MarshalPayload(snapshotPayload)
	if err != nil {
		return e.fail(ctx, plan, events, plan.SnapshotHash, PhaseSnapshot, err)
	}
	plan.SnapshotHash = snapshotHash
	for index := range events.events {
		if events.events[index].Action == string(PhaseSnapshot) {
			events.events[index].AfterHash = snapshotHash
			break
		}
	}
	events.add(PhaseCommit, StatusSuccess, snapshotHash, compiledHash, nil)

	record := persistence.ApplyRecord{
		Snapshot:    persistence.RuntimeSnapshot{ID: request.SnapshotID, SourceTransactionID: request.TransactionID, Payload: snapshotJSON, PayloadHash: snapshotHash, CreatedAt: now()},
		AuditEvents: events.events,
	}
	if err := e.Store.SaveApply(ctx, record); err != nil {
		return Result{Plan: plan, Events: events.events}, err
	}
	return Result{Plan: plan, Events: events.events, Receipt: receipt, Readback: readback, GatewayResult: gatewayResult}, nil
}
