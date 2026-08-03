package vpp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
	TransactionID    string
	Routes           []trafficpolicy.RoutePolicy
	WANGroups        []trafficpolicy.WANGroup
	WANGroupsContext WANGroupsContext
	DeleteRoutes     []string
	DeleteWANGroups  []string
	DeleteRouteState []trafficpolicy.RoutePolicy
	DeleteWANState   []trafficpolicy.WANGroup
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
	for _, route := range plan.Routes {
		operations = append(operations, Operation{Name: "vpp.route-policy", RequestID: transactionID, Resource: route.ID, Payload: route, VPPCtlCommands: routePolicyCommands(route, groups)})
	}
	for _, id := range plan.DeleteWANGroups {
		operations = append(operations, Operation{Name: "vpp.pbr.next-hop-group", RequestID: transactionID, Resource: id, VPPCtlCommands: deleteWANGroupCommands(id)})
	}
	for _, id := range plan.DeleteRoutes {
		operation := Operation{Name: "vpp.route-policy", RequestID: transactionID, Resource: id, VPPCtlCommands: deleteRoutePolicyCommands(id)}
		for _, state := range plan.DeleteRouteState {
			if state.ID == id {
				operation.Payload = state
				break
			}
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func (a Adapter) ApplyRouteWANGroup(ctx context.Context, plan RouteWANGroupPlan, prior Snapshot, attempted ...RouteWANGroupPlan) (RouteWANGroupApplyResult, error) {
	plan.DeleteRouteState = appendRoutesByID(plan.DeleteRouteState, plan.DeleteRoutes, prior.RoutePolicies)
	plan.DeleteWANState = appendWANGroupsByID(plan.DeleteWANState, plan.DeleteWANGroups, prior.WANGroups)
	rollbackPlan := plan
	if len(attempted) > 0 {
		rollbackPlan = attempted[0]
	}
	operations, err := BuildRouteWANGroupOperations(plan)
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
	for _, operation := range operations {
		if _, err := doOperation(ctx, channel, operation); err != nil {
			return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, operation.Name, err, prior, rollbackPlan)
		}
	}
	readback, err := a.Snapshot(ctx, routeWANGroupSnapshotRequestForPlan(plan))
	if err != nil {
		return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	if err := verifyRouteWANGroupDeletes(readback, plan); err != nil {
		return RouteWANGroupApplyResult{}, a.routeWANGroupFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	return RouteWANGroupApplyResult{Receipt: Receipt{RequestID: plan.TransactionID, Operations: operations}, Readback: readback}, nil
}

func (a Adapter) routeWANGroupFailure(ctx context.Context, channel Channel, transactionID, operation string, cause error, prior Snapshot, current RouteWANGroupPlan) error {
	rollbackErr := errors.Join(cleanupRouteWANGroup(ctx, channel, transactionID, current), applyRouteWANGroupSnapshot(ctx, channel, transactionID, prior))
	readback, err := a.Snapshot(ctx, routeWANGroupSnapshotRequest(transactionID, prior.RoutePolicies, prior.WANGroups))
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
	for _, route := range plan.Routes {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.route-policy.rollback-delete", RequestID: transactionID, Resource: route.ID, Payload: route, VPPCtlCommands: deleteRoutePolicyCommands(route.ID)}); err != nil {
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

func applyRouteWANGroupSnapshot(ctx context.Context, channel Channel, transactionID string, snapshot Snapshot) error {
	var replay []error
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
	for _, route := range snapshot.RoutePolicies {
		operation := Operation{Name: "vpp.route-policy.rollback", RequestID: transactionID, Resource: route.ID, Payload: route, VPPCtlCommands: routePolicyCommands(route, groups)}
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
	return []string{fmt.Sprintf("?abf attach ip4 del policy %d lyroute-$LY_ROUTE_LAN_INTERFACE", policyID), fmt.Sprintf("?abf policy del id %d", policyID), fmt.Sprintf("?delete acl-plugin acl index %d", aclID), fmt.Sprintf("?ip table del %d", tableID), fmt.Sprintf("show acl-plugin acl index %d", aclID), fmt.Sprintf("show abf policy %d", policyID), fmt.Sprintf("show ip fib table %d", tableID)}
}
