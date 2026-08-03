package vpp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestGatewayRouteWANGroupApplySuccessReadsBackTypedState(t *testing.T) {
	// Given
	client := &routeWANLifecycleClient{replies: map[string]Reply{
		"vpp.pbr.next-hop-group":          {Payload: "applied"},
		"vpp.route-policy":                {Payload: "applied"},
		"vpp.pbr.next-hop-group.snapshot": {Payload: WANGroupReadback{Groups: []trafficpolicy.WANGroup{{ID: "wan-primary", Members: []string{"wan0", "wan1"}}}}},
		"vpp.route-policy.snapshot":       {Payload: RoutePolicyReadback{Policies: []trafficpolicy.RoutePolicy{{ID: "route-office", Action: "route", Egress: "wan-primary"}}}},
	}}
	plan := RouteWANGroupPlan{
		TransactionID: "txn-route-apply",
		WANGroups:     []trafficpolicy.WANGroup{{ID: "wan-primary", Members: []string{"wan0", "wan1"}}},
		Routes:        []trafficpolicy.RoutePolicy{{ID: "route-office", Action: "route", Egress: "wan-primary"}},
	}

	// When
	result, err := (Adapter{Client: client}).ApplyRouteWANGroup(context.Background(), plan, routeWANPriorSnapshot())

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Readback.TransactionID != plan.TransactionID || len(result.Readback.WANGroups) != 1 || len(result.Readback.RoutePolicies) != 1 {
		t.Fatalf("readback = %#v, want typed route/WAN-group state", result.Readback)
	}
	if len(result.Receipt.Operations) != 2 || client.operations[0].Name != "vpp.pbr.next-hop-group" || client.operations[1].Name != "vpp.route-policy" {
		t.Fatalf("operations = %#v, want WAN group before route", client.operations)
	}
}

func TestBuildRouteWANGroupOperations_usesReadOnlyContextWithoutGroupCommands(t *testing.T) {
	// Given
	group := trafficpolicy.WANGroup{ID: "wan-primary", Members: []string{"wan0"}}
	plan := RouteWANGroupPlan{
		TransactionID:    "txn-route-context",
		WANGroupsContext: NewWANGroupsContext(nil, []trafficpolicy.WANGroup{group}),
		Routes:           []trafficpolicy.RoutePolicy{{ID: "route-office", Action: "route", Egress: group.ID}},
	}

	// When
	operations, err := BuildRouteWANGroupOperations(plan)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Name != "vpp.route-policy" {
		t.Fatalf("operations = %#v, want one route operation", operations)
	}
	wanGroupOperations := 0
	for _, operation := range operations {
		if operation.Name == "vpp.pbr.next-hop-group" {
			wanGroupOperations++
		}
	}
	if wanGroupOperations != 0 {
		t.Fatalf("route WAN-group operation count = %d, want zero", wanGroupOperations)
	}
	if !strings.Contains(strings.Join(operations[0].VPPCtlCommands, "\n"), fmt.Sprintf("table %d", wanGroupTableID(group.ID))) {
		t.Fatalf("route commands = %#v, want WAN group table", operations[0].VPPCtlCommands)
	}
}

func TestGatewayRouteWANGroupDeleteExecutesAndConfirmsAbsence(t *testing.T) {
	// Given
	client := &routeWANLifecycleClient{replies: map[string]Reply{
		"vpp.pbr.next-hop-group.snapshot": {Payload: WANGroupReadback{}},
		"vpp.route-policy.snapshot":       {Payload: RoutePolicyReadback{}},
	}}
	plan := RouteWANGroupPlan{TransactionID: "txn-route-delete", DeleteWANGroups: []string{"wan-primary"}, DeleteRoutes: []string{"route-office"}}

	// When
	result, err := (Adapter{Client: client}).ApplyRouteWANGroup(context.Background(), plan, routeWANPriorSnapshot())

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Readback.WANGroups) != 0 || len(result.Readback.RoutePolicies) != 0 {
		t.Fatalf("readback = %#v, want confirmed absence", result.Readback)
	}
	if len(result.Receipt.Operations) != 2 || !strings.Contains(strings.Join(client.operations[0].VPPCtlCommands, "\n"), "del") {
		t.Fatalf("operations = %#v, want live deletes", client.operations)
	}
}

func TestGatewayRouteWANGroupFailureRestoresPriorSnapshot(t *testing.T) {
	// Given
	prior := routeWANPriorSnapshot()
	client := &routeWANLifecycleClient{
		errors: map[string]error{"vpp.route-policy": errors.New("route command failed")},
		replies: map[string]Reply{
			"vpp.pbr.next-hop-group":          {Payload: "applied"},
			"vpp.pbr.next-hop-group.snapshot": {Payload: WANGroupReadback{Groups: prior.WANGroups}},
			"vpp.route-policy.snapshot":       {Payload: RoutePolicyReadback{Policies: prior.RoutePolicies}},
		},
	}

	// When
	_, err := (Adapter{Client: client}).ApplyRouteWANGroup(context.Background(), RouteWANGroupPlan{TransactionID: "txn-route-failure", WANGroups: prior.WANGroups, Routes: prior.RoutePolicies}, prior)

	// Then
	var lifecycleErr *RouteWANGroupLifecycleError
	if !errors.As(err, &lifecycleErr) || lifecycleErr.RollbackResult != RollbackSucceeded {
		t.Fatalf("error = %T %v, want successful snapshot rollback", err, err)
	}
	if client.rollbackOperations == 0 {
		t.Fatal("rollback issued no VPP operations")
	}
}

func TestGatewayRouteWANGroupReportsRollbackFailure(t *testing.T) {
	// Given
	rollbackCommandErr := errors.New("rollback command failed")
	client := &routeWANLifecycleClient{
		errors: map[string]error{"vpp.route-policy": errors.New("route command failed"), "vpp.pbr.next-hop-group.rollback": rollbackCommandErr},
	}

	// When
	_, err := (Adapter{Client: client}).ApplyRouteWANGroup(context.Background(), RouteWANGroupPlan{TransactionID: "txn-route-rollback-failure", Routes: routeWANPriorSnapshot().RoutePolicies}, routeWANPriorSnapshot())

	// Then
	var lifecycleErr *RouteWANGroupLifecycleError
	if !errors.As(err, &lifecycleErr) || lifecycleErr.RollbackResult != RollbackFailed || !errors.Is(lifecycleErr.Rollback, rollbackCommandErr) {
		t.Fatalf("error = %T %v, rollback = %v; want exact rollback command failure", err, err, lifecycleErr.Rollback)
	}
}

func TestGatewayRouteWANGroupRejectsIncompleteReadback(t *testing.T) {
	// Given
	client := &routeWANLifecycleClient{replies: map[string]Reply{
		"vpp.pbr.next-hop-group":          {Payload: "applied"},
		"vpp.route-policy":                {Payload: "applied"},
		"vpp.pbr.next-hop-group.snapshot": {Payload: WANGroupReadback{Groups: []trafficpolicy.WANGroup{{ID: "wan-primary"}}}},
		"vpp.route-policy.snapshot":       {Payload: RoutePolicyReadback{}},
	}}

	// When
	_, err := (Adapter{Client: client}).ApplyRouteWANGroup(context.Background(), RouteWANGroupPlan{TransactionID: "txn-route-incomplete", WANGroups: routeWANPriorSnapshot().WANGroups, Routes: routeWANPriorSnapshot().RoutePolicies}, routeWANPriorSnapshot())

	// Then
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("error = %v, want deterministic incomplete readback", err)
	}
}

func routeWANPriorSnapshot() Snapshot {
	return Snapshot{
		TransactionID: "txn-route-prior",
		RequestID:     "txn-route-prior",
		WANGroups:     []trafficpolicy.WANGroup{{ID: "wan-primary", Members: []string{"wan0", "wan1"}}},
		RoutePolicies: []trafficpolicy.RoutePolicy{{ID: "route-office", Action: "route", Egress: "wan-primary"}},
		Hash:          "prior-route-hash",
	}
}

type routeWANLifecycleClient struct {
	replies            map[string]Reply
	errors             map[string]error
	operations         []Operation
	rollbackOperations int
}

func (client *routeWANLifecycleClient) OpenChannel(context.Context) (Channel, error) {
	return &routeWANLifecycleChannel{client: client}, nil
}

type routeWANLifecycleChannel struct{ client *routeWANLifecycleClient }

func (channel *routeWANLifecycleChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	channel.client.operations = append(channel.client.operations, operation)
	if strings.HasSuffix(operation.Name, ".rollback") {
		channel.client.rollbackOperations++
	}
	if err := channel.client.errors[operation.Name]; err != nil {
		return Reply{}, err
	}
	return channel.client.replies[operation.Name], nil
}

func (channel *routeWANLifecycleChannel) Close() error { return nil }
