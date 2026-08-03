package apply

import (
	"fmt"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

type GatewayEvidenceValidationCode string

const (
	GatewayEvidenceIncomplete          GatewayEvidenceValidationCode = "incomplete"
	GatewayEvidenceCapabilityMismatch  GatewayEvidenceValidationCode = "capability_mismatch"
	GatewayEvidenceTransactionMismatch GatewayEvidenceValidationCode = "transaction_mismatch"
	GatewayEvidenceReceiptMismatch     GatewayEvidenceValidationCode = "receipt_mismatch"
	GatewayEvidenceReceiptInvalid      GatewayEvidenceValidationCode = "receipt_invalid"
	GatewayEvidenceReadbackMismatch    GatewayEvidenceValidationCode = "readback_mismatch"
	GatewayEvidenceReadbackStale       GatewayEvidenceValidationCode = "readback_stale"
	GatewayEvidenceSnapshotMismatch    GatewayEvidenceValidationCode = "snapshot_mismatch"
	GatewayEvidenceSnapshotHash        GatewayEvidenceValidationCode = "snapshot_hash"
)

type GatewayEvidenceValidationError struct {
	Code     GatewayEvidenceValidationCode
	Resource string
	Field    string
	Expected string
	Actual   string
}

func (err *GatewayEvidenceValidationError) Error() string {
	return fmt.Sprintf("%s: resource=%q field=%q expected=%q actual=%q", err.Code, err.Resource, err.Field, err.Expected, err.Actual)
}

func (err *GatewayEvidenceValidationError) Unwrap() error { return ErrInvalidGatewayEvidence }

func evidenceValidationError(code GatewayEvidenceValidationCode, item GatewayResourceEvidence, field, expected, actual string) error {
	return &GatewayEvidenceValidationError{Code: code, Resource: item.Resource, Field: field, Expected: expected, Actual: actual}
}

func validateGatewayEvidence(evidence []GatewayResourceEvidence, transactionID string, now time.Time) error {
	for _, item := range evidence {
		if item.Resource == "" {
			return evidenceValidationError(GatewayEvidenceIncomplete, item, "resource", "non-empty", "")
		}
		if item.Capability != item.Resource {
			return evidenceValidationError(GatewayEvidenceCapabilityMismatch, item, "capability", item.Resource, item.Capability)
		}
		if item.TransactionID != transactionID {
			return evidenceValidationError(GatewayEvidenceTransactionMismatch, item, "transaction_id", transactionID, item.TransactionID)
		}
		if item.ApplyReceipt.TransactionID != transactionID {
			return evidenceValidationError(GatewayEvidenceReceiptMismatch, item, "apply_receipt.transaction_id", transactionID, item.ApplyReceipt.TransactionID)
		}
		if item.ApplyReceipt.Capability != item.Resource {
			return evidenceValidationError(GatewayEvidenceReceiptMismatch, item, "apply_receipt.capability", item.Resource, item.ApplyReceipt.Capability)
		}
		if item.ApplyReceipt.Status != ReceiptApplied {
			return evidenceValidationError(GatewayEvidenceReceiptInvalid, item, "apply_receipt.status", ReceiptApplied, item.ApplyReceipt.Status)
		}
		if item.ApplyReceipt.AppliedAt.IsZero() || item.ApplyReceipt.AppliedAt.After(now) {
			return evidenceValidationError(GatewayEvidenceReceiptInvalid, item, "apply_receipt.applied_at", "non-zero and not future", item.ApplyReceipt.AppliedAt.UTC().Format(time.RFC3339Nano))
		}
		if item.Readback.TransactionID != transactionID {
			return evidenceValidationError(GatewayEvidenceReadbackMismatch, item, "readback.transaction_id", transactionID, item.Readback.TransactionID)
		}
		if item.Readback.Capability != item.Resource {
			return evidenceValidationError(GatewayEvidenceReadbackMismatch, item, "readback.capability", item.Resource, item.Readback.Capability)
		}
		if item.Readback.Timestamp.IsZero() {
			return evidenceValidationError(GatewayEvidenceReadbackStale, item, "readback.timestamp", "non-zero and fresh", "zero")
		}
		age := now.Sub(item.Readback.Timestamp)
		if !item.Readback.Fresh || age < 0 || age > ReadbackFreshnessWindow {
			return evidenceValidationError(GatewayEvidenceReadbackStale, item, "readback", "fresh and within freshness window", item.Readback.Reason)
		}
		if err := validateGatewaySnapshot(item, item.Before, "before", now); err != nil {
			return err
		}
		if err := validateGatewaySnapshot(item, item.After, "after", now); err != nil {
			return err
		}
		for _, supplemental := range item.SupplementalReadback {
			if supplemental.Name == "" || supplemental.Resource == "" || supplemental.PayloadHash == "" || len(supplemental.Shows) == 0 {
				return evidenceValidationError(GatewayEvidenceIncomplete, item, "supplemental_readback", "typed operation identity and show output", "incomplete")
			}
		}
	}
	return nil
}

func ValidateGatewayEvidence(evidence []GatewayResourceEvidence, transactionID string, now time.Time) error {
	return validateGatewayEvidence(evidence, transactionID, now)
}

func validateGatewaySnapshot(item GatewayResourceEvidence, snapshot vpp.Snapshot, field string, now time.Time) error {
	if snapshot.TransactionID != item.TransactionID {
		return evidenceValidationError(GatewayEvidenceSnapshotMismatch, item, field+".transaction_id", item.TransactionID, snapshot.TransactionID)
	}
	if snapshot.RequestID != item.TransactionID {
		return evidenceValidationError(GatewayEvidenceSnapshotMismatch, item, field+".request_id", item.TransactionID, snapshot.RequestID)
	}
	if snapshot.ReadbackAt.IsZero() || snapshot.ReadbackAt.After(now) {
		return evidenceValidationError(GatewayEvidenceIncomplete, item, field+".readback_at", "non-zero and not future", snapshot.ReadbackAt.UTC().Format(time.RFC3339Nano))
	}
	expectedHash := item.BeforeHash
	if field == "after" {
		expectedHash = item.AfterHash
	}
	if snapshot.Hash == "" || expectedHash == "" || snapshot.Hash != expectedHash {
		return evidenceValidationError(GatewayEvidenceSnapshotHash, item, field+".hash", expectedHash, snapshot.Hash)
	}
	if err := vpp.VerifySnapshotHash(snapshot); err != nil {
		return evidenceValidationError(GatewayEvidenceSnapshotHash, item, field+".hash", "canonical hash", snapshot.Hash)
	}
	return nil
}
