package vpp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestVerifyABFPolicyIgnoresNestedDPOPathDetails(t *testing.T) {
	results := []VPPCTLCommandResult{{
		Command: "show abf policy 11198",
		Stdout: "abf:[1]: policy:11198 acl:12345\n" +
			"     path-list:[7] locks:1 flags:shared,no-uRPF, uRPF-list: None\n" +
			"      path:[8] pl-index:7 ip4 weight=1 pref=0 attached-nexthop:  oper-flags:resolved,\n" +
			"        198.18.34.89 lypxinffdc88 (p2p)\n" +
			"      [@0]: ipv4 [features] via 198.18.34.89 lypxinffdc88: mtu:1460\n" +
			"             stacked-on:\n" +
			"               [@2]: lypxoutffdc88-tx-dpo:\n",
	}}

	if err := verifyABFPolicy(results, abfCandidateProof{policyID: 11198, aclID: 12345, via: "198.18.34.89 lypxinffdc88"}); err != nil {
		t.Fatalf("verifyABFPolicy() error = %v", err)
	}
}

func TestVerifyABFPolicyRejectsUnresolvedIfIndexPath(t *testing.T) {
	results := []VPPCTLCommandResult{
		{Command: "show abf policy 11198", Stdout: "abf:[4]: policy:11198 acl:12345\n" +
			"     path-list:[105] locks:1 flags:shared len:1\n" +
			"      path:[122] pl-index:105 ip4 weight=1 pref=0 attached-nexthop: oper-flags:drop,\n" +
			"        198.18.34.90 if_index:8\n" +
			"        unresolved\n"},
		{Command: "show interface", Stdout: "lypxinffdc88 8 up 1460/0/0/0\n"},
	}
	if err := verifyABFPolicy(results, abfCandidateProof{policyID: 11198, aclID: 12345, via: "198.18.34.90 lypxinffdc88"}); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("verifyABFPolicy() error = %v, want unresolved-path failure", err)
	}
}

func TestVerifyABFPolicyRecognizesStaleUnresolvedPathForReconciliation(t *testing.T) {
	results := []VPPCTLCommandResult{{
		Command: "show abf policy 11198",
		Stdout: "abf:[4]: policy:11198 acl:12345\n" +
			"     path-list:[105] locks:1 flags:shared len:1\n" +
			"      path:[122] pl-index:105 ip4 weight=1 pref=0 attached-nexthop: oper-flags:drop,\n" +
			"        198.18.34.90 if_index:8\n" +
			"        unresolved\n",
	}}
	if err := verifyABFPolicy(results, abfCandidateProof{policyID: 11198, aclID: 12345, via: "198.18.34.90 lypxinffdc88"}); err != nil {
		t.Fatalf("prepare snapshot must recognize stale path for reconciliation: %v", err)
	}
}

func TestParseFIBResultTreatsDropDPOAsMissingForwardingPath(t *testing.T) {
	results := []VPPCTLCommandResult{{
		Command: "show ip fib table 92258",
		Stdout: "ipv4-VRF:92258, fib_index:5, flow hash:[src dst sport dport proto ]\n" +
			"0.0.0.0/0\n" +
			"  unicast-ip4-chain\n" +
			"  [@0]: dpo-load-balance: [proto:ip4 index:63 buckets:1]\n" +
			"    [0] [@0]: dpo-drop ip4\n",
	}}
	paths, err := parseFIBResult(results, 92258)
	if err != nil || len(paths) != 0 {
		t.Fatalf("parseFIBResult() paths = %#v, error = %v; want known missing path", paths, err)
	}
}

func TestParseFIBResultIgnoresMainTableFallbackAfterVPP2510WANPaths(t *testing.T) {
	const tableID = 86417
	results := []VPPCTLCommandResult{{
		Command: fmt.Sprintf("show ip fib table %d", tableID),
		Stdout: "ipv4-VRF:86417, fib_index:14, flow hash:[src dst sport dport proto ] epoch:0 flags:none locks:[CLI:1, ]\n" +
			"0.0.0.0/0\n" +
			"  unicast-ip4-chain\n" +
			"  [@0]: dpo-load-balance: [proto:ip4 index:133 buckets:16 uRPF:114 to:[0:0]]\n" +
			"    [0-2] [@6]: ipv4 via 0.0.0.0 pppoe_session9: mtu:9000 next:3 flags:[]\n" +
			"    [3-9] [@6]: ipv4 via 0.0.0.0 pppoe_session8: mtu:9000 next:3 flags:[]\n" +
			"    [10-15] [@14]: dst-address,unicast lookup in ipv4-VRF:0\n" +
			"0.0.0.0/32\n" +
			"  unicast-ip4-chain\n" +
			"  [@0]: dpo-load-balance: [proto:ip4 index:163 buckets:1 uRPF:199 to:[0:0]]\n" +
			"    [0] [@0]: dpo-drop ip4\n",
	}}
	paths, err := parseFIBResult(results, tableID)
	if err != nil {
		t.Fatalf("parseFIBResult() error = %v", err)
	}
	if len(paths) != 2 || paths[0].via != "0.0.0.0 pppoe_session9: mtu:9000 next:3 flags:[]" || paths[1].via != "0.0.0.0 pppoe_session8: mtu:9000 next:3 flags:[]" {
		t.Fatalf("parseFIBResult() paths = %#v, want only the two configured PPPoE paths", paths)
	}
}

func TestParseFIBResultAcceptsCoveringWANGroupDefaults(t *testing.T) {
	const tableID = 86417
	results := []VPPCTLCommandResult{{
		Command: fmt.Sprintf("show ip fib table %d", tableID),
		Stdout: fmt.Sprintf(`ipv4-VRF:%d, fib_index:10, flow hash:[src dst sport dport proto]
0.0.0.0/0
  unicast-ip4-chain
  [@0]: dpo-load-balance: [proto:ip4 index:63 buckets:1]
    [0] [@0]: dpo-drop ip4
0.0.0.0/1
  unicast-ip4-chain
  [@0]: dpo-load-balance: [proto:ip4 index:70 buckets:2]
    [0] [@6]: ipv4 via 0.0.0.0 pppoe_session_a: mtu:9000
    [1] [@6]: ipv4 via 0.0.0.0 pppoe_session_b: mtu:9000
128.0.0.0/1
  unicast-ip4-chain
  [@0]: dpo-load-balance: [proto:ip4 index:71 buckets:2]
    [0] [@6]: ipv4 via 0.0.0.0 pppoe_session_a: mtu:9000
    [1] [@6]: ipv4 via 0.0.0.0 pppoe_session_b: mtu:9000
224.0.0.0/4
  unicast-ip4-chain
  [@0]: dpo-load-balance: [proto:ip4 index:72 buckets:1]
    [0] [@0]: dpo-drop ip4
`, tableID),
	}}
	paths, err := parseFIBResult(results, tableID)
	if err != nil {
		t.Fatalf("parseFIBResult() error = %v", err)
	}
	if len(paths) != 2 || paths[0].via != "0.0.0.0 pppoe_session_a: mtu:9000" || paths[1].via != "0.0.0.0 pppoe_session_b: mtu:9000" {
		t.Fatalf("parseFIBResult() paths = %#v, want two unique covering paths", paths)
	}
}

func TestSelectRoutePoliciesAllowsVerifiedMissingDrift(t *testing.T) {
	policies, err := selectRoutePolicies(nil, SnapshotRequest{AllowMissing: true, RoutePolicies: []string{"route-100"}})
	if err != nil || len(policies) != 0 {
		t.Fatalf("selectRoutePolicies() policies = %#v, error = %v", policies, err)
	}
}

func TestDecodeWANGroupsAllowsMissingTableAfterVPPRestart(t *testing.T) {
	candidate := trafficpolicy.WANGroup{
		ID:      "wan-primary",
		Mode:    trafficpolicy.WANGroupWeighted,
		Members: []string{"wan0", "wan1"},
		Paths: map[string]trafficpolicy.WANPath{
			"wan0": {VPPInterface: "pppoe_session0", NextHop: "10.67.0.1"},
			"wan1": {VPPInterface: "pppoe_session1", NextHop: "10.68.0.1"},
		},
	}
	request := SnapshotRequest{
		AllowMissing: true,
		WANGroups:    []string{candidate.ID},
		Candidates:   SnapshotCandidates{WANGroups: []trafficpolicy.WANGroup{candidate}},
	}
	readback, err := decodeVPPCTLWANGroups(request, []VPPCTLCommandResult{{
		Command: fmt.Sprintf("show ip fib table %d", wanGroupTableID(candidate.ID)),
		Stdout: fmt.Sprintf("ipv4-VRF:%d, fib_index:5, flow hash:[src dst sport dport proto ]\n", wanGroupTableID(candidate.ID)) +
			"0.0.0.0/0\n" +
			"  unicast-ip4-chain\n" +
			"  [@0]: dpo-load-balance: [proto:ip4 index:63 buckets:1]\n" +
			"    [0] [@0]: dpo-drop ip4\n",
	}})
	if err != nil || len(readback.Groups) != 0 {
		t.Fatalf("missing WAN-group FIB readback = %#v, %v; want repairable empty state", readback, err)
	}
	selected, err := selectWANGroups(readback.Groups, request)
	if err != nil || len(selected) != 0 {
		t.Fatalf("selected missing WAN group = %#v, %v; want repairable empty state", selected, err)
	}
}

func TestDecodeQoSAllowsVerifiedMissingMatchedRateAfterVPPRestart(t *testing.T) {
	candidate := flow.VPPObjectGroup{Kind: "vpp.behavior.rate", Objects: []flow.VPPObject{{
		RuleID: "flow-30", Granularity: flow.RuleGranularity, Action: flow.ActionPolicer,
		Policer: &flow.Policer{RateBPS: 1_000_000, BurstBPS: 100_000},
		Match:   flow.Match{Sources: []string{"192.168.50.102/32"}},
	}}}
	request := SnapshotRequest{
		AllowMissing: true,
		QoS:          []string{candidate.Kind},
		Candidates:   SnapshotCandidates{QoS: []flow.VPPObjectGroup{candidate}},
	}
	readback, err := decodeVPPCTLQoS(request, []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: ""},
		{Command: "show policer name ly_route_flow_30", Stdout: ""},
		{Command: "show ly-route flow-rate", Stdout: ""},
	})
	if err != nil || len(readback.Groups) != 0 {
		t.Fatalf("decode missing matched rate = %#v, %v; want repairable empty state", readback, err)
	}
	selected, err := selectQoS(readback.Groups, request)
	if err != nil || len(selected) != 0 {
		t.Fatalf("select missing matched rate = %#v, %v; want repairable empty state", selected, err)
	}
}

func TestDecodeQoSRepairsPolicerCreatedByOlderUnitConversion(t *testing.T) {
	candidate := flow.VPPObjectGroup{Kind: "vpp.behavior.rate", Objects: []flow.VPPObject{{
		RuleID: "flow-30", Granularity: flow.RuleGranularity, Action: flow.ActionPolicer,
		Policer: &flow.Policer{RateBPS: 20_000_000, BurstBPS: 2_000_000},
		Match:   flow.Match{Sources: []string{"192.168.50.102/32"}},
	}}}
	results := []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: "acl-index 2 count 3 tag {ly-route-flow_30}\n  0: ipv4 permit src 192.168.50.102/32 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n  1: ipv4 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n  2: ipv6 permit src ::/0 dst ::/0 proto 0 sport 0-65535 dport 0-65535\n"},
		{Command: "show policer name ly_route_flow_30", Stdout: "Name \"ly_route_flow_30\" type 1r2c cir 20000 eir 0 cb 2000 eb 0\nrate type kbps, round type closest\nconform action transmit, exceed action drop, violate action drop\nPolicer at index 0: single rate, not color-aware\ncir 1 tok/period, pir 1 tok/period, scale 0\ncur lim 1, cur bkt 1, ext lim 0, ext bkt 0\nlast update 1\nconform 0 packets, 0 bytes\nexceed 0 packets, 0 bytes\nviolate 0 packets, 0 bytes\n-----------\n"},
		{Command: "show ly-route flow-rate", Stdout: ""},
	}
	request := SnapshotRequest{QoS: []string{candidate.Kind}, Candidates: SnapshotCandidates{QoS: []flow.VPPObjectGroup{candidate}}}
	if _, err := decodeVPPCTLQoS(request, results); err == nil {
		t.Fatal("strict QoS readback accepted an old policer burst conversion")
	}
	request.AllowMissing = true
	readback, err := decodeVPPCTLQoS(request, results)
	if err != nil || len(readback.Groups) != 0 {
		t.Fatalf("repair readback = %#v, %v; want old policer treated as repairable drift", readback, err)
	}
}

func TestDecodeQoSRecognizesLegacyV4OnlyRateACLAsRepairableDrift(t *testing.T) {
	candidate := flow.VPPObjectGroup{Kind: "vpp.behavior.rate", Objects: []flow.VPPObject{{
		RuleID: "flow-30", Granularity: flow.RuleGranularity, Action: flow.ActionPolicer,
		Policer: &flow.Policer{RateBPS: 1_000_000, BurstBPS: 100_000},
		Match:   flow.Match{Sources: []string{"192.168.50.102/32"}, Protocols: []string{"tcp"}},
	}}}
	request := SnapshotRequest{
		QoS:        []string{candidate.Kind},
		Candidates: SnapshotCandidates{QoS: []flow.VPPObjectGroup{candidate}},
	}
	readback, err := decodeVPPCTLQoS(request, []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: "acl-index 2 count 2 tag {ly-route-flow_30}\n  0: ipv4 permit src 192.168.50.102/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 0-65535\n  1: ipv4 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n"},
		{Command: "show policer name ly_route_flow_30", Stdout: "Name \"ly_route_flow_30\" type 1r2c cir 1000 eir 0 cb 100 eb 0\n"},
		{Command: "show ly-route flow-rate", Stdout: ""},
	})
	if err != nil || len(readback.Groups) != 0 {
		t.Fatalf("decode legacy matched rate = %#v, %v; want repairable empty state", readback, err)
	}
}

func TestDecodeRoutesAllowsVerifiedMissingRouteAfterVPPRestart(t *testing.T) {
	route := trafficpolicy.RoutePolicy{ID: "route-100", Action: "route", Match: trafficpolicy.Match{Sources: []string{"192.0.2.10/32"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}}}
	policyID := stableID("route-abf:"+route.ID, 10000, 8999)
	readback, err := decodeVPPCTLRoutes(SnapshotRequest{
		AllowMissing:  true,
		RoutePolicies: []string{route.ID},
		Candidates:    SnapshotCandidates{RoutePolicies: []trafficpolicy.RoutePolicy{route}},
	}, []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: ""},
		{Command: fmt.Sprintf("show abf policy %d", policyID), Stdout: ""},
	})
	if err != nil || len(readback.Policies) != 0 {
		t.Fatalf("decodeVPPCTLRoutes() = %#v, %v; want repairable missing route", readback, err)
	}
}

func TestDecodeRoutesAllowsTaggedACLWithoutABFAsRepairableDrift(t *testing.T) {
	route := trafficpolicy.RoutePolicy{ID: "route-100", Action: "route", Match: trafficpolicy.Match{Sources: []string{"192.0.2.10/32"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}}}
	policyID := stableID("route-abf:"+route.ID, 10000, 8999)
	readback, err := decodeVPPCTLRoutes(SnapshotRequest{
		AllowMissing:  true,
		RoutePolicies: []string{route.ID},
		Candidates:    SnapshotCandidates{RoutePolicies: []trafficpolicy.RoutePolicy{route}},
	}, []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: "acl-index 7 count 1 tag {ly-route-route_100}\n  0: ipv4 permit src 192.0.2.10/32 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n"},
		{Command: fmt.Sprintf("show abf policy %d", policyID), Stdout: fmt.Sprintf("Invalid policy ID:%d\n", policyID)},
	})
	if err != nil || len(readback.Policies) != 0 {
		t.Fatalf("decodeVPPCTLRoutes() = %#v, %v; want repairable partial route drift", readback, err)
	}
}

func TestDecodeRoutesAllowsOlderABFPathAsRepairableDrift(t *testing.T) {
	route := trafficpolicy.RoutePolicy{ID: "route-10", Priority: 10, Action: "route", Match: trafficpolicy.Match{Sources: []string{"0.0.0.0/0"}, Destinations: []string{"203.0.113.0/24"}, Protocols: []string{"any"}}, Path: &trafficpolicy.WANPath{VPPInterface: "pppoe_session1", NextHop: "10.67.0.1"}}
	fallback := trafficpolicy.RoutePolicy{ID: "route-100", Priority: 100, Action: "route", Match: trafficpolicy.Match{Sources: []string{"0.0.0.0/0"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}}, Path: &trafficpolicy.WANPath{VPPInterface: "lypxin100", NextHop: "198.18.0.1"}}
	policyID := stableID("route-abf:"+route.ID, 10000, 8999)
	readback, err := decodeVPPCTLRoutes(SnapshotRequest{
		AllowMissing:  true,
		RoutePolicies: []string{route.ID},
		Candidates:    SnapshotCandidates{RoutePolicies: []trafficpolicy.RoutePolicy{route, fallback}},
	}, []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: "acl-index 2 count 1 tag {ly-route-route_10}\n  0: ipv4 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n"},
		{Command: fmt.Sprintf("show abf policy %d", policyID), Stdout: fmt.Sprintf("abf:[2]: policy:%d acl:2\n path-list:[1] locks:1 len:1\n  path:[1] pl-index:1 ip4 weight=1 pref=0\n    10.67.0.1 pppoe_session1 (p2p)\n", policyID)},
	})
	if err != nil || len(readback.Policies) != 0 {
		t.Fatalf("decodeVPPCTLRoutes() = %#v, %v; want older path treated as repairable drift", readback, err)
	}
}

func TestDecodeRoutesUsesABFReferencedACLWhenTaggedInventoryHasStaleDuplicate(t *testing.T) {
	route := trafficpolicy.RoutePolicy{
		ID:     "route-duplicate",
		Action: "route",
		Match:  trafficpolicy.Match{Sources: []string{"192.0.2.10/32"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}},
		Path:   &trafficpolicy.WANPath{VPPInterface: "pppoe_session1", NextHop: "10.67.0.1"},
	}
	policyID := stableID("route-abf:"+route.ID, 10000, 8999)
	tableID := stableID("route-table:"+route.ID, 50000, 49999)
	const activeACLID = 7
	readback, err := decodeVPPCTLRoutes(SnapshotRequest{
		RoutePolicies: []string{route.ID},
		Candidates:    SnapshotCandidates{RoutePolicies: []trafficpolicy.RoutePolicy{route}},
	}, []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: "acl-index 7 count 1 tag {ly-route-route_duplicate}\n  0: ipv4 permit src 192.0.2.10/32 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\nacl-index 8 count 1 tag {ly-route-route_duplicate}\n  0: ipv4 permit src 192.0.2.10/32 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n"},
		{Command: fmt.Sprintf("show abf policy %d", policyID), Stdout: fmt.Sprintf("abf:[0]: policy:%d acl:%d\n path-list:[17] locks:1 flags:shared len:1\n  path:[21] pl-index:17 ip4 weight=1 pref=0\n    10.67.0.1 pppoe_session1 (p2p)\n", policyID, activeACLID)},
		{Command: fmt.Sprintf("show ip fib table %d", tableID), Stdout: fmt.Sprintf("ipv4-VRF:%d, fib_index:3, flow hash:[src dst sport dport proto]\n0.0.0.0/0\n  unicast-ip4-chain\n    [@0]: ipv4 via 0.0.0.0 pppoe_session1\n", tableID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readback.Policies, []trafficpolicy.RoutePolicy{route}) {
		t.Fatalf("routes = %#v, want active ABF route despite stale duplicate ACL", readback.Policies)
	}
}

func TestDecodeRoutesReadsBackRadixPolicyWithoutACLOrABF(t *testing.T) {
	route := trafficpolicy.RoutePolicy{
		ID:       "route-split-geoip-cn",
		Priority: 10,
		Action:   "route",
		Match: trafficpolicy.Match{
			Sources:      []string{"0.0.0.0/0"},
			Destinations: []string{"1.0.1.0/24", "1.0.2.0/23"},
			Protocols:    []string{"any"},
		},
		Path: &trafficpolicy.WANPath{VPPInterface: "pppoe_session1", NextHop: "10.67.0.1"},
	}
	policyID := stableID("route-abf:"+route.ID, 10000, 8999)
	tableID := stableID("route-table:"+route.ID, 50000, 49999)
	request := SnapshotRequest{
		LANVPPInterface:   "lyroute-ens34",
		LocalDestinations: []string{"192.168.50.0/24"},
		RoutePolicies:     []string{route.ID},
		Candidates:        SnapshotCandidates{RoutePolicies: []trafficpolicy.RoutePolicy{route}},
	}
	commands := routeSnapshotCommands(request)
	if !slices.Contains(commands, "show ly-route pre-nat-route") {
		t.Fatalf("snapshot commands = %#v, want radix inventory", commands)
	}
	readback, err := decodeVPPCTLRoutes(request, []VPPCTLCommandResult{
		{Command: "show acl-plugin acl", Stdout: ""},
		{Command: "show ly-route pre-nat-route", Stdout: fmt.Sprintf("enabled 1 interface lyroute-ens34 lan-prefix 192.168.50.0/24 rules 2 radix-nodes 3\nrule id %d priority 10 prefixes 2 table %d fib-index 12 skip-nat 0 bypass 0\n", policyID, tableID)},
		{Command: fmt.Sprintf("show abf policy %d", policyID), Stdout: fmt.Sprintf("Invalid policy ID:%d\n", policyID)},
		{Command: fmt.Sprintf("show ip fib table %d", tableID), Stdout: fmt.Sprintf("ipv4-VRF:%d, fib_index:12, flow hash:[src dst sport dport proto]\n0.0.0.0/0\n  unicast-ip4-chain\n    [@0]: ipv4 via 0.0.0.0 pppoe_session1\n", tableID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readback.Policies, []trafficpolicy.RoutePolicy{route}) {
		t.Fatalf("routes = %#v, want radix route", readback.Policies)
	}
}

type fakeVPPResponse struct {
	stdout string
	stderr string
	retval int
}

func TestVPPCTLChannelExecutesShowCommandsAndRetainsResults(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("FAKE_VPPCTL_LOG", logPath)
	binary := writeFakeVPPCTL(t, map[string]fakeVPPResponse{
		"show first":  {stdout: "first output\n"},
		"show second": {stdout: "second output\n"},
	})
	channel, err := NewVPPCTLClient(binary).OpenChannel(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// When
	reply, err := channel.Do(context.Background(), Operation{Name: "proof", VPPCtlCommands: []string{"show first", "show second"}})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok || len(payload.CommandResults) != 2 || payload.CommandResults[0].Stdout != "first output\n" || payload.CommandResults[1].Retval != 0 {
		t.Fatalf("payload = %#v", reply.Payload)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(logData)); !reflect.DeepEqual(got, []string{"show", "first", "show", "second"}) {
		t.Fatalf("commands = %q", logData)
	}
}

func TestVPPCTLChannelFailedCommandRetainsResult(t *testing.T) {
	// Given
	binary := writeFakeVPPCTL(t, map[string]fakeVPPResponse{
		"show broken": {stdout: "partial output\n", stderr: "socket read failed\n", retval: 7},
	})
	channel, err := NewVPPCTLClient(binary).OpenChannel(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// When
	reply, err := channel.Do(context.Background(), Operation{Name: "proof", VPPCtlCommands: []string{"show broken"}})

	// Then
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if err == nil || !ok || reply.Retval != 7 || len(payload.CommandResults) != 1 {
		t.Fatalf("reply = %#v, error = %v", reply, err)
	}
	result := payload.CommandResults[0]
	if result.Stdout != "partial output\n" || result.Stderr != "socket read failed\n" || result.Retval != 7 {
		t.Fatalf("command result = %#v", result)
	}
}

func TestVPPCTLChannelOptionalFailureContinuesToSemanticReadback(t *testing.T) {
	binary := writeFakeVPPCTL(t, map[string]fakeVPPResponse{
		"create interface af_xdp host-if eth1 name lyroute-eth1 zero-copy": {stderr: "interface already exists\n", retval: 1},
		"show interface lyroute-eth1":                                      {stdout: "lyroute-eth1 up\n"},
	})
	channel, err := NewVPPCTLClient(binary).OpenChannel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := channel.Do(context.Background(), Operation{Name: "proof", VPPCtlCommands: []string{
		"?create interface af_xdp host-if eth1 name lyroute-eth1 zero-copy",
		"show interface lyroute-eth1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok || len(payload.CommandResults) != 2 || payload.CommandResults[0].Retval != 1 || payload.CommandResults[1].Retval != 0 || !strings.Contains(payload.CommandResults[1].Stdout, "lyroute-eth1") {
		t.Fatalf("optional failure results = %#v", reply.Payload)
	}
}

func TestVPPCTLChannelDecodeFailureRetainsResult(t *testing.T) {
	// Given
	binary := writeFakeVPPCTL(t, map[string]fakeVPPResponse{
		"show interface address": {stdout: "unknown interface grammar\n"},
	})
	channel, err := NewVPPCTLClient(binary).OpenChannel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := SnapshotRequest{TransactionID: "txn-decode-failure", Interfaces: []string{"lyroute-eth1"}, Candidates: SnapshotCandidates{Interfaces: []InterfaceState{{Name: "lyroute-eth1"}}}}

	// When
	reply, err := channel.Do(context.Background(), snapshotOperation(request, SnapshotCapabilityInterfaces))

	// Then
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !errors.Is(err, ErrSnapshotIncomplete) || !ok || len(payload.CommandResults) != 1 || payload.CommandResults[0].Stdout != "unknown interface grammar\n" {
		t.Fatalf("reply = %#v, error = %v", reply, err)
	}
}

func TestVPPCTLSnapshotAllowsMissingManagedInterfaceAfterVerifiedRestart(t *testing.T) {
	request := SnapshotRequest{
		TransactionID: "txn-cold-start",
		AllowMissing:  true,
		Interfaces:    []string{"lyroute-ens192", "lyroute-ens224"},
		Candidates: SnapshotCandidates{Interfaces: []InterfaceState{
			{Name: "lyroute-ens192"},
			{Name: "lyroute-ens224"},
		}},
		Capabilities: []SnapshotCapability{SnapshotCapabilityInterfaces},
	}
	responses := map[string]fakeVPPResponse{
		"show interface address": {stdout: "local0 (down):\n"},
	}
	snapshot, err := (Adapter{Client: NewVPPCTLClient(writeFakeVPPCTL(t, responses))}).Snapshot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Interfaces) != 0 {
		t.Fatalf("interfaces = %#v, want no managed interfaces", snapshot.Interfaces)
	}
}

func TestVPPCTLSnapshotDecodesAllGatewayResourceClasses(t *testing.T) {
	// Given
	interfaceCandidate := InterfaceState{Name: "lyroute-eth1"}
	bondCandidate := BondState{Name: "BondEthernet0", Mode: "active-backup"}
	logicalBondCandidate := BondState{Name: "bond-lan", Mode: "active-backup"}
	logicalBondID, _ := vppBondIdentity(logicalBondCandidate.Name)
	wanCandidate := trafficpolicy.WANGroup{ID: "wan-primary", Members: []string{"wan0", "wan1"}, Weights: map[string]int{"wan0": 2, "wan1": 1}}
	routeCandidate := trafficpolicy.RoutePolicy{ID: "route-office", Priority: 100, Action: "route", Match: trafficpolicy.Match{Sources: []string{"10.0.0.0/24"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"tcp"}, SourcePorts: []string{"any"}, DestPorts: []string{"443"}}, Egress: wanCandidate.ID}
	aclCandidate := trafficpolicy.SecurityACL{ID: "acl-guest", Priority: 50, Action: "deny", Match: trafficpolicy.Match{Sources: []string{"192.168.88.0/24"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"tcp"}, SourcePorts: []string{"any"}, DestPorts: []string{"443"}, Direction: "input"}}
	qosCandidate := flow.VPPObjectGroup{Kind: "vpp.qos.egress-map", Objects: []flow.VPPObject{{RuleID: "voice", Granularity: flow.RuleGranularity, Action: flow.ActionRemark, Class: "voice", DSCP: "46"}}}
	staticCandidate := nat.StaticMapping{ID: "static-main", ExternalAddress: "203.0.113.10", InternalAddress: "192.168.88.10", WANInterface: "wan0"}
	portCandidate := nat.PortMapping{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 8443, WANInterface: "wan0", Hairpin: true}
	routePolicyID := stableID("route-abf:"+routeCandidate.ID, 10000, 8999)
	routeACLID := stableID("route-acl:"+routeCandidate.ID, 10000, 49999)
	routeTableID := stableID("route-table:"+routeCandidate.ID, 50000, 49999)
	wanTableID := wanGroupTableID(wanCandidate.ID)
	aclID := stableID("security-acl:"+aclCandidate.ID, 50000, 49999)
	qosMapID := stableID("qos-map:voice", 1, 999)
	qosValue := qosClassValue(flow.Target{RuleID: "voice", Class: "voice"})
	tests := []struct {
		name       string
		capability SnapshotCapability
		request    SnapshotRequest
		responses  map[string]fakeVPPResponse
		assert     func(*testing.T, Snapshot)
	}{
		{
			name: "interfaces with whitespace variation", capability: SnapshotCapabilityInterfaces,
			request:   SnapshotRequest{Interfaces: []string{interfaceCandidate.Name}, Candidates: SnapshotCandidates{Interfaces: []InterfaceState{interfaceCandidate}}},
			responses: map[string]fakeVPPResponse{"show interface address": {stdout: "  lyroute-eth1   (up):  \n    L3   192.0.2.1/24  \neth0 (up):\n  L3 198.51.100.1/24\n"}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if !reflect.DeepEqual(snapshot.Interfaces, []InterfaceState{{Name: "lyroute-eth1", AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.1/24"}}}) {
					t.Fatalf("interfaces = %#v", snapshot.Interfaces)
				}
			},
		},
		{
			name: "interface address with non-default FIB metadata", capability: SnapshotCapabilityInterfaces,
			request:   SnapshotRequest{Interfaces: []string{interfaceCandidate.Name}, Candidates: SnapshotCandidates{Interfaces: []InterfaceState{interfaceCandidate}}},
			responses: map[string]fakeVPPResponse{"show interface address": {stdout: "lyroute-eth1 (up):\n  L3 198.18.17.217/30 ip4 table-id 30756 fib-idx 1\n"}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if !reflect.DeepEqual(snapshot.Interfaces, []InterfaceState{{Name: "lyroute-eth1", AdminState: "up", LinkState: "up", Addresses: []string{"198.18.17.217/30"}}}) {
					t.Fatalf("interfaces = %#v", snapshot.Interfaces)
				}
			},
		},
		{
			name: "bonds", capability: SnapshotCapabilityBonds,
			request:   SnapshotRequest{Bonds: []string{bondCandidate.Name}, Candidates: SnapshotCandidates{Bonds: []BondState{bondCandidate}}},
			responses: map[string]fakeVPPResponse{"show bond details": {stdout: "BondEthernet0\n  mode: active-backup\n  load balance: active-backup\n  number of active members: 2\n    lyroute-eth1\n      weight: 1, is_local_numa: 1, sw_if_index: 1\n    lyroute-eth2\n      weight: 1, is_local_numa: 1, sw_if_index: 2\n  number of members: 2\n    lyroute-eth1\n    lyroute-eth2\n  device instance: 0\n  interface id: 0\n  sw_if_index: 5\n  hw_if_index: 5\n"}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if len(snapshot.Bonds) != 1 || !reflect.DeepEqual(snapshot.Bonds[0].Members, []string{"lyroute-eth1", "lyroute-eth2"}) {
					t.Fatalf("bonds = %#v", snapshot.Bonds)
				}
			},
		},
		{
			name: "logical bond names map from stable VPP interface id", capability: SnapshotCapabilityBonds,
			request:   SnapshotRequest{Bonds: []string{logicalBondCandidate.Name}, Candidates: SnapshotCandidates{Bonds: []BondState{logicalBondCandidate}}},
			responses: map[string]fakeVPPResponse{"show bond details": {stdout: fmt.Sprintf("BondEthernet%d\n  mode: active-backup\n  load balance: active-backup\n  number of active members: 1\n    lyroute-eth1\n      weight: 1, is_local_numa: 1, sw_if_index: 1\n  number of members: 1\n    lyroute-eth1\n  device instance: 0\n  interface id: %d\n  sw_if_index: 5\n  hw_if_index: 5\n", logicalBondID, logicalBondID)}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if len(snapshot.Bonds) != 1 || snapshot.Bonds[0].Name != logicalBondCandidate.Name {
					t.Fatalf("logical bonds = %#v", snapshot.Bonds)
				}
			},
		},
		{
			name: "routes", capability: SnapshotCapabilityRoutePolicies,
			request: SnapshotRequest{RoutePolicies: []string{routeCandidate.ID}, Candidates: SnapshotCandidates{RoutePolicies: []trafficpolicy.RoutePolicy{routeCandidate}, WANGroups: []trafficpolicy.WANGroup{wanCandidate}}},
			responses: map[string]fakeVPPResponse{
				"show acl-plugin acl":                             {stdout: fmt.Sprintf("acl-index %d count 1 tag {ly-route-%s}\n  0: ipv4 permit src 10.0.0.0/24 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 443-443\n", routeACLID, safeTag(routeCandidate.ID))},
				fmt.Sprintf("show abf policy %d", routePolicyID):  {stdout: fmt.Sprintf("abf:[0]: policy:%d acl:%d\n path-list:[17] locks:1 flags:shared len:1\n  path:[21] pl-index:17 ip4 weight=1 pref=0\n    [@0]: ipv4 via table %d\n", routePolicyID, routeACLID, wanTableID)},
				fmt.Sprintf("show ip fib table %d", routeTableID): {stdout: fmt.Sprintf("ipv4-VRF:%d, fib_index:3, flow hash:[src dst sport dport proto]\n0.0.0.0/0\n  unicast-ip4-chain\n    [@0]: dpo-load-balance: [proto:ip4 index:8 buckets:1 uRPF:7 to:[0:0]]\n      path-list:[17] locks:1 flags:shared len:1\n        path:[21] pl-index:17 ip4 weight=1 pref=0\n          [@0]: ipv4 via table %d\n", routeTableID, wanTableID)},
			},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if !reflect.DeepEqual(snapshot.RoutePolicies, []trafficpolicy.RoutePolicy{routeCandidate}) {
					t.Fatalf("routes = %#v", snapshot.RoutePolicies)
				}
			},
		},
		{
			name: "wan groups", capability: SnapshotCapabilityWANGroups,
			request:   SnapshotRequest{WANGroups: []string{wanCandidate.ID}, Candidates: SnapshotCandidates{WANGroups: []trafficpolicy.WANGroup{wanCandidate}}},
			responses: map[string]fakeVPPResponse{fmt.Sprintf("show ip fib table %d", wanTableID): {stdout: fmt.Sprintf("ipv4-VRF:%d, fib_index:4, flow hash:[src dst sport dport proto]\n0.0.0.0/0\n  unicast-ip4-chain\n    [@0]: dpo-load-balance: [proto:ip4 index:9 buckets:2 uRPF:8 to:[0:0]]\n      path-list:[18] locks:1 flags:shared len:2\n        path:[22] pl-index:18 ip4 weight=2 pref=0\n          [@0]: ipv4 via wan0\n        path:[23] pl-index:18 ip4 weight=1 pref=0\n          [@1]: ipv4 via wan1\n", wanTableID)}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if !reflect.DeepEqual(snapshot.WANGroups, []trafficpolicy.WANGroup{wanCandidate}) {
					t.Fatalf("WAN groups = %#v", snapshot.WANGroups)
				}
			},
		},
		{
			name: "acls", capability: SnapshotCapabilityACLs,
			request:   SnapshotRequest{ACLs: []string{aclCandidate.ID}, Candidates: SnapshotCandidates{ACLs: []trafficpolicy.SecurityACL{aclCandidate}}},
			responses: map[string]fakeVPPResponse{fmt.Sprintf("show acl-plugin acl index %d", aclID): {stdout: fmt.Sprintf("acl-index %d count 1 tag {ly-route-%s}\n  0: ipv4 deny src 192.168.88.0/24 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 443-443\n", aclID, safeTag(aclCandidate.ID))}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if !reflect.DeepEqual(snapshot.ACLs, []trafficpolicy.SecurityACL{aclCandidate}) {
					t.Fatalf("ACLs = %#v", snapshot.ACLs)
				}
			},
		},
		{
			name: "qos", capability: SnapshotCapabilityQoS,
			request:   SnapshotRequest{QoS: []string{qosCandidate.Kind}, Candidates: SnapshotCandidates{QoS: []flow.VPPObjectGroup{qosCandidate}}},
			responses: map[string]fakeVPPResponse{fmt.Sprintf("show qos egress map id %d", qosMapID): {stdout: qosMapFixture(qosMapID, qosValue, 46)}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if !reflect.DeepEqual(snapshot.QoS, []flow.VPPObjectGroup{qosCandidate}) {
					t.Fatalf("QoS = %#v", snapshot.QoS)
				}
			},
		},
		{
			name: "nat44 static mappings", capability: SnapshotCapabilityNAT44,
			request:   SnapshotRequest{NATStaticMappings: []string{staticCandidate.ID}, Candidates: SnapshotCandidates{NATStaticMappings: []nat.StaticMapping{staticCandidate}}},
			responses: map[string]fakeVPPResponse{"show nat44 static mappings": {stdout: "NAT44 static mappings:\n  local 192.168.88.10 external 203.0.113.10 vrf 0\n"}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if !reflect.DeepEqual(snapshot.NAT.StaticMappings, []nat.StaticMapping{staticCandidate}) {
					t.Fatalf("NAT44 = %#v", snapshot.NAT)
				}
			},
		},
		{
			name: "nat44 port maps", capability: SnapshotCapabilityNAT44,
			request:   SnapshotRequest{NATPortMappings: []string{portCandidate.ID}, Candidates: SnapshotCandidates{NATPortMappings: []nat.PortMapping{portCandidate}}},
			responses: map[string]fakeVPPResponse{"show nat44 static mappings": {stdout: "NAT44 static mappings:\n  TCP local 192.168.88.20:8443 external 203.0.113.10:8443 vrf 0\n"}},
			assert: func(t *testing.T, snapshot Snapshot) {
				t.Helper()
				if !reflect.DeepEqual(snapshot.NAT.PortMappings, []nat.PortMapping{portCandidate}) {
					t.Fatalf("port maps = %#v", snapshot.NAT)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := writeFakeVPPCTL(t, test.responses)
			test.request.TransactionID = "txn-vppctl"
			test.request.Capabilities = []SnapshotCapability{test.capability}
			snapshot, err := (Adapter{Client: NewVPPCTLClient(binary)}).Snapshot(context.Background(), test.request)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.TransactionID != "txn-vppctl" {
				t.Fatalf("identity = %#v", snapshot)
			}
			test.assert(t, snapshot)
		})
	}
}

func TestVPPCTLSnapshotFailsClosed(t *testing.T) {
	// Given
	aclID := stableID("security-acl:acl-guest", 50000, 49999)
	tests := []struct {
		name       string
		capability SnapshotCapability
		request    SnapshotRequest
		responses  map[string]fakeVPPResponse
	}{
		{name: "duplicate rows", capability: SnapshotCapabilityInterfaces, request: SnapshotRequest{Interfaces: []string{"lyroute-eth1"}, Candidates: SnapshotCandidates{Interfaces: []InterfaceState{{Name: "lyroute-eth1"}}}}, responses: map[string]fakeVPPResponse{"show interface address": {stdout: "lyroute-eth1 (up):\n  L3 192.0.2.1/24\nlyroute-eth1 (up):\n  L3 192.0.2.2/24\n"}}},
		{name: "missing candidate", capability: SnapshotCapabilityACLs, request: SnapshotRequest{ACLs: []string{"acl-guest"}}, responses: map[string]fakeVPPResponse{fmt.Sprintf("show acl-plugin acl index %d", aclID): {stdout: fmt.Sprintf("acl-index %d count 1 tag {ly-route-acl_guest}\n  0: ipv4 deny src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n", aclID)}}},
		{name: "failed command stderr", capability: SnapshotCapabilityInterfaces, request: SnapshotRequest{Interfaces: []string{"lyroute-eth1"}, Candidates: SnapshotCandidates{Interfaces: []InterfaceState{{Name: "lyroute-eth1"}}}}, responses: map[string]fakeVPPResponse{"show interface address": {stderr: "socket read failed\n", retval: 7}}},
		{name: "truncated bond output", capability: SnapshotCapabilityBonds, request: SnapshotRequest{Bonds: []string{"BondEthernet0"}, Candidates: SnapshotCandidates{Bonds: []BondState{{Name: "BondEthernet0", Mode: "active-backup"}}}}, responses: map[string]fakeVPPResponse{"show bond details": {stdout: "BondEthernet0\n  mode: active-backup\n  number of members: 2\n    lyroute-eth1\n"}}},
		{name: "malformed port", capability: SnapshotCapabilityNAT44, request: SnapshotRequest{NATPortMappings: []string{"web"}, Candidates: SnapshotCandidates{NATPortMappings: []nat.PortMapping{{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 443}}}}, responses: map[string]fakeVPPResponse{"show nat44 static mappings": {stdout: "NAT44 static mappings:\n  tcp local 192.168.88.20:nope external 203.0.113.10:8443 vrf 0\n"}}},
		{name: "unknown grammar", capability: SnapshotCapabilityInterfaces, request: SnapshotRequest{Interfaces: []string{"lyroute-eth1"}, Candidates: SnapshotCandidates{Interfaces: []InterfaceState{{Name: "lyroute-eth1"}}}}, responses: map[string]fakeVPPResponse{"show interface address": {stdout: "interface maybe present\n"}}},
		{name: "unknown interface address metadata", capability: SnapshotCapabilityInterfaces, request: SnapshotRequest{Interfaces: []string{"lyroute-eth1"}, Candidates: SnapshotCandidates{Interfaces: []InterfaceState{{Name: "lyroute-eth1"}}}}, responses: map[string]fakeVPPResponse{"show interface address": {stdout: "lyroute-eth1 (up):\n  L3 192.0.2.1/24 unexpected metadata\n"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.TransactionID = "txn-fail-closed"
			test.request.Capabilities = []SnapshotCapability{test.capability}
			_, err := (Adapter{Client: NewVPPCTLClient(writeFakeVPPCTL(t, test.responses))}).Snapshot(context.Background(), test.request)
			if !errors.Is(err, ErrSnapshotIncomplete) {
				t.Fatalf("error = %T %v, want ErrSnapshotIncomplete", err, err)
			}
		})
	}
}

func writeFakeVPPCTL(t *testing.T, responses map[string]fakeVPPResponse) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vppctl")
	var script strings.Builder
	script.WriteString("#!/bin/sh\nif [ -n \"$FAKE_VPPCTL_LOG\" ]; then printf '%s\\n' \"$*\" >> \"$FAKE_VPPCTL_LOG\"; fi\ncase \"$*\" in\n")
	for command, response := range responses {
		fmt.Fprintf(&script, "  %q)\n", command)
		if response.stdout != "" {
			script.WriteString("    cat <<'VPP_STDOUT'\n")
			script.WriteString(response.stdout)
			script.WriteString("VPP_STDOUT\n")
		}
		if response.stderr != "" {
			script.WriteString("    cat >&2 <<'VPP_STDERR'\n")
			script.WriteString(response.stderr)
			script.WriteString("VPP_STDERR\n")
		}
		fmt.Fprintf(&script, "    exit %d;;\n", response.retval)
	}
	script.WriteString("  *) printf 'unsupported command: %s\\n' \"$*\" >&2; exit 64;;\nesac\n")
	if err := os.WriteFile(path, []byte(script.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func qosMapFixture(mapID, input, output int) string {
	rows := make(map[string][]string, 4)
	for _, source := range []string{"ext", "VLAN", "MPLS", "IP"} {
		rows[source] = make([]string, 256)
		for index := range rows[source] {
			rows[source][index] = "0"
		}
	}
	rows["IP"][input] = strconv.Itoa(output)
	var fixture strings.Builder
	fmt.Fprintf(&fixture, " Map-ID:%d\n", mapID)
	for _, source := range []string{"ext", "VLAN", "MPLS", "IP"} {
		fmt.Fprintf(&fixture, "  %s:[%s]\n", source, strings.Join(rows[source], ","))
	}
	return fixture.String()
}
