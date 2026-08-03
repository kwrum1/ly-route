package apply

import (
	"errors"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

var ErrInvalidGatewayEvidence = errors.New("apply: invalid gateway resource evidence")

type GatewayResourceEvidence struct {
	Resource             string                              `json:"resource"`
	Capability           string                              `json:"capability"`
	TransactionID        string                              `json:"transaction_id"`
	ApplyReceipt         ApplyReceipt                        `json:"apply_receipt"`
	Readback             Readback                            `json:"readback"`
	Deleted              bool                                `json:"deleted"`
	Before               vpp.Snapshot                        `json:"before"`
	BeforeHash           string                              `json:"before_hash"`
	After                vpp.Snapshot                        `json:"after"`
	AfterHash            string                              `json:"after_hash"`
	SupplementalReadback []vpp.SupplementalOperationReadback `json:"supplemental_readback,omitempty"`
}

func gatewayEvidence(result GatewayResourceResult, resource, transactionID string, now time.Time) (GatewayResourceEvidence, error) {
	before, err := completeGatewaySnapshot(result.Before, transactionID, now)
	if err != nil {
		return GatewayResourceEvidence{}, err
	}
	after, err := completeGatewaySnapshot(result.After, transactionID, now)
	if err != nil {
		return GatewayResourceEvidence{}, err
	}
	if err := vpp.VerifySnapshotHash(before); err != nil {
		return GatewayResourceEvidence{}, err
	}
	if err := vpp.VerifySnapshotHash(after); err != nil {
		return GatewayResourceEvidence{}, err
	}
	return GatewayResourceEvidence{
		Resource: resource, Capability: resource, TransactionID: transactionID,
		ApplyReceipt: result.Receipt, Readback: result.Readback, Deleted: result.Deleted,
		Before: before, BeforeHash: before.Hash, After: after, AfterHash: after.Hash, SupplementalReadback: result.SupplementalReadback,
	}, nil
}

func completeGatewaySnapshot(snapshot vpp.Snapshot, transactionID string, now time.Time) (vpp.Snapshot, error) {
	if snapshot.TransactionID == "" {
		snapshot.TransactionID = transactionID
	}
	if snapshot.RequestID == "" {
		snapshot.RequestID = transactionID
	}
	if snapshot.ReadbackAt.IsZero() {
		snapshot.ReadbackAt = now
	}
	if snapshot.Hash == "" {
		hash, err := vpp.CanonicalSnapshotHash(snapshot)
		if err != nil {
			return vpp.Snapshot{}, err
		}
		snapshot.Hash = hash
	}
	return snapshot, nil
}
