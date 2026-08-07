package apply

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"ly-route/backend/internal/runtime/dataplane"
	"ly-route/backend/internal/runtime/vpp"
)

type DataplaneController interface {
	Apply(context.Context, dataplane.Request) (dataplane.Receipt, error)
	Rollback(context.Context, string) (dataplane.Receipt, error)
}

type productionGatewayWithDataplane struct {
	inner      GatewayTransactionRunner
	dataplane  DataplaneController
	mu         sync.Mutex
	dpdkActive map[string]bool
}

type ProductionGatewayComposition interface {
	GatewayResourceNames() []string
	HasDataplaneController() bool
}

func (transaction *productionGatewayWithDataplane) GatewayResourceNames() []string {
	return append([]string(nil), gatewayResourceOrder...)
}

func (transaction *productionGatewayWithDataplane) HasDataplaneController() bool {
	return transaction.dataplane != nil
}

func NewProductionGatewayTransactionWithDataplane(adapter vpp.Adapter, controller DataplaneController, now Clock) GatewayTransactionRunner {
	return &productionGatewayWithDataplane{inner: NewProductionGatewayTransaction(adapter, now), dataplane: controller, dpdkActive: map[string]bool{}}
}

func (transaction *productionGatewayWithDataplane) Run(ctx context.Context, plan Plan) (GatewayTransactionResult, error) {
	if len(plan.GatewayPlan.NativePath.Assignments) == 0 {
		return transaction.inner.Run(ctx, plan)
	}
	path, err := vpp.SelectNativePath(plan.GatewayPlan.NativePath)
	if err != nil {
		return GatewayTransactionResult{}, err
	}
	if transaction.dataplane == nil {
		return GatewayTransactionResult{}, fmt.Errorf("dataplane controller is unavailable")
	}
	request := dataplane.Request{TransactionID: plan.Request.TransactionID, Path: path}
	dataplaneReceipt, err := transaction.dataplane.Apply(ctx, request)
	if err != nil {
		return GatewayTransactionResult{}, err
	}
	plan.GatewayPlan.DataplanePrepared = true
	result, applyErr := transaction.inner.Run(ctx, plan)
	if applyErr != nil {
		if dataplaneReceipt.Changed {
			_, rollbackErr := transaction.dataplane.Rollback(ctx, request.TransactionID)
			applyErr = errors.Join(applyErr, rollbackErr)
		}
		return result, applyErr
	}
	if dataplaneReceipt.Changed {
		transaction.mu.Lock()
		transaction.dpdkActive[request.TransactionID] = true
		transaction.mu.Unlock()
	}
	return result, nil
}

func (transaction *productionGatewayWithDataplane) Rollback(ctx context.Context, plan Plan) error {
	innerErr := transaction.inner.Rollback(ctx, plan)
	transaction.mu.Lock()
	active := transaction.dpdkActive[plan.Request.TransactionID]
	delete(transaction.dpdkActive, plan.Request.TransactionID)
	transaction.mu.Unlock()
	if !active {
		return innerErr
	}
	_, dataplaneErr := transaction.dataplane.Rollback(ctx, plan.Request.TransactionID)
	return errors.Join(innerErr, dataplaneErr)
}
