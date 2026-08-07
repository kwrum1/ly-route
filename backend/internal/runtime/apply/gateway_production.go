package apply

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

func NewProductionGatewayTransaction(adapter vpp.Adapter, now Clock) *GatewayMultiResourceTransaction {
	reconciler := &productionGatewayReconciler{adapter: adapter}
	wanGroups := vpp.NewWANGroupsContext(nil, nil)
	transaction := &GatewayMultiResourceTransaction{Now: now, Prepare: reconciler.prepare}
	for _, name := range gatewayResourceOrder {
		transaction.Adapters = append(transaction.Adapters, &productionGatewayAdapter{name: name, reconciler: reconciler, now: now, deleted: map[string]GatewayResourceResult{}, wanGroups: &wanGroups})
	}
	return transaction
}

type productionGatewayReconciler struct {
	adapter vpp.Adapter
	mu      sync.Mutex
	diffs   map[string]vpp.GatewayDiff
	live    map[string]vpp.Snapshot
}

func (reconciler *productionGatewayReconciler) prepare(ctx context.Context, plan Plan) error {
	if plan.Previous.Available && plan.Previous.GatewayPlan == nil {
		return &LegacyGatewayPlanError{SnapshotID: plan.Request.PreviousSnapshotID}
	}
	prior := vpp.Plan{}
	if plan.Previous.GatewayPlan != nil {
		prior = *plan.Previous.GatewayPlan
	}
	request := vpp.GatewayDiffSnapshotRequest(plan.Request.TransactionID, prior, plan.GatewayPlan)
	live := vpp.Snapshot{RequestID: plan.Request.TransactionID, TransactionID: plan.Request.TransactionID}
	// A stale TAP can make even a no-op snapshot ambiguous. Clean managed
	// service handoffs before deciding whether the snapshot needs capabilities;
	// this is deliberately independent of the diff list.
	if err := reconciler.adapter.CleanupManagedTAPs(ctx, plan.GatewayPlan); err != nil {
		return fmt.Errorf("managed TAP cleanup: %w", err)
	}
	if len(request.Capabilities) > 0 {
		readback, err := reconciler.adapter.Snapshot(ctx, request)
		if err != nil {
			return fmt.Errorf("desired/live snapshot: %w", err)
		}
		live = readback
	}
	diff, err := vpp.ReconcileGatewayPlan(vpp.GatewayReconciliationInput{TransactionID: plan.Request.TransactionID, Prior: prior, Desired: plan.GatewayPlan, Live: live, RepairVerifiedDrift: true})
	if err != nil {
		return fmt.Errorf("desired/live diff: %w", err)
	}
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	if reconciler.diffs == nil {
		reconciler.diffs = map[string]vpp.GatewayDiff{}
		reconciler.live = map[string]vpp.Snapshot{}
	}
	reconciler.diffs[plan.Request.TransactionID] = diff
	reconciler.live[plan.Request.TransactionID] = live
	return nil
}

func (reconciler *productionGatewayReconciler) state(transactionID string) (vpp.GatewayDiff, vpp.Snapshot, bool) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	diff, found := reconciler.diffs[transactionID]
	return diff, reconciler.live[transactionID], found
}

type productionGatewayAdapter struct {
	name       string
	reconciler *productionGatewayReconciler
	now        Clock
	deleted    map[string]GatewayResourceResult
	wanGroups  *vpp.WANGroupsContext
}

func (adapter *productionGatewayAdapter) Name() string { return adapter.name }

func (adapter *productionGatewayAdapter) Applicable(plan Plan) bool {
	diff, live, found := adapter.reconciler.state(plan.Request.TransactionID)
	return found && (gatewayDiffApplies(adapter.name, diff) || gatewaySnapshotHasResource(adapter.name, live) || adapter.hasSupplemental(plan.GatewayPlan))
}

func (adapter *productionGatewayAdapter) Apply(ctx context.Context, plan Plan) (GatewayResourceResult, error) {
	diff, prior, found := adapter.reconciler.state(plan.Request.TransactionID)
	if !found {
		return GatewayResourceResult{}, fmt.Errorf("%s apply: reconciliation is unavailable", adapter.name)
	}
	supplemental, err := adapter.applySupplemental(ctx, plan.GatewayPlan)
	if err != nil {
		return GatewayResourceResult{}, fmt.Errorf("%s supplemental apply: %w", adapter.name, err)
	}
	if !gatewayDiffHasApply(adapter.name, diff) {
		result, deleted := adapter.deleted[plan.Request.TransactionID]
		if deleted {
			result.SupplementalReadback = supplemental
			return result, nil
		}
		result = adapter.result(plan.Request.TransactionID, false, prior, prior)
		result.SupplementalReadback = supplemental
		return result, nil
	}
	_, err = adapter.applyDiff(ctx, diff, prior, plan.GatewayPlan)
	if err != nil {
		return GatewayResourceResult{}, fmt.Errorf("%s apply: %w", adapter.name, err)
	}
	request, err := vpp.GatewayResourceSnapshotRequest(plan.Request.TransactionID, adapter.name, plan.GatewayPlan)
	if err != nil {
		return GatewayResourceResult{}, fmt.Errorf("%s desired readback request: %w", adapter.name, err)
	}
	after, err := adapter.reconciler.adapter.Snapshot(ctx, request)
	if err != nil {
		return GatewayResourceResult{}, fmt.Errorf("%s desired readback: %w", adapter.name, err)
	}
	_, deleted := adapter.deleted[plan.Request.TransactionID]
	result := adapter.result(plan.Request.TransactionID, deleted, prior, after)
	result.SupplementalReadback = supplemental
	return result, nil
}

func (adapter *productionGatewayAdapter) supplementalOwner() vpp.SupplementalOwner {
	switch adapter.name {
	case "interfaces":
		return vpp.SupplementalInterfaces
	case "routes":
		return vpp.SupplementalRoutes
	case "acls":
		return vpp.SupplementalSecurity
	case "qos":
		return vpp.SupplementalQoS
	default:
		return ""
	}
}

func (adapter *productionGatewayAdapter) hasSupplemental(plan vpp.Plan) bool {
	owner := adapter.supplementalOwner()
	return owner != "" && vpp.HasSupplementalOperations(plan, owner)
}

func (adapter *productionGatewayAdapter) supplementalChanged(plan Plan) (bool, error) {
	owner := adapter.supplementalOwner()
	if owner == "" {
		return false, nil
	}
	if plan.Previous.GatewayPlan == nil {
		return vpp.HasSupplementalOperations(plan.GatewayPlan, owner), nil
	}
	equal, err := vpp.SupplementalOperationsEqual(plan.GatewayPlan, *plan.Previous.GatewayPlan, owner)
	return !equal, err
}

func (adapter *productionGatewayAdapter) applySupplemental(ctx context.Context, plan vpp.Plan) ([]vpp.SupplementalOperationReadback, error) {
	owner := adapter.supplementalOwner()
	if owner == "" || !vpp.HasSupplementalOperations(plan, owner) {
		return nil, nil
	}
	return adapter.reconciler.adapter.ApplySupplemental(ctx, plan, owner)
}

func gatewaySnapshotHasResource(name string, snapshot vpp.Snapshot) bool {
	switch name {
	case "interfaces":
		return len(snapshot.Interfaces) > 0
	case "bonds":
		return len(snapshot.Bonds) > 0
	case "wan-groups":
		return len(snapshot.WANGroups) > 0
	case "routes":
		return len(snapshot.RoutePolicies) > 0
	case "acls":
		return len(snapshot.ACLs) > 0
	case "qos":
		return len(snapshot.QoS) > 0
	case "nat44":
		return len(snapshot.NAT.StaticMappings) > 0
	case "port-maps":
		return len(snapshot.NAT.PortMappings) > 0
	default:
		return false
	}
}

func (adapter *productionGatewayAdapter) Delete(ctx context.Context, plan Plan) (bool, error) {
	diff, prior, found := adapter.reconciler.state(plan.Request.TransactionID)
	if !found || !gatewayDiffHasDelete(adapter.name, diff) {
		return false, nil
	}
	after, err := adapter.deleteDiff(ctx, diff, prior)
	if err != nil {
		return false, fmt.Errorf("%s delete: %w", adapter.name, err)
	}
	adapter.deleted[plan.Request.TransactionID] = adapter.result(plan.Request.TransactionID, true, prior, after)
	return true, nil
}

func (adapter *productionGatewayAdapter) Rollback(ctx context.Context, plan Plan) error {
	diff, prior, found := adapter.reconciler.state(plan.Request.TransactionID)
	if !found {
		return fmt.Errorf("%s rollback: prior snapshot is unavailable", adapter.name)
	}
	return adapter.rollback(ctx, productionRollbackInput{transactionID: plan.Request.TransactionID, desired: plan.GatewayPlan, prior: prior, attempted: diff})
}

func (adapter *productionGatewayAdapter) result(transactionID string, deleted bool, before, after vpp.Snapshot) GatewayResourceResult {
	timestamp := adapter.timestamp()
	return GatewayResourceResult{
		Receipt:  ApplyReceipt{TransactionID: transactionID, Capability: adapter.name, Status: ReceiptApplied, AppliedAt: timestamp},
		Readback: Readback{TransactionID: transactionID, Capability: adapter.name, Timestamp: timestamp, Fresh: true},
		Deleted:  deleted,
		Before:   before,
		After:    after,
	}
}

func (adapter *productionGatewayAdapter) timestamp() time.Time {
	if adapter.now == nil {
		return time.Now().UTC()
	}
	return adapter.now().UTC()
}
