package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

var ErrGatewayTransactionFailed = errors.New("gateway multi-resource transaction failed")

var gatewayResourceOrder = []string{"interfaces", "bonds", "wan-groups", "routes", "acls", "qos", "nat44", "port-maps"}

type GatewayResourceAdapter interface {
	Name() string
	Apply(context.Context, Plan) (GatewayResourceResult, error)
	Rollback(context.Context, Plan) error
}

type GatewayResourceApplicability interface {
	Applicable(Plan) bool
}

type GatewayResourceDeleter interface {
	Delete(context.Context, Plan) (bool, error)
}

type GatewayResourceRollbackPhases interface {
	RollbackCleanup(context.Context, Plan) error
	RollbackRestore(context.Context, Plan) error
}

type GatewayResourceResult struct {
	Receipt              ApplyReceipt
	Readback             Readback
	SupplementalReadback []vpp.SupplementalOperationReadback
	Deleted              bool
	Before               vpp.Snapshot
	After                vpp.Snapshot
}

type GatewayTransactionResult struct {
	Order      []string
	Receipts   []ApplyReceipt
	Readbacks  []Readback
	Deletions  map[string]bool
	Evidence   []GatewayResourceEvidence
	Rollback   RollbackReceipt
	FailedName string
}

type GatewayMultiResourceTransaction struct {
	Adapters []GatewayResourceAdapter
	Now      Clock
	Prepare  func(context.Context, Plan) error
	mu       sync.Mutex
	active   map[string][]GatewayResourceAdapter
}

type GatewayTransactionRunner interface {
	Run(context.Context, Plan) (GatewayTransactionResult, error)
	Rollback(context.Context, Plan) error
}

func (transaction *GatewayMultiResourceTransaction) Run(ctx context.Context, plan Plan) (GatewayTransactionResult, error) {
	if transaction.Prepare != nil {
		if err := transaction.Prepare(ctx, plan); err != nil {
			return GatewayTransactionResult{}, fmt.Errorf("gateway reconciliation prepare: %w", err)
		}
	}
	adapters, err := transaction.orderedAdapters(plan)
	if err != nil {
		return GatewayTransactionResult{}, err
	}
	now := transaction.now
	result := GatewayTransactionResult{Deletions: map[string]bool{}}
	completed := make([]GatewayResourceAdapter, 0, len(adapters))
	applied := make([]GatewayResourceAdapter, 0, len(adapters))
	for index := len(adapters) - 1; index >= 0; index-- {
		adapter := adapters[index]
		deleter, ok := adapter.(GatewayResourceDeleter)
		if !ok {
			continue
		}
		resourcePlan := plan
		resourcePlan.Request.Resource = adapter.Name()
		deleted, deleteErr := deleter.Delete(ctx, resourcePlan)
		if deleteErr != nil {
			result.FailedName = adapter.Name()
			return transaction.fail(ctx, plan, result, completed, []GatewayResourceAdapter{adapter}, adapters, deleteErr, now())
		}
		if deleted {
			completed = append(completed, adapter)
			result.Deletions[adapter.Name()] = true
		}
	}
	for _, adapter := range adapters {
		resourcePlan := plan
		resourcePlan.Request.Resource = adapter.Name()
		resource, applyErr := adapter.Apply(ctx, resourcePlan)
		if applyErr != nil {
			result.FailedName = adapter.Name()
			return transaction.fail(ctx, plan, result, completed, append(applied, adapter), adapters, applyErr, now())
		}
		applied = append(applied, adapter)
		if err := resource.Receipt.validate(resourcePlan.Request, now()); err != nil {
			result.FailedName = adapter.Name()
			return transaction.fail(ctx, plan, result, completed, applied, adapters, err, now())
		}
		if err := resource.Readback.validate(resourcePlan.Request, now()); err != nil {
			result.FailedName = adapter.Name()
			return transaction.fail(ctx, plan, result, completed, applied, adapters, err, now())
		}
		if !containsGatewayAdapter(completed, adapter) {
			completed = append(completed, adapter)
		}
		result.Order = append(result.Order, adapter.Name())
		result.Receipts = append(result.Receipts, resource.Receipt)
		result.Readbacks = append(result.Readbacks, resource.Readback)
		result.Deletions[adapter.Name()] = resource.Deleted
		evidence, evidenceErr := gatewayEvidence(resource, adapter.Name(), plan.Request.TransactionID, now())
		if evidenceErr != nil {
			result.FailedName = adapter.Name()
			return transaction.fail(ctx, plan, result, completed, applied, adapters, evidenceErr, now())
		}
		result.Evidence = append(result.Evidence, evidence)
	}
	result.Rollback = RollbackReceipt{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Status: ReceiptApplied, Cause: "multi-resource transaction committed", Timestamp: now()}
	transaction.mu.Lock()
	if transaction.active == nil {
		transaction.active = map[string][]GatewayResourceAdapter{}
	}
	transaction.active[plan.Request.TransactionID] = adapters
	transaction.mu.Unlock()
	return result, nil
}

func containsGatewayAdapter(adapters []GatewayResourceAdapter, wanted GatewayResourceAdapter) bool {
	for _, adapter := range adapters {
		if adapter.Name() == wanted.Name() {
			return true
		}
	}
	return false
}

func (transaction *GatewayMultiResourceTransaction) fail(ctx context.Context, plan Plan, result GatewayTransactionResult, completed, applied, ordered []GatewayResourceAdapter, cause error, now time.Time) (GatewayTransactionResult, error) {
	rollback := RollbackReceipt{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Status: ReceiptRolledBack, Cause: cause.Error(), Timestamp: now}
	rollbackErrors := make([]error, 0)
	phased := true
	for _, adapter := range ordered {
		if _, ok := adapter.(GatewayResourceRollbackPhases); !ok {
			phased = false
			break
		}
	}
	if phased {
		for index := len(applied) - 1; index >= 0; index-- {
			adapter := applied[index]
			if err := adapter.(GatewayResourceRollbackPhases).RollbackCleanup(ctx, plan); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback cleanup %s: %w", adapter.Name(), err))
			}
		}
		for _, adapter := range ordered {
			if err := adapter.(GatewayResourceRollbackPhases).RollbackRestore(ctx, plan); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback restore %s: %w", adapter.Name(), err))
			}
		}
	} else {
		for index := len(completed) - 1; index >= 0; index-- {
			if err := completed[index].Rollback(ctx, plan); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback %s: %w", completed[index].Name(), err))
			}
		}
	}
	if len(rollbackErrors) > 0 {
		rollback.Status = ReceiptFailed
		rollback.RollbackError = errors.Join(rollbackErrors...).Error()
	}
	result.Rollback = rollback
	result.Evidence = nil
	return result, &GatewayTransactionError{Resource: result.FailedName, Cause: cause, Rollback: rollback, RollbackErr: errors.Join(rollbackErrors...)}
}

type GatewayTransactionError struct {
	Resource    string
	Cause       error
	Rollback    RollbackReceipt
	RollbackErr error
}

func (err *GatewayTransactionError) Error() string {
	if err.RollbackErr != nil {
		return fmt.Sprintf("%s: resource %s: %v; rollback: %v", ErrGatewayTransactionFailed, err.Resource, err.Cause, err.RollbackErr)
	}
	return fmt.Sprintf("%s: resource %s: %v", ErrGatewayTransactionFailed, err.Resource, err.Cause)
}

func (err *GatewayTransactionError) Unwrap() error {
	return errors.Join(err.Cause, err.RollbackErr)
}

func (err *GatewayTransactionError) Is(target error) bool {
	return target == ErrGatewayTransactionFailed
}

func (transaction *GatewayMultiResourceTransaction) Rollback(ctx context.Context, plan Plan) error {
	transaction.mu.Lock()
	completed := transaction.active[plan.Request.TransactionID]
	delete(transaction.active, plan.Request.TransactionID)
	transaction.mu.Unlock()
	rollbackErrors := make([]error, 0)
	phased := true
	for _, adapter := range completed {
		if _, ok := adapter.(GatewayResourceRollbackPhases); !ok {
			phased = false
			break
		}
	}
	if phased {
		for index := len(completed) - 1; index >= 0; index-- {
			adapter := completed[index]
			if err := adapter.(GatewayResourceRollbackPhases).RollbackCleanup(ctx, plan); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback cleanup %s: %w", adapter.Name(), err))
			}
		}
		for _, adapter := range completed {
			if err := adapter.(GatewayResourceRollbackPhases).RollbackRestore(ctx, plan); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback restore %s: %w", adapter.Name(), err))
			}
		}
	} else {
		for index := len(completed) - 1; index >= 0; index-- {
			if err := completed[index].Rollback(ctx, plan); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback %s: %w", completed[index].Name(), err))
			}
		}
	}
	return errors.Join(rollbackErrors...)
}

func (transaction *GatewayMultiResourceTransaction) orderedAdapters(plan Plan) ([]GatewayResourceAdapter, error) {
	byName := make(map[string]GatewayResourceAdapter, len(transaction.Adapters))
	for _, adapter := range transaction.Adapters {
		if adapter == nil || strings.TrimSpace(adapter.Name()) == "" {
			return nil, fmt.Errorf("%w: resource adapter name is required", ErrGatewayTransactionFailed)
		}
		name := strings.TrimSpace(adapter.Name())
		if applicability, ok := adapter.(GatewayResourceApplicability); ok && !applicability.Applicable(plan) {
			continue
		}
		if _, exists := byName[name]; exists {
			return nil, fmt.Errorf("%w: duplicate resource adapter %q", ErrGatewayTransactionFailed, name)
		}
		byName[name] = adapter
	}
	ordered := make([]GatewayResourceAdapter, 0, len(transaction.Adapters))
	for _, name := range gatewayResourceOrder {
		if adapter, ok := byName[name]; ok {
			ordered = append(ordered, adapter)
			delete(byName, name)
		}
	}
	for name := range byName {
		return nil, fmt.Errorf("%w: unsupported resource adapter %q", ErrGatewayTransactionFailed, name)
	}
	return ordered, nil
}

func (transaction *GatewayMultiResourceTransaction) now() time.Time {
	if transaction.Now == nil {
		return time.Now().UTC()
	}
	return transaction.Now().UTC()
}
