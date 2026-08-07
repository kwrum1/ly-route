package apply

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/vpp"
)

func TestGatewayTransactionReturnsTypedEvidenceForEveryApplicableResource(t *testing.T) {
	// Given
	clock := deterministicClock(time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, _ := gatewayEvidenceAdapters(clock, &trace, "")
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}

	// When
	result, err := transaction.Run(context.Background(), Plan{Request: Request{TransactionID: "txn-evidence-all", Resource: "runtime.apply"}})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != len(gatewayResourceOrder) {
		t.Fatalf("evidence count = %d, want %d", len(result.Evidence), len(gatewayResourceOrder))
	}
	for index, evidence := range result.Evidence {
		if evidence.Resource != gatewayResourceOrder[index] || evidence.Capability != gatewayResourceOrder[index] || evidence.TransactionID != "txn-evidence-all" {
			t.Fatalf("evidence[%d] identity = %#v", index, evidence)
		}
		if evidence.ApplyReceipt.Capability != evidence.Resource || evidence.Readback.Capability != evidence.Resource {
			t.Fatalf("evidence[%d] typed resource identity = %#v", index, evidence)
		}
		if evidence.BeforeHash == "" || evidence.AfterHash == "" || evidence.Before.Hash == "" || evidence.After.Hash == "" {
			t.Fatalf("evidence[%d] snapshot hashes = %#v", index, evidence)
		}
	}
}

type gatewayEvidenceAdapter struct {
	name    string
	clock   Clock
	trace   *[]string
	deleted bool
	fail    bool
}

func (adapter *gatewayEvidenceAdapter) Name() string { return adapter.name }

func (adapter *gatewayEvidenceAdapter) Apply(_ context.Context, plan Plan) (GatewayResourceResult, error) {
	*adapter.trace = append(*adapter.trace, adapter.name)
	if adapter.fail {
		return GatewayResourceResult{}, errors.New("gateway evidence apply failed")
	}
	before := vpp.Snapshot{TransactionID: plan.Request.TransactionID, RequestID: plan.Request.TransactionID}
	after := before
	return GatewayResourceResult{
		Receipt:  ApplyReceipt{TransactionID: plan.Request.TransactionID, Capability: adapter.name, Status: ReceiptApplied, AppliedAt: adapter.clock()},
		Readback: Readback{TransactionID: plan.Request.TransactionID, Capability: adapter.name, Timestamp: adapter.clock(), Fresh: true},
		Deleted:  adapter.deleted, Before: before, After: after,
	}, nil
}

func (adapter *gatewayEvidenceAdapter) Delete(_ context.Context, plan Plan) (bool, error) {
	if !adapter.deleted {
		return false, nil
	}
	return true, nil
}

func (adapter *gatewayEvidenceAdapter) Rollback(_ context.Context, _ Plan) error { return nil }

func gatewayEvidenceAdapters(clock Clock, trace *[]string, failure string) ([]GatewayResourceAdapter, []*gatewayEvidenceAdapter) {
	adapters := make([]GatewayResourceAdapter, 0, len(gatewayResourceOrder))
	concrete := make([]*gatewayEvidenceAdapter, 0, len(gatewayResourceOrder))
	for _, name := range gatewayResourceOrder {
		adapter := &gatewayEvidenceAdapter{name: name, clock: clock, trace: trace, fail: name == failure}
		adapters = append(adapters, adapter)
		concrete = append(concrete, adapter)
	}
	return adapters, concrete
}

func gatewayEvidenceExecutor(store *persistence.Store, clock Clock, transaction *GatewayMultiResourceTransaction) Executor {
	return gatewayFailureExecutor(store, clock, transaction)
}

func marshalSnapshotPayload(payload SnapshotPayload) ([]byte, string, error) {
	bytes, hash, err := persistence.MarshalPayload(payload)
	return []byte(bytes), hash, err
}

func unmarshalSnapshotPayload(payload []byte, target *SnapshotPayload) error {
	return json.Unmarshal(payload, target)
}

func runtimeSnapshotRecord(id, transactionID string, payload []byte, hash string) persistence.ApplyRecord {
	return persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: id, SourceTransactionID: transactionID, Payload: payload, PayloadHash: hash, CreatedAt: time.Now().UTC()}}
}

func TestGatewayTransactionReturnsDeletionOnlyEvidence(t *testing.T) {
	// Given
	clock := deterministicClock(time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, concrete := gatewayEvidenceAdapters(clock, &trace, "")
	concrete[0].deleted = true
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}

	// When
	result, err := transaction.Run(context.Background(), Plan{Request: Request{TransactionID: "txn-evidence-delete", Resource: "runtime.apply"}})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != len(gatewayResourceOrder) || !result.Evidence[0].Deleted {
		t.Fatalf("evidence = %#v, want deletion-only resource evidence", result.Evidence)
	}
}

func TestExecutorPersistsGatewayEvidenceAndRejectsWrongOrMissingSnapshotEvidence(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	clock := deterministicClock(time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, _ := gatewayEvidenceAdapters(clock, &trace, "")
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}
	executor := gatewayEvidenceExecutor(store, clock, transaction)

	// When
	request := validRequest("txn-evidence-persist")
	result, err := executor.Run(ctx, request)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.RuntimeSnapshot(ctx, request.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	var payload SnapshotPayload
	if err := unmarshalSnapshotPayload(snapshot.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.GatewayEvidence) != len(gatewayResourceOrder) || len(result.GatewayResult.Evidence) != len(gatewayResourceOrder) {
		t.Fatalf("persisted/result evidence = %d/%d", len(payload.GatewayEvidence), len(result.GatewayResult.Evidence))
	}

	for _, malformed := range []struct {
		name string
		edit func(*SnapshotPayload)
	}{
		{name: "wrong transaction", edit: func(payload *SnapshotPayload) { payload.GatewayEvidence[0].TransactionID = "wrong" }},
		{name: "missing hash", edit: func(payload *SnapshotPayload) { payload.GatewayEvidence[0].BeforeHash = "" }},
		{name: "wrong hash", edit: func(payload *SnapshotPayload) { payload.GatewayEvidence[0].AfterHash = "wrong" }},
	} {
		t.Run(malformed.name, func(t *testing.T) {
			candidate := cloneSnapshotPayload(t, payload)
			malformed.edit(&candidate)
			bytes, hash, marshalErr := marshalSnapshotPayload(candidate)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			badID := "snapshot-" + malformed.name
			if saveErr := store.SaveApply(ctx, runtimeSnapshotRecord(badID, request.TransactionID, bytes, hash)); saveErr != nil {
				t.Fatal(saveErr)
			}
			if _, loadErr := executor.previousState(ctx, badID); !errors.Is(loadErr, ErrInvalidGatewayEvidence) {
				t.Fatalf("load error = %v, want invalid evidence", loadErr)
			}
		})
	}
}

func TestExecutorAcceptsAgedCommittedGatewayEvidenceAsPreviousGeneration(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, ctx)
	committedAt := time.Date(2026, 7, 26, 12, 30, 0, 0, time.UTC)
	commitClock := deterministicClock(committedAt)
	trace := []string{}
	adapters, _ := gatewayEvidenceAdapters(commitClock, &trace, "")
	executor := gatewayEvidenceExecutor(store, commitClock, &GatewayMultiResourceTransaction{Adapters: adapters, Now: commitClock})
	request := validRequest("txn-evidence-aged")
	if _, err := executor.Run(ctx, request); err != nil {
		t.Fatal(err)
	}

	executor.Now = deterministicClock(committedAt.Add(ReadbackFreshnessWindow + time.Hour))
	previous, err := executor.previousState(ctx, request.SnapshotID)
	if err != nil {
		t.Fatalf("aged committed generation was rejected as rollback state: %v", err)
	}
	if !previous.Available {
		t.Fatal("aged committed generation is not available as rollback state")
	}
}

func TestExecutorDoesNotPersistFailedGatewayCandidateEvidence(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-evidence-prior", "txn-evidence-prior")
	clock := deterministicClock(time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, _ := gatewayEvidenceAdapters(clock, &trace, "routes")
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}
	executor := gatewayEvidenceExecutor(store, clock, transaction)
	request := validRequest("txn-evidence-failed")
	request.PreviousSnapshotID = "snapshot-evidence-prior"

	// When
	result, err := executor.Run(ctx, request)

	// Then
	if err == nil || len(result.GatewayResult.Evidence) != 0 {
		t.Fatalf("result = %#v, error = %v, want failed candidate without persisted evidence", result, err)
	}
	if _, loadErr := store.RuntimeSnapshot(ctx, request.SnapshotID); !errors.Is(loadErr, persistence.ErrNotFound) {
		t.Fatalf("failed candidate snapshot = %v, want absent", loadErr)
	}
}

func TestExecutorFailurePreservesPriorEvidenceBytesAndHash(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	clock := deterministicClock(time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC))
	trace := []string{}
	adapters, concrete := gatewayEvidenceAdapters(clock, &trace, "")
	transaction := &GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}
	executor := gatewayEvidenceExecutor(store, clock, transaction)
	first := validRequest("txn-evidence-prior")

	// When
	if _, err := executor.Run(ctx, first); err != nil {
		t.Fatal(err)
	}
	prior, err := store.RuntimeSnapshot(ctx, first.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	previous := snapshotBytes{payload: append([]byte(nil), prior.Payload...), hash: prior.PayloadHash}
	concrete[3].fail = true
	second := validRequest("txn-evidence-candidate")
	second.PreviousSnapshotID = first.SnapshotID
	result, runErr := executor.Run(ctx, second)

	// Then
	if runErr == nil || len(result.GatewayResult.Evidence) != 0 {
		t.Fatalf("result = %#v, error = %v, want failed candidate without evidence", result, runErr)
	}
	assertSnapshotUnchanged(t, ctx, store, first.SnapshotID, previous)
	if _, loadErr := store.RuntimeSnapshot(ctx, second.SnapshotID); !errors.Is(loadErr, persistence.ErrNotFound) {
		t.Fatalf("failed candidate snapshot = %v, want absent", loadErr)
	}
}
