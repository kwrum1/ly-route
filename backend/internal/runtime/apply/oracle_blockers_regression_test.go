package apply

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

func TestProductionGatewayFirstGenerationUsesEmptyLivePriorAndCommits(t *testing.T) {
	// Given
	client := &desiredLiveClient{state: vpp.Snapshot{}}
	transaction := NewProductionGatewayTransaction(vpp.Adapter{Client: client}, deterministicClock(time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)))
	plan := Plan{Request: Request{TransactionID: "txn-first-generation", Resource: "/api/v1/runtime/apply"}, GatewayPlan: vpp.Plan{}}

	// When
	result, err := transaction.Run(context.Background(), plan)

	// Then
	if err != nil {
		t.Fatalf("first-generation transaction failed: %v", err)
	}
	if result.Rollback.Status != ReceiptApplied {
		t.Fatalf("first-generation result = %#v, want committed transaction", result)
	}
}

func TestGatewayMixedDeleteApplyFailureCleansReverseThenRestoresForward(t *testing.T) {
	// Given
	clock := deterministicClock(time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters := make([]GatewayResourceAdapter, 0, len(gatewayResourceOrder))
	for _, name := range gatewayResourceOrder {
		adapters = append(adapters, &mixedRollbackAdapter{name: name, clock: clock, trace: &trace, failApply: name == "qos"})
	}
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}

	// When
	_, err := transaction.Run(context.Background(), Plan{Request: Request{TransactionID: "txn-mixed", Resource: "/api/v1/runtime/apply"}})

	// Then
	if !errors.Is(err, ErrGatewayTransactionFailed) {
		t.Fatalf("error = %v, want failed mixed transaction", err)
	}
	want := []string{
		"delete:port-maps", "delete:nat44", "delete:qos", "delete:acls", "delete:routes", "delete:wan-groups", "delete:bonds", "delete:interfaces",
		"apply:interfaces", "apply:bonds", "apply:wan-groups", "apply:routes", "apply:acls", "apply:qos",
		"rollback-cleanup:qos", "rollback-cleanup:acls", "rollback-cleanup:routes", "rollback-cleanup:wan-groups", "rollback-cleanup:bonds", "rollback-cleanup:interfaces",
		"rollback-restore:interfaces", "rollback-restore:bonds", "rollback-restore:wan-groups", "rollback-restore:routes", "rollback-restore:acls", "rollback-restore:qos", "rollback-restore:nat44", "rollback-restore:port-maps",
	}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("mixed rollback trace = %#v, want %#v", trace, want)
	}
}

func TestGatewayCommittedTransactionLaterRollbackCleansReverseThenRestoresForward(t *testing.T) {
	// Given
	clock := deterministicClock(time.Date(2026, 7, 27, 11, 30, 0, 0, time.UTC))
	trace := []string{}
	adapters := make([]GatewayResourceAdapter, 0, len(gatewayResourceOrder))
	for _, name := range gatewayResourceOrder {
		adapters = append(adapters, &mixedRollbackAdapter{name: name, clock: clock, trace: &trace})
	}
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}
	plan := Plan{Request: Request{TransactionID: "txn-later-rollback", Resource: "/api/v1/runtime/apply"}}
	if _, err := transaction.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	trace = nil

	// When
	err := transaction.Rollback(context.Background(), plan)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"rollback-cleanup:port-maps", "rollback-cleanup:nat44", "rollback-cleanup:qos", "rollback-cleanup:acls", "rollback-cleanup:routes", "rollback-cleanup:wan-groups", "rollback-cleanup:bonds", "rollback-cleanup:interfaces",
		"rollback-restore:interfaces", "rollback-restore:bonds", "rollback-restore:wan-groups", "rollback-restore:routes", "rollback-restore:acls", "rollback-restore:qos", "rollback-restore:nat44", "rollback-restore:port-maps",
	}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("later rollback trace = %#v, want %#v", trace, want)
	}
}

type mixedRollbackAdapter struct {
	name      string
	clock     Clock
	trace     *[]string
	failApply bool
}

func (adapter *mixedRollbackAdapter) Name() string { return adapter.name }

func (adapter *mixedRollbackAdapter) Delete(context.Context, Plan) (bool, error) {
	*adapter.trace = append(*adapter.trace, "delete:"+adapter.name)
	return true, nil
}

func (adapter *mixedRollbackAdapter) Apply(_ context.Context, plan Plan) (GatewayResourceResult, error) {
	*adapter.trace = append(*adapter.trace, "apply:"+adapter.name)
	if adapter.failApply {
		return GatewayResourceResult{}, errors.New("injected apply failure")
	}
	return GatewayResourceResult{
		Receipt:  ApplyReceipt{TransactionID: plan.Request.TransactionID, Capability: adapter.name, Status: ReceiptApplied, AppliedAt: adapter.clock()},
		Readback: Readback{TransactionID: plan.Request.TransactionID, Capability: adapter.name, Timestamp: adapter.clock(), Fresh: true},
	}, nil
}

func (adapter *mixedRollbackAdapter) Rollback(context.Context, Plan) error {
	*adapter.trace = append(*adapter.trace, "legacy-rollback:"+adapter.name)
	return nil
}

func (adapter *mixedRollbackAdapter) RollbackCleanup(context.Context, Plan) error {
	*adapter.trace = append(*adapter.trace, "rollback-cleanup:"+adapter.name)
	return nil
}

func (adapter *mixedRollbackAdapter) RollbackRestore(context.Context, Plan) error {
	*adapter.trace = append(*adapter.trace, "rollback-restore:"+adapter.name)
	return nil
}
