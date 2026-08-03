package apply

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/vpp"
)

func TestGatewayMultiResourceTransaction_appliesInDependencyOrderAndRollsBackInReverse(t *testing.T) {
	// Given
	clock := deterministicClock(time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, _ := gatewayTestAdapters(clock, &trace, "")
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}

	// When
	result, err := transaction.Run(context.Background(), Plan{Request: Request{TransactionID: "txn-7g", Resource: "/api/v1/config/apply"}})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"interfaces", "bonds", "wan-groups", "routes", "acls", "qos", "nat44", "port-maps"}
	if !reflect.DeepEqual(result.Order, wantOrder) || !reflect.DeepEqual(trace, wantOrder) {
		t.Fatalf("apply order = %#v, trace = %#v, want %#v", result.Order, trace, wantOrder)
	}
	if len(result.Receipts) != len(wantOrder) || len(result.Readbacks) != len(wantOrder) {
		t.Fatalf("evidence lengths = %d/%d, want %d/%d", len(result.Receipts), len(result.Readbacks), len(wantOrder), len(wantOrder))
	}
}

func TestGatewayMultiResourceTransaction_rollsBackEarlierResourcesWhenMiddleApplyFails(t *testing.T) {
	// Given
	clock := deterministicClock(time.Date(2026, 7, 23, 13, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, concrete := gatewayTestAdapters(clock, &trace, "routes")
	concrete[1].rollbackErr = errors.New("bond rollback failed")
	concrete[0].rollbackErr = errors.New("interface rollback failed")
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}

	// When
	result, err := transaction.Run(context.Background(), Plan{Request: Request{TransactionID: "txn-7g-fail", Resource: "/api/v1/config/apply"}})

	// Then
	if err == nil || !errors.Is(err, ErrGatewayTransactionFailed) {
		t.Fatalf("error = %v, want gateway transaction failure", err)
	}
	wantTrace := []string{"interfaces", "bonds", "wan-groups", "routes", "rollback:wan-groups", "rollback:bonds", "rollback:interfaces"}
	if !reflect.DeepEqual(trace, wantTrace) {
		t.Fatalf("trace = %#v, want %#v", trace, wantTrace)
	}
	if result.Rollback.Status != ReceiptFailed || result.Rollback.RollbackError != "rollback bonds: bond rollback failed\nrollback interfaces: interface rollback failed" {
		t.Fatalf("rollback = %#v, want deterministic aggregate failure", result.Rollback)
	}
}

func TestGatewayMultiResourceTransaction_rejectsReadbackMismatchAndReportsRollbackFailure(t *testing.T) {
	// Given
	clock := deterministicClock(time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, concrete := gatewayTestAdapters(clock, &trace, "qos")
	concrete[5].fail = false
	concrete[4].rollbackErr = errors.New("rollback command failed")
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}

	// When
	result, err := transaction.Run(context.Background(), Plan{Request: Request{TransactionID: "txn-7g-readback", Resource: "/api/v1/config/apply"}})

	// Then
	if err == nil || !errors.Is(err, ErrGatewayTransactionFailed) {
		t.Fatalf("error = %v, want gateway transaction failure", err)
	}
	if result.Rollback.Status != ReceiptFailed || result.Rollback.RollbackError != "rollback acls: rollback command failed" {
		t.Fatalf("rollback = %#v, want failed command receipt", result.Rollback)
	}
}

func TestGatewayMultiResourceTransaction_includesDeletionInAdapterContract(t *testing.T) {
	// Given
	clock := deterministicClock(time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, concrete := gatewayTestAdapters(clock, &trace, "")
	concrete[0].deleted = true
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}

	// When
	result, err := transaction.Run(context.Background(), Plan{Request: Request{TransactionID: "txn-7g-delete", Resource: "/api/v1/config/apply"}})

	// Then
	if err != nil || !result.Deletions["interfaces"] {
		t.Fatalf("result = %#v, want interfaces deletion in transaction", result)
	}
}

func TestGatewayMultiResourceTransaction_failureKeepsPreviousStoreGeneration(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	seedGatewayTransactionSnapshot(t, ctx, store, "snapshot-previous", "txn-previous")
	previous := previousSnapshotBytes(t, ctx, store, "snapshot-previous")
	clock := deterministicClock(time.Date(2026, 7, 23, 16, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, _ := gatewayTestAdapters(clock, &trace, "routes")
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}
	executor := Executor{
		Store:   store,
		Now:     clock,
		Gateway: transaction,
		Receipt: func(_ context.Context, plan Plan) (ApplyReceipt, error) {
			return ApplyReceipt{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Status: ReceiptApplied, AppliedAt: clock()}, nil
		},
		Readback: func(_ context.Context, plan Plan) (Readback, error) {
			return Readback{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Timestamp: clock(), Fresh: true}, nil
		},
		Rollback: func(ctx context.Context, plan Plan) error { return transaction.Rollback(ctx, plan) },
	}
	request := validRequest("txn-7g-generation")
	request.PreviousSnapshotID = "snapshot-previous"

	// When
	result, err := executor.Run(ctx, request)

	// Then
	if err == nil || result.Rollback.Status != "succeeded" {
		t.Fatalf("result = %#v, error = %v, want rolled back failure", result, err)
	}
	assertSnapshotUnchanged(t, ctx, store, "snapshot-previous", previous)
	if _, err := store.RuntimeSnapshot(ctx, request.SnapshotID); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("failed generation = %v, want not found", err)
	}
}

func TestGatewayMultiResourceTransaction_preservesPriorGenerationBytesAfterReadbackAndRollbackFailure(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		rollbackFailure bool
	}{
		{name: "readback", rollbackFailure: false},
		{name: "rollback failure", rollbackFailure: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Given
			ctx := context.Background()
			store := openStore(t, ctx)
			seedGatewayTransactionSnapshot(t, ctx, store, "snapshot-previous", "txn-previous")
			previous := previousSnapshotBytes(t, ctx, store, "snapshot-previous")
			clock := deterministicClock(time.Date(2026, 7, 23, 17, 0, 0, 0, time.UTC))
			trace := []string{}
			adapters, concrete := gatewayTestAdapters(clock, &trace, "qos")
			concrete[5].fail = false
			if scenario.rollbackFailure {
				concrete[4].rollbackErr = errors.New("acl rollback failed")
			}
			transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}
			executor := gatewayFailureExecutor(store, clock, transaction)
			request := validRequest("txn-7g-" + scenario.name)
			request.PreviousSnapshotID = "snapshot-previous"

			// When
			_, err := executor.Run(ctx, request)

			// Then
			if err == nil {
				t.Fatal("transaction succeeded after simulated failure")
			}
			wantTrace := []string{"interfaces", "bonds", "wan-groups", "routes", "acls", "qos", "rollback:acls", "rollback:routes", "rollback:wan-groups", "rollback:bonds", "rollback:interfaces"}
			if !reflect.DeepEqual(trace, wantTrace) {
				t.Fatalf("trace = %#v, want exact readback rollback %v", trace, wantTrace)
			}
			assertSnapshotUnchanged(t, ctx, store, "snapshot-previous", previous)
		})
	}
}

func seedGatewayTransactionSnapshot(t *testing.T, ctx context.Context, store *persistence.Store, id, transactionID string) {
	t.Helper()
	request := validRequest(transactionID)
	payload, hash, err := persistence.MarshalPayload(SnapshotPayload{Proxy: request.ProxyEgress, Flow: request.FlowIntent, GatewayPlan: &vpp.Plan{}})
	if err != nil {
		t.Fatal(err)
	}
	err = store.SaveApply(ctx, persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: id, SourceTransactionID: transactionID, Payload: payload, PayloadHash: hash, CreatedAt: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
}

func gatewayFailureExecutor(store *persistence.Store, clock Clock, transaction *GatewayMultiResourceTransaction) Executor {
	return Executor{
		Store:   store,
		Now:     clock,
		Gateway: transaction,
		Receipt: func(_ context.Context, plan Plan) (ApplyReceipt, error) {
			return ApplyReceipt{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Status: ReceiptApplied, AppliedAt: clock()}, nil
		},
		Readback: func(_ context.Context, plan Plan) (Readback, error) {
			return Readback{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Timestamp: clock(), Fresh: true}, nil
		},
		Rollback: func(ctx context.Context, plan Plan) error { return transaction.Rollback(ctx, plan) },
	}
}

type snapshotBytes struct {
	payload []byte
	hash    string
}

func previousSnapshotBytes(t *testing.T, ctx context.Context, store *persistence.Store, id string) snapshotBytes {
	t.Helper()
	snapshot, err := store.RuntimeSnapshot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return snapshotBytes{payload: append([]byte(nil), snapshot.Payload...), hash: snapshot.PayloadHash}
}

func assertSnapshotUnchanged(t *testing.T, ctx context.Context, store *persistence.Store, id string, previous snapshotBytes) {
	t.Helper()
	snapshot, err := store.RuntimeSnapshot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]byte(snapshot.Payload), previous.payload) || snapshot.PayloadHash != previous.hash {
		t.Fatalf("snapshot changed: payload=%q hash=%q, want payload=%q hash=%q", snapshot.Payload, snapshot.PayloadHash, previous.payload, previous.hash)
	}
}

type gatewayTestAdapter struct {
	name        string
	clock       Clock
	trace       *[]string
	fail        bool
	badReadback bool
	rollbackErr error
	deleted     bool
}

func (adapter *gatewayTestAdapter) Name() string { return adapter.name }

func (adapter *gatewayTestAdapter) Apply(_ context.Context, plan Plan) (GatewayResourceResult, error) {
	*adapter.trace = append(*adapter.trace, adapter.name)
	if adapter.fail {
		return GatewayResourceResult{}, errors.New("middle resource apply failed")
	}
	receipt := ApplyReceipt{TransactionID: plan.Request.TransactionID, Capability: adapter.name, Status: ReceiptApplied, AppliedAt: adapter.clock()}
	readback := Readback{TransactionID: plan.Request.TransactionID, Capability: adapter.name, Timestamp: adapter.clock(), Fresh: !adapter.badReadback}
	return GatewayResourceResult{Receipt: receipt, Readback: readback, Deleted: adapter.deleted}, nil
}

func (adapter *gatewayTestAdapter) Rollback(_ context.Context, _ Plan) error {
	*adapter.trace = append(*adapter.trace, "rollback:"+adapter.name)
	return adapter.rollbackErr
}

func gatewayTestAdapters(clock Clock, trace *[]string, failure string) ([]GatewayResourceAdapter, []*gatewayTestAdapter) {
	names := []string{"interfaces", "bonds", "routes", "wan-groups", "acls", "qos", "nat44", "port-maps"}
	adapters := make([]GatewayResourceAdapter, 0, len(names))
	concrete := make([]*gatewayTestAdapter, 0, len(names))
	for _, name := range names {
		adapter := &gatewayTestAdapter{name: name, clock: clock, trace: trace, fail: name == failure, badReadback: name == failure && failure == "qos"}
		adapters = append(adapters, adapter)
		concrete = append(concrete, adapter)
	}
	return adapters, concrete
}
