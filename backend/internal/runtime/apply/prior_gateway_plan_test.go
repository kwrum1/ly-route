package apply

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/vpp"
)

func TestExecutorPersistsAndReloadsPriorGatewayPlan(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	clock := fixedGatewayPlanClock()
	want := vpp.Plan{RequestID: "gateway-plan-1"}
	request := validRequest("txn-gateway-plan")
	request.GatewayPlan = want
	executor := gatewayPlanExecutor(store, clock)

	// When
	if _, err := executor.Run(ctx, request); err != nil {
		t.Fatal(err)
	}
	previous, err := executor.previousState(ctx, request.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if previous.GatewayPlan == nil || !reflect.DeepEqual(*previous.GatewayPlan, want) {
		t.Fatalf("prior gateway plan = %#v, want typed plan %#v", previous.GatewayPlan, want)
	}
}

func TestExecutorPersistsValidEmptyPriorGatewayPlan(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	clock := fixedGatewayPlanClock()
	request := validRequest("txn-empty-gateway-plan")
	executor := gatewayPlanExecutor(store, clock)

	// When
	if _, err := executor.Run(ctx, request); err != nil {
		t.Fatal(err)
	}
	previous, err := executor.previousState(ctx, request.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if previous.GatewayPlan == nil {
		t.Fatal("prior gateway plan is nil, want valid empty plan")
	}
	if !reflect.DeepEqual(*previous.GatewayPlan, vpp.Plan{}) {
		t.Fatalf("prior gateway plan = %#v, want empty plan", *previous.GatewayPlan)
	}
	stored, err := store.RuntimeSnapshot(ctx, request.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	var payload SnapshotPayload
	if err := json.Unmarshal(stored.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.GatewayPlan == nil {
		t.Fatal("persisted gateway plan is nil, want explicit empty plan")
	}
}

func TestExecutorFailsClosedForLegacyPriorSnapshotWithGateway(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-legacy-gateway", "txn-legacy")
	request := validRequest("txn-legacy-gateway-reconcile")
	request.PreviousSnapshotID = "snapshot-legacy-gateway"
	runner := &recordingGatewayRunner{}
	executor := gatewayPlanExecutor(store, fixedGatewayPlanClock())
	executor.Gateway = runner

	// When
	_, err := executor.Run(ctx, request)

	// Then
	var legacyErr *LegacyGatewayPlanError
	if !errors.As(err, &legacyErr) {
		t.Fatalf("Run error = %v, want typed legacy gateway plan error", err)
	}
	if legacyErr.SnapshotID != request.PreviousSnapshotID || legacyErr.Error() != "apply: legacy snapshot \"snapshot-legacy-gateway\" has no gateway plan" {
		t.Fatalf("legacy error = %#v, want deterministic snapshot error", legacyErr)
	}
	if runner.called {
		t.Fatal("Gateway transaction ran for legacy prior snapshot")
	}
}

func TestExecutorKeepsLegacyPriorSnapshotCompatibleWithoutGateway(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-legacy-no-gateway", "txn-legacy")
	request := validRequest("txn-legacy-no-gateway-reconcile")
	request.PreviousSnapshotID = "snapshot-legacy-no-gateway"

	// When
	_, err := gatewayPlanExecutor(store, fixedGatewayPlanClock()).Run(ctx, request)

	// Then
	if err != nil {
		t.Fatalf("Run error = %v, want legacy Gateway-less compatibility", err)
	}
}

func gatewayPlanExecutor(store *persistence.Store, clock Clock) Executor {
	return Executor{
		Store: store,
		Now:   clock,
		Receipt: func(_ context.Context, plan Plan) (ApplyReceipt, error) {
			return ApplyReceipt{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Status: ReceiptApplied, AppliedAt: clock()}, nil
		},
		Readback: func(_ context.Context, plan Plan) (Readback, error) {
			return Readback{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Timestamp: clock(), Fresh: true}, nil
		},
	}
}

func fixedGatewayPlanClock() Clock {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

type recordingGatewayRunner struct {
	called         bool
	rollbackCalled bool
}

func (runner *recordingGatewayRunner) Run(context.Context, Plan) (GatewayTransactionResult, error) {
	runner.called = true
	return GatewayTransactionResult{}, nil
}

func (runner *recordingGatewayRunner) Rollback(context.Context, Plan) error {
	runner.rollbackCalled = true
	return nil
}
