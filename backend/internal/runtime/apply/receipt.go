package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/vpp"
)

var (
	ErrIncompleteEvidence = errors.New("apply: incomplete receipt or readback evidence")
	ErrStaleReadback      = errors.New("apply: stale readback evidence")
)

const ReadbackFreshnessWindow = 5 * time.Minute

const (
	ReceiptMissing    = "missing"
	ReceiptApplied    = "applied"
	ReceiptRolledBack = "rolled_back"
	ReceiptFailed     = "failed"
	ReceiptDegraded   = "degraded"
)

type ApplyReceipt struct {
	TransactionID string    `json:"transaction_id"`
	Capability    string    `json:"capability"`
	Status        string    `json:"status"`
	AppliedAt     time.Time `json:"applied_at"`
	Cause         string    `json:"cause,omitempty"`
}

type Readback struct {
	TransactionID string    `json:"transaction_id"`
	Capability    string    `json:"capability"`
	Timestamp     time.Time `json:"timestamp"`
	Fresh         bool      `json:"fresh"`
	Reason        string    `json:"reason,omitempty"`
}

type RollbackReceipt struct {
	TransactionID    string    `json:"transaction_id"`
	Capability       string    `json:"affected_capability"`
	TargetSnapshotID string    `json:"target_snapshot_id"`
	Status           string    `json:"status"`
	Cause            string    `json:"cause"`
	Timestamp        time.Time `json:"timestamp"`
	RollbackError    string    `json:"rollback_error,omitempty"`
}

type ReconciliationReceipt struct {
	TransactionID string    `json:"transaction_id"`
	Capability    string    `json:"affected_capability"`
	Status        string    `json:"status"`
	Cause         string    `json:"cause"`
	Timestamp     time.Time `json:"timestamp"`
}

type CapabilityFailureEvidence struct {
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type ReceiptFunc func(context.Context, Plan) (ApplyReceipt, error)
type ReadbackFunc func(context.Context, Plan) (Readback, error)

type SnapshotPayload struct {
	Proxy              proxy.Egress                `json:"proxy"`
	Flow               flow.Intent                 `json:"flow"`
	GatewayPlan        *vpp.Plan                   `json:"gateway_plan,omitempty"`
	Receipt            ApplyReceipt                `json:"apply_receipt"`
	Readback           Readback                    `json:"readback"`
	GatewayEvidence    []GatewayResourceEvidence   `json:"gateway_evidence,omitempty"`
	CapabilityFailures []CapabilityFailureEvidence `json:"capability_failures,omitempty"`
}

func (receipt ApplyReceipt) validate(request Request, now time.Time) error {
	if receipt.TransactionID != request.TransactionID || strings.TrimSpace(receipt.Capability) == "" || receipt.Capability != request.Resource || receipt.Status != ReceiptApplied || receipt.AppliedAt.IsZero() || receipt.AppliedAt.After(now) {
		return fmt.Errorf("%w: transaction=%q capability=%q status=%q", ErrIncompleteEvidence, receipt.TransactionID, receipt.Capability, receipt.Status)
	}
	return nil
}

func (readback Readback) validate(request Request, now time.Time) error {
	if readback.TransactionID != request.TransactionID || strings.TrimSpace(readback.Capability) == "" || readback.Capability != request.Resource || readback.Timestamp.IsZero() {
		return fmt.Errorf("%w: transaction=%q capability=%q", ErrIncompleteEvidence, readback.TransactionID, readback.Capability)
	}
	if !readback.Fresh || now.Sub(readback.Timestamp) < 0 || now.Sub(readback.Timestamp) > ReadbackFreshnessWindow {
		return fmt.Errorf("%w: timestamp=%s reason=%q", ErrStaleReadback, readback.Timestamp.UTC().Format(time.RFC3339Nano), readback.Reason)
	}
	return nil
}
