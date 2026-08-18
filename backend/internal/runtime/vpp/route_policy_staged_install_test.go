package vpp

import (
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestStageRoutePolicyInstallBuildsFIBsBeforeABFAttach(t *testing.T) {
	policies := []trafficpolicy.RoutePolicy{
		{ID: "geoip-cn", Priority: 10, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Destinations: []string{"10.0.0.0/8"}}},
		{ID: "proxy-default", Priority: 100, Action: "route", Egress: "proxy", Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/0"}}},
	}
	options := buildRoutePolicyCommandOptions(policies, nil)
	operations := []Operation{
		{Name: "vpp.route-policy", Resource: policies[1].ID, Payload: policies[1], VPPCtlCommands: routePolicyCommandsWithOptions(policies[1], nil, options[policies[1].ID])},
		{Name: "vpp.route-policy", Resource: policies[0].ID, Payload: policies[0], VPPCtlCommands: routePolicyCommandsWithOptions(policies[0], nil, options[policies[0].ID])},
	}

	staged := stageRoutePolicyInstallOperations(operations)
	if len(staged) != 6 {
		t.Fatalf("staged operation count = %d, want 6", len(staged))
	}
	wantNames := []string{
		"vpp.route-table.prepare", "vpp.route-table.prepare",
		"vpp.route-table.populate", "vpp.route-table.populate",
		"vpp.route-policy", "vpp.route-policy",
	}
	for index, want := range wantNames {
		if staged[index].Name != want {
			t.Fatalf("operation %d name = %q, want %q", index, staged[index].Name, want)
		}
	}
	if staged[4].Resource != "geoip-cn" || staged[5].Resource != "proxy-default" {
		t.Fatalf("activation order = %q, %q; want priority 10 then 100", staged[4].Resource, staged[5].Resource)
	}
	for _, operation := range staged[:4] {
		if strings.Contains(strings.Join(operation.VPPCtlCommands, "\n"), "abf attach") {
			t.Fatalf("ABF was attached during table preparation: %#v", operation)
		}
	}
	for _, operation := range staged[4:] {
		commands := strings.Join(operation.VPPCtlCommands, "\n")
		if strings.Contains(commands, "ip route add table") || strings.Contains(commands, vppRouteBatchBegin) {
			t.Fatalf("route population leaked into activation: %s", commands)
		}
	}
}

func TestRoutePolicyFullRebuildIncludesOptimizedSuffixAfterSourceExceptions(t *testing.T) {
	routes := []trafficpolicy.RoutePolicy{
		{ID: "client-exception", Priority: 1, Action: "route", Match: trafficpolicy.Match{Sources: []string{"192.168.50.10/32"}, Destinations: []string{"0.0.0.0/0"}}},
		{ID: "geoip-cn", Priority: 10, Action: "route", Match: trafficpolicy.Match{Destinations: []string{"10.0.0.0/8"}}},
		{ID: "proxy-default", Priority: 100, Action: "route", Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/0"}}},
	}
	if !routePolicyFullRebuildRequired(RouteWANGroupPlan{Routes: routes, RoutePolicyContext: routes}, Snapshot{}) {
		t.Fatal("destination-only FIB suffix must be rebuilt atomically")
	}
}
