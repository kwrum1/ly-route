package apply

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReceiptRejectsMissingProviderTransactionID(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	now := time.Date(2026, 6, 5, 20, 0, 0, 0, time.UTC)
	executor := Executor{
		Store: store,
		Now:   func() time.Time { return now },
		Receipt: func(context.Context, Plan) (ApplyReceipt, error) {
			return ApplyReceipt{Capability: "runtime.apply", Status: ReceiptApplied, AppliedAt: now}, nil
		},
		Readback: func(context.Context, Plan) (Readback, error) {
			return Readback{TransactionID: "txn-missing-provider-id", Capability: "runtime.apply", Timestamp: now, Fresh: true}, nil
		},
	}

	// When
	_, err := executor.Run(ctx, validRequest("txn-missing-provider-id"))

	// Then
	if !errors.Is(err, ErrIncompleteEvidence) {
		t.Fatalf("Run error = %v, want incomplete provider receipt", err)
	}
}

func TestReadbackRejectsMismatchedCapability(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	now := time.Date(2026, 6, 5, 20, 30, 0, 0, time.UTC)
	executor := Executor{
		Store: store,
		Now:   func() time.Time { return now },
		Receipt: func(_ context.Context, plan Plan) (ApplyReceipt, error) {
			return ApplyReceipt{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Status: ReceiptApplied, AppliedAt: now}, nil
		},
		Readback: func(_ context.Context, plan Plan) (Readback, error) {
			return Readback{TransactionID: plan.Request.TransactionID, Capability: "other-capability", Timestamp: now, Fresh: true}, nil
		},
	}

	// When
	_, err := executor.Run(ctx, validRequest("txn-readback-capability"))

	// Then
	if !errors.Is(err, ErrIncompleteEvidence) {
		t.Fatalf("Run error = %v, want mismatched capability rejection", err)
	}
}

func TestRollbackReceiptCarriesExactApplyCause(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-previous", "txn-previous")
	now := time.Date(2026, 6, 5, 21, 0, 0, 0, time.UTC)
	cause := errors.New("vpp command 4 failed: permission denied")
	executor := Executor{
		Store:    store,
		Now:      func() time.Time { return now },
		Apply:    func(context.Context, Plan) error { return cause },
		Rollback: func(context.Context, Plan) error { return nil },
	}
	request := validRequest("txn-rollback-receipt")
	request.PreviousSnapshotID = "snapshot-previous"

	// When
	result, err := executor.Run(ctx, request)

	// Then
	if !errors.Is(err, cause) {
		t.Fatalf("Run error = %v, want %v", err, cause)
	}
	if result.RollbackReceipt.TransactionID != request.TransactionID || result.RollbackReceipt.TargetSnapshotID != request.PreviousSnapshotID {
		t.Fatalf("rollback receipt identity = %#v", result.RollbackReceipt)
	}
	if result.RollbackReceipt.Status != ReceiptRolledBack || result.RollbackReceipt.Cause != "apply: "+cause.Error() || result.RollbackReceipt.Capability != request.Resource || result.RollbackReceipt.Timestamp.IsZero() {
		t.Fatalf("rollback receipt = %#v, want exact apply cause", result.RollbackReceipt)
	}
}
