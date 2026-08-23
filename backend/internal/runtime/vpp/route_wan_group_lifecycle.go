package vpp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

type RoutePolicyReadback struct {
	Policies []trafficpolicy.RoutePolicy `json:"policies"`
}
type WANGroupReadback struct {
	Groups []trafficpolicy.WANGroup `json:"groups"`
}

type RouteWANGroupPlan struct {
	TransactionID       string
	IngressVPPInterface string
	// LocalDestinations are LAN prefixes that must bypass a catch-all ABF
	// policy. They are carried by incremental reconciliation as well as the
	// initial full-plan compiler so a UI edit cannot route traffic addressed to
	// the gateway or another LAN client through NAT/WAN.
	LocalDestinations []string
	Routes            []trafficpolicy.RoutePolicy
	// RoutePolicyContext is the complete desired route-policy set used to
	// compile an incremental change. The production reconciler may only put
	// changed policies in Routes, but a native FIB chain needs unchanged
	// neighboring policies to calculate each table's fall-through target.
	RoutePolicyContext []trafficpolicy.RoutePolicy
	WANGroups          []trafficpolicy.WANGroup
	WANGroupsContext   WANGroupsContext
	DeleteRoutes       []string
	DeleteWANGroups    []string
	DeleteRouteState   []trafficpolicy.RoutePolicy
	DeleteWANState     []trafficpolicy.WANGroup
}

type RouteWANGroupLifecycleError struct {
	Operation      string
	Cause          error
	Rollback       error
	RollbackResult RollbackResult
}

func (err *RouteWANGroupLifecycleError) Error() string {
	if err.Rollback != nil {
		return fmt.Sprintf("route/WAN-group operation %s failed: %v; rollback %s: %v", err.Operation, err.Cause, err.RollbackResult, err.Rollback)
	}
	return fmt.Sprintf("route/WAN-group operation %s failed: %v; rollback %s", err.Operation, err.Cause, err.RollbackResult)
}

func (err *RouteWANGroupLifecycleError) Unwrap() error { return errors.Join(err.Cause, err.Rollback) }

type RouteWANGroupApplyResult struct {
	Receipt  Receipt
	Readback Snapshot
}

func BuildRouteWANGroupOperations(plan RouteWANGroupPlan) ([]Operation, error) {
	transactionID := strings.TrimSpace(plan.TransactionID)
	if transactionID == "" {
		return nil, fmt.Errorf("%w: transaction ID is required", ErrSnapshotIncomplete)
	}
	groups := make(map[string]trafficpolicy.WANGroup, len(plan.WANGroups)+len(plan.WANGroupsContext.RouteGroups()))
	for _, group := range plan.WANGroupsContext.RouteGroups() {
		groups[group.ID] = group
	}
	for _, group := range plan.WANGroups {
		groups[group.ID] = group
	}
	operations := make([]Operation, 0, len(plan.Routes)+len(plan.WANGroups)+len(plan.DeleteRoutes)+len(plan.DeleteWANGroups))
	for _, group := range plan.WANGroups {
		operations = append(operations, Operation{Name: "vpp.pbr.next-hop-group", RequestID: transactionID, Resource: group.ID, Payload: group, VPPCtlCommands: wanGroupCommands(group)})
	}
	chainContext := plan.RoutePolicyContext
	if len(chainContext) == 0 {
		chainContext = plan.Routes
	}
	routeOptions := buildRoutePolicyCommandOptions(chainContext, groups)
	addRoutePolicyLocalDestinationPrefixes(routeOptions, chainContext, plan.LocalDestinations)
	routes := orderedRoutePolicySubsetForVPP(plan.Routes, chainContext, routeOptions, groups)
	if err := validateLargeRoutePolicyFallback(routes, routeOptions, groups); err != nil {
		return nil, err
	}
	for _, route := range routes {
		option := routeOptions[route.ID]
		operations = append(operations, Operation{Name: "vpp.route-policy", RequestID: transactionID, Resource: route.ID, Payload: route, VPPCtlCommands: routePolicyCommandsWithOptions(route, groups, option)})
	}
	// A chained route table can point at a lower-priority table, and a route
	// table can in turn point at a WAN-group table.  Tear down the outermost
	// (highest-priority) policy first so every reference is gone before its
	// target table is deleted.  This is the inverse of the install order.
	for _, route := range routePoliciesForVPPDelete(plan.DeleteRoutes, plan.DeleteRouteState) {
		id := route.ID
		operation := Operation{Name: "vpp.route-policy", RequestID: transactionID, Resource: id, VPPCtlCommands: deleteRoutePolicyCommands(id)}
		operation.Payload = route
		operations = append(operations, operation)
	}
	for _, id := range plan.DeleteWANGroups {
		operations = append(operations, Operation{Name: "vpp.pbr.next-hop-group", RequestID: transactionID, Resource: id, VPPCtlCommands: deleteWANGroupCommands(id)})
	}
	ingress := strings.TrimSpace(plan.IngressVPPInterface)
	if ingress == "" {
		ingress = configuredLANVPPInterface()
	}
	return rewriteOperationsInterface(operations, ingress), nil
}

func (a Adapter) ApplyRouteWANGroup(ctx context.Context, plan RouteWANGroupPlan, prior Snapshot, attempted ...RouteWANGroupPlan) (RouteWANGroupApplyResult, error) {
	plan.DeleteRouteState = appendRoutesByID(plan.DeleteRouteState, plan.DeleteRoutes, prior.RoutePolicies)
	plan.DeleteWANState = appendWANGroupsByID(plan.DeleteWANState, plan.DeleteWANGroups, prior.WANGroups)
	rollbackPlan := plan
	if len(attempted) > 0 {
		rollbackPlan = attempted[0]
	}
	// An optimized route chain is a graph of private FIBs: the higher
	// precedence table points at the next lower precedence table.  The old
	// per-operation lifecycle removed and recreated each policy inline.  On a
	// full apply that tried to delete the terminal table while the other live
	// tables still referenced it, which VPP correctly rejected as a retained
	// private FIB.  Reconcile a complete chain in two phases: remove every old
	// policy in dependency-safe order, then install the desired chain in its
	// dependency order.
	fullRouteRebuild := routePolicyFullRebuildRequired(plan, prior)
	buildPlan := plan
	if fullRouteRebuild && len(plan.RoutePolicyContext) > 0 && len(plan.Routes) > 0 {
		buildPlan.Routes = append([]trafficpolicy.RoutePolicy(nil), plan.RoutePolicyContext...)
	}
	operations, err := BuildRouteWANGroupOperations(buildPlan)
	if err != nil {
		return RouteWANGroupApplyResult{}, err
	}
	if fullRouteRebuild {
		operations = stageRoutePolicyInstallOperations(operations)
	}
	if a.Client == nil {
		return RouteWANGroupApplyResult{}, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return RouteWANGroupApplyResult{}, fmt.Errorf("%w: open apply channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	receiptOperations := make([]Operation, 0, len(operations))
	if fullRouteRebuild {
		// A full rebuild must be idempotent even when the persisted snapshot is
		// incomplete or was produced by an older route representation. Include
		// every policy that may still exist in VPP: prior state, explicit deletes,
		// and the complete desired chain. The pre-delete is harmless for an absent
		// policy and prevents an old ABF with the desired stable ID from blocking
		// the new dynamically allocated ACL during replay.
		cleanupIDs := make([]string, 0, len(prior.RoutePolicies)+len(plan.DeleteRoutes)+len(buildPlan.Routes))
		for _, route := range prior.RoutePolicies {
			cleanupIDs = append(cleanupIDs, route.ID)
		}
		cleanupIDs = append(cleanupIDs, plan.DeleteRoutes...)
		cleanupIDs = append(cleanupIDs, routePolicyIDs(buildPlan.Routes)...)
		states := append([]trafficpolicy.RoutePolicy(nil), prior.RoutePolicies...)
		states = appendRoutesByID(states, plan.DeleteRoutes, plan.DeleteRouteState)
		states = append(states, buildPlan.Routes...)
		ingress := strings.TrimSpace(plan.IngressVPPInterface)
		if ingress == "" {
			ingress = configuredLANVPPInterface()
		}
		for _, route := range routePoliciesForVPPDelete(cleanupIDs, states) {
			cleanup := Operation{
				Name:           "vpp.route-policy.pre-delete",
				RequestID:      plan.TransactionID,
				Resource:       route.ID,
				Payload:        route,
				VPPCtlCommands: deleteRoutePolicyCommands(route.ID),
			}
			cleanup = rewriteOperationsInterface([]Operation{cleanup}, ingress)[0]
			if _, err := doOperation(ctx, channel, cleanup); err != nil {
				return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, cleanup.Name, err, prior, rollbackPlan)
			}
			receiptOperations = append(receiptOperations, cleanup)
		}
	}
	for _, operation := range operations {
		if fullRouteRebuild && isStagedRoutePolicyApplyOperation(operation) {
			// The old state was removed in the phase above.  The replay name is
			// handled by the same dynamic-ACL path but skips a second cleanup.
			operation.Name = "vpp.route-policy.replay"
		}
		if _, err := doOperation(ctx, channel, operation); err != nil {
			return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, operation.Name, err, prior, rollbackPlan)
		}
		receiptOperations = append(receiptOperations, operation)
	}
	readback, err := a.snapshotRouteWANGroupConverged(ctx, routeWANGroupSnapshotRequestForPlan(plan))
	if err != nil {
		return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	if err := verifyRouteWANGroupDeletes(readback, plan); err != nil {
		return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	return RouteWANGroupApplyResult{Receipt: Receipt{RequestID: plan.TransactionID, Operations: receiptOperations}, Readback: readback}, nil
}

func (a Adapter) snapshotRouteWANGroupConverged(ctx context.Context, request SnapshotRequest) (Snapshot, error) {
	return retryRouteWANGroupSnapshot(ctx, 6, 2*time.Second, func() (Snapshot, error) {
		return a.Snapshot(ctx, request)
	})
}

func retryRouteWANGroupSnapshot(ctx context.Context, attempts int, delay time.Duration, snapshot func() (Snapshot, error)) (Snapshot, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		readback, err := snapshot()
		if err == nil {
			return readback, nil
		}
		lastErr = err
		if !errors.Is(err, ErrSnapshotIncomplete) || attempt == attempts-1 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return Snapshot{}, ctx.Err()
		case <-timer.C:
		}
	}
	return Snapshot{}, lastErr
}
