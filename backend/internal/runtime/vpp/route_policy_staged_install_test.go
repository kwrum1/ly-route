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

func TestNativeRoutePolicyReplayPreservesPreparedFIB(t *testing.T) {
	operation := Operation{
		Name:     "vpp.route-policy.replay",
		Resource: "proxy-default",
		Payload: trafficpolicy.RoutePolicy{
			ID:       "proxy-default",
			Priority: 100,
			Action:   "route",
			Match: trafficpolicy.Match{
				Sources:      []string{"192.168.50.0/24"},
				Destinations: []string{"0.0.0.0/0"},
			},
			Path: &trafficpolicy.WANPath{NextHop: "198.18.145.162", VPPInterface: "lypxin5567a4"},
		},
	}
	spec := routePolicyVPPCTLSpec{policyID: 15392, tableID: 76716}
	classifier := []string{"set ly-route pre-nat-route add id 15392 priority 100 source 192.168.50.0/24 destination 0.0.0.0/0 protocol any sport 0-65535 dport 0-65535 table 76716 skip-nat"}

	commands := strings.Join(preNATRoutePolicyApplyCommands(operation, spec, classifier), "\n")

	if strings.Contains(commands, "ip table del 76716") || strings.Contains(commands, "ip route del table 76716") {
		t.Fatalf("native replay deleted the prepared private FIB:\n%s", commands)
	}
	if !strings.Contains(commands, classifier[0]) {
		t.Fatalf("native replay omitted classifier activation:\n%s", commands)
	}
}

func TestNativeRoutePolicyWithoutPreparedFIBUsesRefreshLifecycle(t *testing.T) {
	operation := Operation{
		Name:     "vpp.route-policy",
		Resource: "proxy-default",
		Payload: trafficpolicy.RoutePolicy{
			ID:       "proxy-default",
			Priority: 100,
			Action:   "route",
			Path:     &trafficpolicy.WANPath{NextHop: "198.18.145.162", VPPInterface: "lypxin5567a4"},
		},
		VPPCtlCommands: []string{
			"?ip table add 76716",
			"?ip route add table 76716 0.0.0.0/0 via 198.18.145.162 lypxin5567a4",
			"set ly-route pre-nat-route add id 15392 priority 100 source 192.168.50.0/24 destination 0.0.0.0/0 protocol any sport 0-65535 dport 0-65535 table 76716 skip-nat",
		},
	}
	if isStagedRoutePolicyApplyOperation(operation) {
		t.Fatal("native pre-NAT route was incorrectly classified as a prepared ABF route")
	}
	commands := strings.Join(preNATRoutePolicyApplyCommands(operation, routePolicyVPPCTLSpec{policyID: 15392, tableID: 76716}, operation.VPPCtlCommands[2:]), "\n")
	if !strings.Contains(commands, "ip route add table 76716 0.0.0.0/0 via 198.18.145.162 lypxin5567a4") {
		t.Fatalf("native refresh omitted private FIB population:\n%s", commands)
	}
}

func TestNativeRoutePolicyRepairsMissingForwardingPath(t *testing.T) {
	operation := Operation{
		Name:     "vpp.route-policy",
		Resource: "proxy-default",
		VPPCtlCommands: []string{
			"?ip table add 76716",
			"?ip route add table 76716 0.0.0.0/0 via 198.18.145.162 lypxin5567a4",
		},
	}
	results := []VPPCTLCommandResult{{
		Command: "show ip fib table 76716",
		Stdout: "ipv4-VRF:76716, fib_index:10, flow hash:[src dst sport dport proto ]\n" +
			"0.0.0.0/0\n  unicast-ip4-chain\n  [@0]: dpo-load-balance: [proto:ip4 index:90 buckets:1]\n  [0] [@0]: dpo-drop ip4\n",
	}}
	commands := preNATRoutePolicyRepairCommands(operation, routePolicyVPPCTLSpec{tableID: 76716}, results)
	if len(commands) != 2 {
		t.Fatalf("repair commands = %v, want route add and readback", commands)
	}
	if commands[0] != "ip route add table 76716 0.0.0.0/0 via 198.18.145.162 lypxin5567a4" {
		t.Fatalf("repair route command = %q", commands[0])
	}
	if strings.HasPrefix(commands[0], "?") {
		t.Fatalf("repair route command must fail visibly: %q", commands[0])
	}
}

func TestNativeRoutePolicyDoesNotRepairHealthyForwardingPath(t *testing.T) {
	operation := Operation{
		Name:     "vpp.route-policy",
		Resource: "proxy-default",
		VPPCtlCommands: []string{
			"?ip table add 76716",
			"?ip route add table 76716 0.0.0.0/0 via 198.18.145.162 lypxin5567a4",
		},
	}
	results := []VPPCTLCommandResult{{
		Command: "show ip fib table 76716",
		Stdout: "ipv4-VRF:76716, fib_index:10, flow hash:[src dst sport dport proto ]\n" +
			"0.0.0.0/0\n  unicast-ip4-chain\n  [@0]: dpo-load-balance: [proto:ip4 index:90 buckets:1]\n  [0] [@5]: ipv4 via 198.18.145.162 lypxin5567a4: mtu:1460 next:9 flags:[]\n",
	}}
	if commands := preNATRoutePolicyRepairCommands(operation, routePolicyVPPCTLSpec{tableID: 76716}, results); len(commands) != 0 {
		t.Fatalf("healthy route produced repair commands: %v", commands)
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
