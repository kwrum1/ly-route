package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"ly-route/backend/internal/runtime/apply"
)

type EvidenceRequest struct {
	TransactionID string
	Capability    string
	Artifacts     []RenderedArtifact
}

type RuntimeEvidenceProvider interface {
	Receipt(context.Context, EvidenceRequest) (apply.ApplyReceipt, error)
	Readback(context.Context, EvidenceRequest) (apply.Readback, error)
}

type serviceApplyRecord struct {
	Service       ServiceName       `json:"service"`
	TransactionID string            `json:"transaction_id,omitempty"`
	AppliedAt     time.Time         `json:"applied_at"`
	Artifacts     map[string]string `json:"artifacts"`
}

func (controller FilesystemController) Receipt(_ context.Context, request EvidenceRequest) (apply.ApplyReceipt, error) {
	if strings.TrimSpace(request.TransactionID) == "" || strings.TrimSpace(request.Capability) == "" || len(request.Artifacts) == 0 {
		return apply.ApplyReceipt{}, fmt.Errorf("service receipt identity or artifacts are missing: %w", apply.ErrIncompleteEvidence)
	}
	appliedAt, err := controller.validateRecords(request.Artifacts)
	if err != nil {
		return apply.ApplyReceipt{}, err
	}
	if err := controller.bindTransaction(request.TransactionID, request.Artifacts); err != nil {
		return apply.ApplyReceipt{}, err
	}
	return apply.ApplyReceipt{TransactionID: request.TransactionID, Capability: request.Capability, Status: apply.ReceiptApplied, AppliedAt: appliedAt}, nil
}

func (controller FilesystemController) Readback(ctx context.Context, request EvidenceRequest) (apply.Readback, error) {
	if _, err := controller.validateRecords(request.Artifacts); err != nil {
		return apply.Readback{}, err
	}
	for service, artifacts := range groupByService(request.Artifacts) {
		record, err := controller.loadApplyRecord(service)
		if err != nil || record.TransactionID != request.TransactionID {
			return apply.Readback{}, fmt.Errorf("%s receipt transaction mismatch: %w", service, apply.ErrIncompleteEvidence)
		}
		for _, artifact := range artifacts {
			path, err := controller.resolvePath(artifact.Path)
			if err != nil {
				return apply.Readback{}, err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return apply.Readback{}, fmt.Errorf("read back %s artifact %s: %w", service, artifact.Path, err)
			}
			digest := sha256.Sum256(content)
			if hex.EncodeToString(digest[:]) != artifact.ContentHash {
				return apply.Readback{}, fmt.Errorf("%s artifact %s hash mismatch: %w", service, artifact.Path, apply.ErrIncompleteEvidence)
			}
		}
		if artifactsArePersistOnly(artifacts) {
			continue
		}
		if controller.Runner == nil {
			return apply.Readback{}, fmt.Errorf("%s readback runner is missing: %w", service, apply.ErrIncompleteEvidence)
		}
		if err := liveReadback(ctx, controller.Runner, service, artifacts); err != nil {
			return apply.Readback{}, fmt.Errorf("%s live readback: %w", service, err)
		}
	}
	return apply.Readback{TransactionID: request.TransactionID, Capability: request.Capability, Timestamp: controller.now(), Fresh: true}, nil
}

func (controller FilesystemController) validateRecords(artifacts []RenderedArtifact) (time.Time, error) {
	if len(artifacts) == 0 {
		return time.Time{}, fmt.Errorf("service evidence has no artifacts: %w", apply.ErrIncompleteEvidence)
	}
	var appliedAt time.Time
	for service, expected := range groupByService(artifacts) {
		record, err := controller.loadApplyRecord(service)
		if err != nil {
			return time.Time{}, fmt.Errorf("load %s apply record: %w", service, errors.Join(apply.ErrIncompleteEvidence, err))
		}
		if record.Service != service || len(record.Artifacts) != len(expected) || record.AppliedAt.IsZero() {
			return time.Time{}, fmt.Errorf("%s apply record identity is incomplete: %w", service, apply.ErrIncompleteEvidence)
		}
		for _, artifact := range expected {
			if record.Artifacts[artifact.Path] != artifact.ContentHash {
				return time.Time{}, fmt.Errorf("%s receipt hash mismatch for %s: %w", service, artifact.Path, apply.ErrIncompleteEvidence)
			}
		}
		if record.AppliedAt.After(appliedAt) {
			appliedAt = record.AppliedAt
		}
	}
	return appliedAt, nil
}

func (controller FilesystemController) saveApplyRecord(service ServiceName, artifacts []RenderedArtifact, transactionID string) error {
	hashes := make(map[string]string, len(artifacts))
	for _, artifact := range artifacts {
		hashes[artifact.Path] = artifact.ContentHash
	}
	payload, err := json.Marshal(serviceApplyRecord{
		Service:       service,
		TransactionID: strings.TrimSpace(transactionID),
		AppliedAt:     controller.now(),
		Artifacts:     hashes,
	})
	if err != nil {
		return err
	}
	return writeFileAtomically(controller.applyRecordPath(service), payload, 0o600)
}

func (controller FilesystemController) bindTransaction(transactionID string, artifacts []RenderedArtifact) error {
	for service := range groupByService(artifacts) {
		record, err := controller.loadApplyRecord(service)
		if err != nil {
			return err
		}
		if record.TransactionID != "" && record.TransactionID != transactionID {
			return fmt.Errorf("%s apply record belongs to transaction %q: %w", service, record.TransactionID, apply.ErrIncompleteEvidence)
		}
		if record.TransactionID == transactionID {
			continue
		}
		record.TransactionID = transactionID
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := writeFileAtomically(controller.applyRecordPath(service), payload, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (controller FilesystemController) loadApplyRecord(service ServiceName) (serviceApplyRecord, error) {
	payload, err := os.ReadFile(controller.applyRecordPath(service))
	if err != nil {
		return serviceApplyRecord{}, err
	}
	var record serviceApplyRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return serviceApplyRecord{}, err
	}
	return record, nil
}

func (controller FilesystemController) applyRecordPath(service ServiceName) string {
	path, _ := controller.resolvePath("/var/lib/ly-route/service-runtime/receipt-" + string(service) + ".json")
	return path
}

func (controller FilesystemController) now() time.Time {
	if controller.Now == nil {
		return time.Now().UTC()
	}
	return controller.Now().UTC()
}
