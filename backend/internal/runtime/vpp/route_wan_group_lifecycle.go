package vpp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

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
	Routes              []trafficpolicy.RoutePolicy
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
	routes := orderedRoutePolicySubsetForVPP(plan.Routes, chainContext, routeOptions, groups)
	if err := validateLargeRoutePolicyFallback(routes, routeOptions); err != nil {
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
		cleanupIDs := make([]string, 0, len(prior.RoutePolicies)+len(plan.DeleteRoutes))
		for _, route := range prior.RoutePolicies {
			cleanupIDs = append(cleanupIDs, route.ID)
		}
		cleanupIDs = append(cleanupIDs, plan.DeleteRoutes...)
		states := append([]trafficpolicy.RoutePolicy(nil), prior.RoutePolicies...)
		states = appendRoutesByID(states, plan.DeleteRoutes, plan.DeleteRouteState)
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
		if fullRouteRebuild && isRoutePolicyApplyOperation(operation) {
			// The old state was removed in the phase above.  The replay name is
			// handled by the same dynamic-ACL path but skips a second cleanup.
			operation.Name = "vpp.route-policy.replay"
		}
		if _, err := doOperation(ctx, channel, operation); err != nil {
			return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, operation.Name, err, prior, rollbackPlan)
		}
		receiptOperations = append(receiptOperations, operation)
	}
	readback, err := a.Snapshot(ctx, routeWANGroupSnapshotRequestForPlan(plan))
	if err != nil {
		return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	if err := verifyRouteWANGroupDeletes(readback, plan); err != nil {
		return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	return RouteWANGroupApplyResult{Receipt: Receipt{RequestID: plan.TransactionID, Operations: receiptOperations}, Readback: readback}, nil
}

func isRoutePolicyApplyOperation(operation Operation) bool {
	if operation.Name != "vpp.route-policy" {
		return false
	}
	for _, command := range operation.VPPCtlCommands {
		if strings.Contains(strings.TrimSpace(strings.TrimPrefix(command, "?")), "abf policy add ") {
			return true
		}
	}
	return false
}

func routePolicyFullRebuildRequired(plan RouteWANGroupPlan, prior Snapshot) bool {
	if len(plan.Routes) == 0 && len(plan.DeleteRoutes) == 0 {
		return false
	}
	desired := plan.Routes
	if len(plan.RoutePolicyContext) > 0 {
		desired = plan.RoutePolicyContext
	}
	desiredGroups := make(map[string]trafficpolicy.WANGroup, len(plan.WANGroups)+len(plan.WANGroupsContext.RouteGroups()))
	for _, group := range plan.WANGroupsContext.RouteGroups() {
		desiredGroups[group.ID] = group
	}
	for _, group := range plan.WANGroups {
		desiredGroups[group.ID] = group
	}
	priorGroups := make(map[string]trafficpolicy.WANGroup, len(prior.WANGroups))
	for _, group := range prior.WANGroups {
		priorGroups[group.ID] = group
	}
	return completeRoutePolicyChain(desired, desiredGroups) || completeRoutePolicyChain(prior.RoutePolicies, priorGroups)
}

func completeRoutePolicyChain(routes []trafficpolicy.RoutePolicy, groups map[string]trafficpolicy.WANGroup) bool {
	if len(routes) < 2 {
		return false
	}
	return len(buildRoutePolicyCommandOptions(routes, groups)) == len(routes)
}

func (a Adapter) routeWANGroupFailure(ctx context.Context, channel Channel, transactionID, operation string, cause error, prior Snapshot, current RouteWANGroupPlan) error {
	rollbackErr := errors.Join(cleanupRouteWANGroup(ctx, channel, transactionID, current), applyRouteWANGroupSnapshot(ctx, channel, transactionID, prior, current.IngressVPPInterface))
	rollbackRequest := routeWANGroupSnapshotRequest(transactionID, prior.RoutePolicies, prior.WANGroups)
	readback, err := a.Snapshot(ctx, rollbackRequest)
	if err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	} else if !reflect.DeepEqual(readback.RoutePolicies, prior.RoutePolicies) || !reflect.DeepEqual(readback.WANGroups, prior.WANGroups) {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback readback does not match prior snapshot"))
	}
	result := RollbackSucceeded
	if rollbackErr != nil {
		result = RollbackFailed
	}
	return &RouteWANGroupLifecycleError{Operation: operation, Cause: cause, Rollback: rollbackErr, RollbackResult: result}
}

func cleanupRouteWANGroup(ctx context.Context, channel Channel, transactionID string, plan RouteWANGroupPlan) error {
	var cleanup []error
	ingress := strings.TrimSpace(plan.IngressVPPInterface)
	if ingress == "" {
		ingress = configuredLANVPPInterface()
	}
	for _, route := range routePoliciesForVPPDelete(routePolicyIDs(plan.Routes), plan.Routes) {
		operation := Operation{Name: "vpp.route-policy.rollback-delete", RequestID: transactionID, Resource: route.ID, Payload: route, VPPCtlCommands: deleteRoutePolicyCommands(route.ID)}
		operation = rewriteOperationsInterface([]Operation{operation}, ingress)[0]
		if _, err := doOperation(ctx, channel, operation); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	for _, group := range plan.WANGroups {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.pbr.next-hop-group.rollback-delete", RequestID: transactionID, Resource: group.ID, VPPCtlCommands: deleteWANGroupCommands(group.ID)}); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	return errors.Join(cleanup...)
}

// routePoliciesForVPPDelete returns policies in dependency-safe teardown
// order.  The optimized FIB chain is installed from the terminal policy
// toward the highest-priority policy; it must be removed in the opposite
// direction.  Priority ascending is that inverse and is also harmless for
// non-chained ACL policies.
func routePoliciesForVPPDelete(ids []string, states []trafficpolicy.RoutePolicy) []trafficpolicy.RoutePolicy {
	byID := make(map[string]trafficpolicy.RoutePolicy, len(states))
	for _, state := range states {
		if strings.TrimSpace(state.ID) != "" {
			byID[state.ID] = state
		}
	}
	ordered := make([]trafficpolicy.RoutePolicy, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if state, ok := byID[id]; ok {
			ordered = append(ordered, state)
		} else {
			ordered = append(ordered, trafficpolicy.RoutePolicy{ID: id})
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority < ordered[j].Priority
	})
	return ordered
}

func routePolicyIDs(routes []trafficpolicy.RoutePolicy) []string {
	ids := make([]string, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.ID)
	}
	return ids
}

func applyRouteWANGroupSnapshot(ctx context.Context, channel Channel, transactionID string, snapshot Snapshot, ingressVPPInterface string) error {
	var replay []error
	ingress := strings.TrimSpace(ingressVPPInterface)
	if ingress == "" {
		ingress = configuredLANVPPInterface()
	}
	groups := make(map[string]trafficpolicy.WANGroup, len(snapshot.WANGroups))
	for _, group := range snapshot.WANGroups {
		groups[group.ID] = group
	}
	for _, group := range snapshot.WANGroups {
		operation := Operation{Name: "vpp.pbr.next-hop-group.rollback", RequestID: transactionID, Resource: group.ID, VPPCtlCommands: wanGroupCommands(group)}
		if _, err := doOperation(ctx, channel, operation); err != nil {
			replay = append(replay, err)
		}
	}
	routes := orderedRoutePoliciesForVPP(snapshot.RoutePolicies, groups)
	routeOptions := buildRoutePolicyCommandOptions(routes, groups)
	if err := validateLargeRoutePolicyFallback(routes, routeOptions); err != nil {
		return err
	}
	for _, route := range routes {
		option := routeOptions[route.ID]
		operation := Operation{Name: "vpp.route-policy.rollback", RequestID: transactionID, Resource: route.ID, Payload: route, VPPCtlCommands: routePolicyCommandsWithOptions(route, groups, option)}
		operation = rewriteOperationsInterface([]Operation{operation}, ingress)[0]
		if _, err := doOperation(ctx, channel, operation); err != nil {
			replay = append(replay, err)
		}
	}
	return errors.Join(replay...)
}

func routeWANGroupSnapshotRequest(transactionID string, routes []trafficpolicy.RoutePolicy, groups []trafficpolicy.WANGroup) SnapshotRequest {
	request := SnapshotRequest{TransactionID: transactionID}
	if len(groups) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityWANGroups)
		for _, group := range groups {
			request.WANGroups = append(request.WANGroups, group.ID)
		}
		request.Candidates.WANGroups = append(request.Candidates.WANGroups, groups...)
	}
	if len(routes) > 0 {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityRoutePolicies)
		for _, route := range routes {
			request.RoutePolicies = append(request.RoutePolicies, route.ID)
		}
		request.Candidates.RoutePolicies = append(request.Candidates.RoutePolicies, routes...)
	}
	return request
}

func routeWANGroupSnapshotRequestForPlan(plan RouteWANGroupPlan) SnapshotRequest {
	request := routeWANGroupSnapshotRequest(plan.TransactionID, plan.Routes, plan.WANGroups)
	request.AbsentRoutePolicies = append([]string(nil), plan.DeleteRoutes...)
	request.AbsentWANGroups = append([]string(nil), plan.DeleteWANGroups...)
	request.Candidates.RoutePolicies = appendUniqueRoutePolicies(request.Candidates.RoutePolicies, plan.RoutePolicyContext)
	request.Candidates.RoutePolicies = appendRoutesByID(request.Candidates.RoutePolicies, plan.DeleteRoutes, plan.DeleteRouteState)
	request.Candidates.WANGroups = appendWANGroupsByID(request.Candidates.WANGroups, plan.DeleteWANGroups, plan.DeleteWANState)
	request.Candidates.WANGroups = appendUniqueWANGroups(request.Candidates.WANGroups, plan.WANGroupsContext.RouteGroups())
	if len(plan.WANGroups)+len(plan.DeleteWANGroups) > 0 && !containsSnapshotCapability(request.Capabilities, SnapshotCapabilityWANGroups) {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityWANGroups)
	}
	if len(plan.Routes)+len(plan.DeleteRoutes) > 0 && !containsSnapshotCapability(request.Capabilities, SnapshotCapabilityRoutePolicies) {
		request.Capabilities = append(request.Capabilities, SnapshotCapabilityRoutePolicies)
	}
	return request
}

func appendUniqueRoutePolicies(existing, additions []trafficpolicy.RoutePolicy) []trafficpolicy.RoutePolicy {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, route := range existing {
		if id := strings.TrimSpace(route.ID); id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, route := range additions {
		id := strings.TrimSpace(route.ID)
		if id == "" {
			continue
		}
		if _, found := seen[id]; found {
			continue
		}
		seen[id] = struct{}{}
		existing = append(existing, route)
	}
	return existing
}

func appendUniqueWANGroups(groups, additions []trafficpolicy.WANGroup) []trafficpolicy.WANGroup {
	seen := make(map[string]struct{}, len(groups)+len(additions))
	for _, group := range groups {
		seen[group.ID] = struct{}{}
	}
	for _, group := range additions {
		if _, found := seen[group.ID]; found {
			continue
		}
		seen[group.ID] = struct{}{}
		groups = append(groups, group)
	}
	return groups
}

func containsSnapshotCapability(capabilities []SnapshotCapability, wanted SnapshotCapability) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func deleteWANGroupCommands(id string) []string {
	tableID := wanGroupTableID(id)
	return []string{fmt.Sprintf("?ip table del %d", tableID), fmt.Sprintf("show ip fib table %d", tableID)}
}

func deleteRoutePolicyCommands(id string) []string {
	aclID := stableID("route-acl:"+id, 10000, 49999)
	policyID := stableID("route-abf:"+id, 10000, 8999)
	tableID := stableID("route-table:"+id, 50000, 49999)
	return []string{
		fmt.Sprintf("?abf attach ip4 del policy %d lyroute-$LY_ROUTE_LAN_INTERFACE", policyID),
		fmt.Sprintf("?abf policy del id %d", policyID),
		fmt.Sprintf("?delete acl-plugin acl index %d", aclID),
		fmt.Sprintf("?ip route del table %d 0.0.0.0/0", tableID),
		fmt.Sprintf("?ip route del table %d 0.0.0.0/32", tableID),
		fmt.Sprintf("?ip table del %d", tableID),
		fmt.Sprintf("show acl-plugin acl index %d", aclID),
		fmt.Sprintf("show abf policy %d", policyID),
		fmt.Sprintf("show ip fib table %d", tableID),
	}
}
