package apply

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

func TestExecutorEmitsOrderedCommitAuditEvents(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, ctx)
	clock := deterministicClock(time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC))
	executor := Executor{
		Store: store,
		Now:   clock,
		Receipt: func(_ context.Context, plan Plan) (ApplyReceipt, error) {
			return ApplyReceipt{TransactionID: plan.Request.TransactionID, Status: ReceiptApplied, Capability: plan.Request.Resource, AppliedAt: clock()}, nil
		},
		Readback: func(_ context.Context, plan Plan) (Readback, error) {
			return Readback{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Timestamp: clock(), Fresh: true}, nil
		},
	}

	result, err := executor.Run(ctx, validRequest("txn-commit"))
	if err != nil {
		t.Fatal(err)
	}

	assertEvents(t, result.Events, []Phase{PhaseValidate, PhaseCompile, PhaseSnapshot, PhaseApply, PhaseHealthCheck, PhaseReadback, PhaseCommit}, []string{StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess}, "txn-commit")
	stored, err := store.AuditEvents(ctx, "txn-commit")
	if err != nil {
		t.Fatal(err)
	}
	assertEvents(t, stored, []Phase{PhaseValidate, PhaseCompile, PhaseSnapshot, PhaseApply, PhaseHealthCheck, PhaseReadback, PhaseCommit}, []string{StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess}, "txn-commit")
	snapshot, err := store.RuntimeSnapshot(ctx, "snapshot-txn-commit")
	if err != nil {
		t.Fatal(err)
	}
	var payload SnapshotPayload
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Receipt.TransactionID != "txn-commit" || payload.Readback.TransactionID != "txn-commit" || !payload.Readback.Fresh {
		t.Fatalf("persisted evidence = %#v, want complete transaction chain", payload)
	}
}

func TestExecutorEmitsRollbackAuditEvidenceAfterHealthCheckFailure(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, ctx)
	clock := deterministicClock(time.Date(2026, 6, 5, 15, 0, 0, 0, time.UTC))
	seedSnapshot(t, ctx, store, "snapshot-previous", "txn-previous")
	executor := Executor{
		Store: store,
		Now:   clock,
		Receipt: func(_ context.Context, plan Plan) (ApplyReceipt, error) {
			return ApplyReceipt{TransactionID: plan.Request.TransactionID, Status: ReceiptApplied, Capability: plan.Request.Resource, AppliedAt: clock()}, nil
		},
		Readback: func(_ context.Context, plan Plan) (Readback, error) {
			return Readback{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Timestamp: clock(), Fresh: true}, nil
		},
		HealthCheck: func(context.Context, Plan) error {
			return errors.New("simulated health check failure")
		},
		Rollback: func(_ context.Context, plan Plan) error {
			if !plan.Previous.Available || plan.Previous.ProxyEgress.ID != "proxy-media" || plan.Previous.FlowIntent.ID != "default" {
				return errors.New("prior snapshot was not supplied to rollback")
			}
			return nil
		},
	}
	request := validRequest("txn-health-failure")
	request.PreviousSnapshotID = "snapshot-previous"

	result, err := executor.Run(ctx, request)
	if err == nil {
		t.Fatal("Run succeeded after simulated health-check failure")
	}
	if result.Rollback.TargetSnapshotID != "snapshot-previous" || result.Rollback.Status != "succeeded" {
		t.Fatalf("rollback = %#v, want succeeded rollback tied to snapshot", result.Rollback)
	}

	assertEvents(t, result.Events, []Phase{PhaseValidate, PhaseCompile, PhaseSnapshot, PhaseApply, PhaseHealthCheck, PhaseRollback}, []string{StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess, StatusFailure, StatusRollback}, "txn-health-failure")
	storedRollback, err := store.Rollback(ctx, "rollback-txn-health-failure")
	if err != nil {
		t.Fatal(err)
	}
	if storedRollback.TargetSnapshotID != "snapshot-previous" || storedRollback.Status != "succeeded" || storedRollback.CompletedAt == nil {
		t.Fatalf("stored rollback = %#v, want completed rollback metadata", storedRollback)
	}
	if storedRollback.Reason != "health-check: simulated health check failure" || result.RollbackReceipt.Cause != storedRollback.Reason {
		t.Fatalf("rollback cause mismatch: metadata=%#v receipt=%#v", storedRollback, result.RollbackReceipt)
	}
	storedEvents, err := store.AuditEvents(ctx, "txn-health-failure")
	if err != nil {
		t.Fatal(err)
	}
	assertEvents(t, storedEvents, []Phase{PhaseValidate, PhaseCompile, PhaseSnapshot, PhaseApply, PhaseHealthCheck, PhaseRollback}, []string{StatusSuccess, StatusSuccess, StatusSuccess, StatusSuccess, StatusFailure, StatusRollback}, "txn-health-failure")
}

func TestExecutorRejectsMissingReceiptAndPreservesCommittedSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-previous", "txn-previous")
	executor := Executor{
		Store:    store,
		Now:      deterministicClock(time.Date(2026, 6, 5, 18, 0, 0, 0, time.UTC)),
		Rollback: func(context.Context, Plan) error { return nil },
	}
	request := validRequest("txn-missing-receipt")
	request.PreviousSnapshotID = "snapshot-previous"

	result, err := executor.Run(ctx, request)
	if err == nil || !errors.Is(err, ErrIncompleteEvidence) {
		t.Fatalf("Run error = %v, want incomplete evidence", err)
	}
	if result.Rollback.TargetSnapshotID != "snapshot-previous" || result.Rollback.Status != "succeeded" {
		t.Fatalf("rollback = %#v, want prior committed snapshot", result.Rollback)
	}
	if _, err := store.RuntimeSnapshot(ctx, request.SnapshotID); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("failed generation was persisted: %v", err)
	}
}

func TestExecutorRejectsStaleReadbackWithoutAdvancingGeneration(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-previous", "txn-previous")
	clock := deterministicClock(time.Date(2026, 6, 5, 19, 0, 0, 0, time.UTC))
	executor := Executor{
		Store: store,
		Now:   clock,
		Receipt: func(_ context.Context, plan Plan) (ApplyReceipt, error) {
			return ApplyReceipt{TransactionID: plan.Request.TransactionID, Status: ReceiptApplied, Capability: plan.Request.Resource, AppliedAt: clock()}, nil
		},
		Readback: func(_ context.Context, plan Plan) (Readback, error) {
			return Readback{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Timestamp: clock(), Fresh: false, Reason: "readback timestamp is stale"}, nil
		},
		Rollback: func(context.Context, Plan) error { return nil },
	}
	request := validRequest("txn-stale-readback")
	request.PreviousSnapshotID = "snapshot-previous"

	result, err := executor.Run(ctx, request)
	if err == nil || !errors.Is(err, ErrStaleReadback) {
		t.Fatalf("Run error = %v, want stale readback", err)
	}
	if result.Rollback.TargetSnapshotID != "snapshot-previous" {
		t.Fatalf("rollback target = %q, want snapshot-previous", result.Rollback.TargetSnapshotID)
	}
}

func TestExecutorDoesNotMarkRollbackSuccessfulWhenRollbackFails(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-previous", "txn-previous")
	executor := Executor{
		Store: store,
		Now:   deterministicClock(time.Date(2026, 6, 5, 16, 0, 0, 0, time.UTC)),
		Apply: func(context.Context, Plan) error {
			return errors.New("simulated apply failure")
		},
		Rollback: func(context.Context, Plan) error {
			return errors.New("simulated rollback failure")
		},
	}
	request := validRequest("txn-rollback-failure")
	request.PreviousSnapshotID = "snapshot-previous"

	result, err := executor.Run(ctx, request)
	if err == nil {
		t.Fatal("Run succeeded after simulated apply failure")
	}
	if result.Rollback.Status != "failed" || result.Rollback.Error == "" {
		t.Fatalf("rollback = %#v, want failed rollback with error", result.Rollback)
	}
	assertEvents(t, result.Events, []Phase{PhaseValidate, PhaseCompile, PhaseSnapshot, PhaseApply, PhaseRollback}, []string{StatusSuccess, StatusSuccess, StatusSuccess, StatusFailure, StatusFailure}, "txn-rollback-failure")
}

func TestExecutorFirstGenerationFailureCleansGatewayWithoutRollbackRecord(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	runner := &recordingGatewayRunner{}
	clock := deterministicClock(time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC))
	executor := gatewayPlanExecutor(store, clock)
	executor.Gateway = runner
	executor.Receipt = func(context.Context, Plan) (ApplyReceipt, error) {
		return ApplyReceipt{}, errors.New("receipt failed")
	}

	// When
	result, err := executor.Run(ctx, validRequest("txn-first-generation"))

	// Then
	if err == nil || !strings.Contains(err.Error(), "receipt failed") {
		t.Fatalf("Run error = %v, want receipt failure", err)
	}
	if !runner.rollbackCalled {
		t.Fatal("Gateway rollback was not called for first-generation failure")
	}
	if result.Rollback.Status != "succeeded" || result.Rollback.TargetSnapshotID != "" {
		t.Fatalf("rollback = %#v, want successful attempted-state cleanup without target", result.Rollback)
	}
	if _, err := store.Rollback(ctx, "rollback-txn-first-generation"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("rollback record = %v, want no empty-target rollback record", err)
	}
	assertEvents(t, result.Events, []Phase{PhaseValidate, PhaseCompile, PhaseSnapshot, PhaseApply, PhaseRollback}, []string{StatusSuccess, StatusSuccess, StatusSuccess, StatusFailure, StatusRollback}, "txn-first-generation")
}

func TestExecutorPersistsValidationFailureAuditEvent(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, ctx)
	request := validRequest("txn-validate-failure")
	request.ProxyEgress.ID = ""
	executor := Executor{Store: store, Now: deterministicClock(time.Date(2026, 6, 5, 17, 0, 0, 0, time.UTC))}

	result, err := executor.Run(ctx, request)
	if err == nil {
		t.Fatal("Run succeeded with invalid proxy egress")
	}
	assertEvents(t, result.Events, []Phase{PhaseValidate}, []string{StatusFailure}, "txn-validate-failure")
	storedEvents, err := store.AuditEvents(ctx, "txn-validate-failure")
	if err != nil {
		t.Fatal(err)
	}
	assertEvents(t, storedEvents, []Phase{PhaseValidate}, []string{StatusFailure}, "txn-validate-failure")
}

func openStore(t *testing.T, ctx context.Context) *persistence.Store {
	t.Helper()
	store, err := persistence.Open(ctx, filepath.Join(t.TempDir(), "lyroute.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func seedSnapshot(t *testing.T, ctx context.Context, store *persistence.Store, id, transactionID string) {
	t.Helper()
	request := validRequest(transactionID)
	payload, hash, err := persistence.MarshalPayload(struct {
		Proxy proxy.Egress `json:"proxy"`
		Flow  flow.Intent  `json:"flow"`
	}{Proxy: request.ProxyEgress, Flow: request.FlowIntent})
	if err != nil {
		t.Fatal(err)
	}
	err = store.SaveApply(ctx, persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: id, SourceTransactionID: transactionID, Payload: payload, PayloadHash: hash, CreatedAt: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
}

func validRequest(transactionID string) Request {
	return Request{
		TransactionID: transactionID,
		Actor:         "admin@example",
		Role:          "admin",
		Resource:      "runtime.apply",
		ProxyEgress:   proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound"),
		FlowIntent: flow.NewIntent("default", []flow.Rule{
			flow.NewRule("classify-video", flow.RuleGranularity, flow.Classify("video")),
		}),
		SnapshotID: "snapshot-" + transactionID,
		RollbackID: "rollback-" + transactionID,
	}
}

func deterministicClock(start time.Time) Clock {
	current := start.Add(-time.Second)
	return func() time.Time {
		current = current.Add(time.Second)
		return current
	}
}

func assertEvents(t *testing.T, events []persistence.AuditEvent, phases []Phase, statuses []string, transactionID string) {
	t.Helper()
	if len(events) != len(phases) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(phases), events)
	}
	for i, phase := range phases {
		if events[i].Action != string(phase) || events[i].Status != statuses[i] || events[i].TransactionID != transactionID {
			t.Fatalf("event %d = %#v, want action=%q status=%q transaction=%q", i, events[i], phase, statuses[i], transactionID)
		}
		if i > 0 && !events[i].Timestamp.After(events[i-1].Timestamp) {
			t.Fatalf("event %d timestamp = %s, want after %s", i, events[i].Timestamp, events[i-1].Timestamp)
		}
	}
}
