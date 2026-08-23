package vpp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestResolveRuntimePPPoERoutePolicyUsesCurrentSession(t *testing.T) {
	statusDir := t.TempDir()
	t.Setenv("LY_ROUTE_PPPOE_RUNTIME_STATUS_DIR", statusDir)
	if err := os.WriteFile(filepath.Join(statusDir, "wan-primary.json"), []byte(`{"interface":"pppoe_session1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := trafficpolicy.RoutePolicy{
		ID: "route-60", Path: &trafficpolicy.WANPath{VPPInterface: "pppoe-runtime:wan-primary", NextHop: "10.67.0.1"},
	}
	resolved, err := resolveRuntimePPPoERoutePolicy(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Path == nil || resolved.Path.VPPInterface != "pppoe_session1" || candidate.Path.VPPInterface != "pppoe-runtime:wan-primary" {
		t.Fatalf("resolved=%#v original=%#v", resolved.Path, candidate.Path)
	}
}

func TestBuildRouteWANGroupOperationsDeletesChainedPoliciesSafely(t *testing.T) {
	routes := []trafficpolicy.RoutePolicy{
		{ID: "route-10", Priority: 10, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Destinations: []string{"10.0.0.0/8"}}},
		{ID: "route-20", Priority: 20, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Destinations: []string{"192.168.0.0/16"}}},
		{ID: "route-100", Priority: 100, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/0"}}},
	}
	plan, err := BuildRouteWANGroupOperations(RouteWANGroupPlan{
		TransactionID:    "txn-route-order",
		Routes:           nil,
		DeleteRoutes:     []string{"route-10", "route-100", "route-20"},
		DeleteRouteState: routes,
	})
	if err != nil {
		t.Fatalf("build operations: %v", err)
	}
	got := make([]string, 0, len(plan))
	for _, operation := range plan {
		if operation.Name == "vpp.route-policy" {
			got = append(got, operation.Resource)
		}
	}
	want := []string{"route-10", "route-20", "route-100"}
	if len(got) != len(want) {
		t.Fatalf("delete operation count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delete order = %v, want %v", got, want)
		}
	}
}

func TestRoutePolicyABFDeleteViaUsesPrivateTableWhenVPPOmitsNextHop(t *testing.T) {
	spec := routePolicyVPPCTLSpec{tableID: 93153, via: "0.0.0.0 pppoe_session0"}
	output := "abf:[4]: policy:16469 acl:4\n" +
		"     path-list:[91] locks:1 flags:shared,no-uRPF, uRPF-list: None\n" +
		"      path:[100] pl-index:91 ip4 weight=1 pref=0 deag:  oper-flags:resolved,\n" +
		"         fib-index:6\n"
	fibSummary := "ipv4-VRF:92258, fib_index:5, flow hash:[src dst]\n" +
		"ipv4-VRF:93153, fib_index:6, flow hash:[src dst]\n"
	if got, want := routePolicyABFDeleteVia(spec, output, fibSummary), "ip4-lookup-in-table 93153"; got != want {
		t.Fatalf("delete via = %q, want %q", got, want)
	}
}

func TestRoutePolicyABFDeleteViaFailsClosedWhenLiveFibMappingIsMissing(t *testing.T) {
	spec := routePolicyVPPCTLSpec{tableID: 93153, via: "ip4-lookup-in-table 93153"}
	output := "abf:[4]: policy:16469 acl:4\n" +
		"     path-list:[91] locks:1 flags:shared,no-uRPF, uRPF-list: None\n" +
		"      path:[100] pl-index:91 ip4 weight=1 pref=0 deag: oper-flags:resolved,\n" +
		"         fib-index:6\n"
	if got := routePolicyABFDeleteVia(spec, output, "ipv4-VRF:92258, fib_index:5, flow hash:[src dst]\n"); got != "" {
		t.Fatalf("delete via = %q, want fail-closed empty path", got)
	}
}

func TestObservedABFPathCountRejectsDuplicateReplayPaths(t *testing.T) {
	output := "abf:[4]: policy:16469 acl:4\n" +
		" path-list:[91] locks:1 flags:shared,no-uRPF\n" +
		"  path:[100] pl-index:91 ip4 weight=1 pref=0\n" +
		"  path:[101] pl-index:91 ip4 weight=1 pref=0\n"
	if got := observedABFPathCount(output); got != 2 {
		t.Fatalf("ABF path count = %d, want 2", got)
	}
}

func TestBuildRouteWANGroupOperationsPreservesLocalLANForIncrementalCatchAll(t *testing.T) {
	route := trafficpolicy.RoutePolicy{
		ID:       "route-default",
		Priority: 100,
		Action:   "nat",
		Egress:   "wan-pppoe",
		Match: trafficpolicy.Match{
			Sources:      []string{"192.168.50.100/32"},
			Destinations: []string{"0.0.0.0/0"},
		},
	}
	operations, err := BuildRouteWANGroupOperations(RouteWANGroupPlan{
		TransactionID:       "txn-local-bypass",
		Routes:              []trafficpolicy.RoutePolicy{route},
		RoutePolicyContext:  []trafficpolicy.RoutePolicy{route},
		LocalDestinations:   []string{"192.168.50.0/24"},
		IngressVPPInterface: "lyroute-ens34",
	})
	if err != nil {
		t.Fatalf("build operations: %v", err)
	}
	if len(operations) != 1 {
		t.Fatalf("operation count = %d, want 1", len(operations))
	}
	joined := strings.Join(operations[0].VPPCtlCommands, "\n")
	if !strings.Contains(joined, "deny src 192.168.50.100/32 dst 192.168.50.0/24") {
		t.Fatalf("incremental catch-all route stole local LAN traffic: %s", joined)
	}
}

func TestVerifyABFPolicyAcceptsVPP25PointToPointPathDetails(t *testing.T) {
	const (
		policyID = 16469
		aclID    = 7
	)
	output := "abf:[3]: policy:16469 acl:7\n" +
		"     path-list:[78] locks:1 flags:shared,no-uRPF, uRPF-list: None\n" +
		"      path:[91] pl-index:78 ip4 weight=1 pref=0 attached-nexthop: oper-flags:resolved,\n" +
		"        10.67.0.1 pppoe_session0 (p2p)\n" +
		"      [@0]: ipv4 [features] via 0.0.0.0 pppoe_session0: mtu:9000 next:6 flags:[features ]\n" +
		"             stacked-on:\n" +
		"               [@2]: lyroute-ens33-tx-dpo:\n"
	results := []VPPCTLCommandResult{{Command: "show abf policy 16469", Stdout: output}}
	if err := verifyABFPolicy(results, abfCandidateProof{policyID: policyID, aclID: aclID, via: "10.67.0.1 pppoe_session0"}); err != nil {
		t.Fatalf("verify point-to-point ABF readback: %v", err)
	}
}

func TestParseRoutePolicyDeleteSpecUsesExplicitIngressInterface(t *testing.T) {
	operation := Operation{
		Name:     "vpp.route-policy.rollback-delete",
		Resource: "route-10",
		Payload:  trafficpolicy.RoutePolicy{ID: "route-10", Action: "route"},
		VPPCtlCommands: []string{
			"?abf attach ip4 del policy 16469 lyroute-ens34",
			"?abf policy del id 16469",
		},
	}
	spec, err := parseRoutePolicyVPPCTLSpec(operation)
	if err != nil {
		t.Fatalf("parse delete spec: %v", err)
	}
	if spec.ingress != "lyroute-ens34" {
		t.Fatalf("ingress = %q, want lyroute-ens34", spec.ingress)
	}
}

func TestFibPathMatchesResolvedPointToPointNextHop(t *testing.T) {
	if !fibPathMatchesExpected("0.0.0.0 pppoe_session0: mtu:9000 next:6 flags:[features]", "10.67.0.1 pppoe_session0") {
		t.Fatal("resolved PPPoE FIB path should match the configured point-to-point interface")
	}
	if !fibPathMatchesExpected("0.0.0.0 pppoe_session0: mtu:9000 next:6 flags:[features]", "pppoe_session0") {
		t.Fatal("resolved PPPoE FIB path should match an interface-only runtime target")
	}
	if fibPathMatchesExpected("0.0.0.0 lyroute-ens33", "10.67.0.1 pppoe_session0") {
		t.Fatal("FIB path with a different interface must not match")
	}
}

func TestFibPathsResolveWeightedWANGroup(t *testing.T) {
	group := trafficpolicy.WANGroup{
		ID: "dual", Mode: trafficpolicy.WANGroupWeighted,
		Members: []string{"primary", "backup"},
		Paths: map[string]trafficpolicy.WANPath{
			"primary": {VPPInterface: "pppoe_session1", NextHop: "10.67.0.1"},
			"backup":  {VPPInterface: "pppoe_session0", NextHop: "10.68.0.1"},
		},
	}
	paths := []fibPath{
		{via: "0.0.0.0 pppoe_session1: mtu:9000 next:6 flags:[]"},
		{via: "0.0.0.0 pppoe_session0: mtu:9000 next:6 flags:[]"},
	}
	if !fibPathsResolveWANGroup(paths, group) {
		t.Fatal("recursive FIB member paths should match the weighted WAN group")
	}
	if fibPathsResolveWANGroup(paths[:1], group) {
		t.Fatal("incomplete weighted WAN group member paths were accepted")
	}
}

func TestBuildRoutePolicyCommandOptionsAcceptsObservedDomainSentinelChain(t *testing.T) {
	policies := []trafficpolicy.RoutePolicy{
		{ID: "route-10", Priority: 10, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Sources: []string{"0.0.0.0/0"}, Destinations: []string{"1.1.8.0/24", "1.116.0.0/15"}, Protocols: []string{"any"}}},
		{ID: "route-20", Priority: 20, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Sources: []string{"0.0.0.0/0"}, Destinations: []string{"0.0.0.0/32"}, Protocols: []string{"any"}}},
		{ID: "route-100", Priority: 100, Action: "route", Egress: "proxy", Match: trafficpolicy.Match{Sources: []string{"0.0.0.0/0"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}}},
	}
	got := buildRoutePolicyCommandOptions(orderedRoutePoliciesForVPP(policies, nil), nil)
	if len(got) != len(policies) {
		t.Fatalf("options = %#v, want one option per route", got)
	}
}

func TestBuildRoutePolicyCommandOptionsOptimizesDestinationOnlySuffix(t *testing.T) {
	destinations := make([]string, maxRoutePolicyACLIPv4Prefixes+1)
	for index := range destinations {
		destinations[index] = fmt.Sprintf("10.%d.%d.0/24", index/256, index%256)
	}
	policies := []trafficpolicy.RoutePolicy{
		{ID: "client-exception", Priority: 10, Action: "nat", Egress: "wan", Match: trafficpolicy.Match{Sources: []string{"192.168.50.100/32"}, Destinations: []string{"0.0.0.0/0"}}},
		{ID: "geoip-cn", Priority: 200, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Destinations: destinations}},
		{ID: "proxy-default", Priority: 300, Action: "route", Egress: "proxy", Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/0"}}},
	}

	options := buildRoutePolicyCommandOptions(policies, nil)
	if len(options) != 2 || !options["geoip-cn"].optimizedIPv4 || !options["proxy-default"].optimizedIPv4 {
		t.Fatalf("suffix options = %#v, want geoip and default policies", options)
	}
	if _, optimized := options["client-exception"]; optimized {
		t.Fatalf("source-specific exception was incorrectly moved into the FIB chain: %#v", options)
	}
	operations, err := BuildRouteWANGroupOperations(RouteWANGroupPlan{
		TransactionID:      "txn-mixed-fib-suffix",
		Routes:             []trafficpolicy.RoutePolicy{policies[1]},
		RoutePolicyContext: policies,
	})
	if err != nil {
		t.Fatalf("build mixed policy operations: %v", err)
	}
	joined := strings.Join(operations[0].VPPCtlCommands, "\n")
	if strings.Contains(joined, "dst 10.") || !strings.Contains(joined, vppRouteBatchBegin) || !strings.Contains(joined, "ip4-lookup-in-table") {
		t.Fatalf("large suffix did not use the native FIB chain: %s", joined[:min(len(joined), 500)])
	}
}

func TestBuildRoutePolicyCommandOptionsKeepsGeoIPChainAfterTrailingUIRule(t *testing.T) {
	destinations := make([]string, maxRoutePolicyACLIPv4Prefixes+1)
	for index := range destinations {
		destinations[index] = fmt.Sprintf("10.%d.%d.0/24", index/256, index%256)
	}
	policies := []trafficpolicy.RoutePolicy{
		{ID: "client-exception", Priority: 1, Action: "nat", Egress: "wan", Match: trafficpolicy.Match{Sources: []string{"192.168.50.100/32"}, Destinations: []string{"0.0.0.0/0"}}},
		{ID: "geoip-cn", Priority: 10, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Destinations: destinations}},
		{ID: "proxy-default", Priority: 100, Action: "route", Egress: "proxy", Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/0"}}},
		{ID: "ui-rule-after-default", Priority: 628, Action: "nat", Egress: "wan", Match: trafficpolicy.Match{Sources: []string{"192.168.50.0/24"}, Destinations: []string{"0.0.0.0/0"}}},
	}

	options := buildRoutePolicyCommandOptions(policies, nil)
	if len(options) != 2 || !options["geoip-cn"].optimizedIPv4 || !options["proxy-default"].optimizedIPv4 {
		t.Fatalf("options = %#v, want preserved GeoIP/default chain", options)
	}
	if err := validateLargeRoutePolicyFallback(policies, options, nil); err != nil {
		t.Fatalf("large GeoIP route should keep its native chain: %v", err)
	}
}

func TestLargeGeoIPUsesNativeRadixWhenUIRuleInterruptsFIBChain(t *testing.T) {
	destinations := make([]string, maxRoutePolicyACLIPv4Prefixes+1)
	for index := range destinations {
		destinations[index] = fmt.Sprintf("10.%d.%d.0/24", index/256, index%256)
	}
	geoIP := trafficpolicy.RoutePolicy{
		ID:       "geoip-cn",
		Priority: 10,
		Action:   "route",
		Egress:   "wan",
		Path:     &trafficpolicy.WANPath{VPPInterface: "pppoe_session0", NextHop: "10.67.0.1"},
		Match:    trafficpolicy.Match{Destinations: destinations},
	}
	policies := []trafficpolicy.RoutePolicy{
		geoIP,
		{ID: "ui-source-rule", Priority: 50, Action: "route", Egress: "wan", Path: &trafficpolicy.WANPath{VPPInterface: "pppoe_session0", NextHop: "10.67.0.1"}, Match: trafficpolicy.Match{Sources: []string{"192.168.50.101/32"}, Destinations: []string{"198.51.100.0/24"}}},
		{ID: "proxy-default", Priority: 100, Action: "route", Egress: "proxy", Path: &trafficpolicy.WANPath{VPPInterface: "lypxin0", NextHop: "198.18.16.2"}, Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/0"}}},
	}

	options := buildRoutePolicyCommandOptions(policies, nil)
	if len(options) != 0 {
		t.Fatalf("ordinary rule must prevent only the FIB-chain optimization, got %#v", options)
	}
	addRoutePolicyLocalDestinationPrefixes(options, policies, []string{"192.168.50.0/24"})
	if err := validateLargeRoutePolicyFallback(policies, options, nil); err != nil {
		t.Fatalf("large GeoIP rule should use its independent native Radix path: %v", err)
	}
	commands := strings.Join(routePolicyCommandsWithOptions(geoIP, nil, options[geoIP.ID]), "\n")
	if !strings.Contains(commands, "set ly-route pre-nat-route add") || !strings.Contains(commands, vppRouteBatchBegin) {
		t.Fatalf("large GeoIP policy did not use the native Radix path: %s", commands[:min(len(commands), 500)])
	}
	if strings.Contains(commands, "acl-plugin") || strings.Contains(commands, "abf policy") {
		t.Fatalf("large GeoIP policy fell back to ACL/ABF: %s", commands[:min(len(commands), 500)])
	}
}

func TestIncrementalLargeRouteApplyUsesFullFIBChainContext(t *testing.T) {
	destinations := make([]string, 257)
	for i := range destinations {
		destinations[i] = fmt.Sprintf("10.%d.%d.0/24", i/256, i%256)
	}
	route10 := trafficpolicy.RoutePolicy{ID: "route-10", Priority: 10, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Destinations: destinations}}
	context := []trafficpolicy.RoutePolicy{
		route10,
		{ID: "route-20", Priority: 20, Action: "route", Egress: "wan", Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/32"}}},
		{ID: "route-100", Priority: 100, Action: "route", Egress: "proxy", Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/0"}}},
	}
	operations, err := BuildRouteWANGroupOperations(RouteWANGroupPlan{
		TransactionID:      "txn-incremental-fib",
		Routes:             []trafficpolicy.RoutePolicy{route10},
		RoutePolicyContext: context,
	})
	if err != nil {
		t.Fatalf("build incremental operations: %v", err)
	}
	if len(operations) != 1 {
		t.Fatalf("operation count = %d, want 1", len(operations))
	}
	joined := strings.Join(operations[0].VPPCtlCommands, "\n")
	if strings.Contains(joined, "dst 10.") {
		t.Fatalf("large destination set was serialized into ACL: %s", joined[:min(len(joined), 300)])
	}
	if !strings.Contains(joined, vppRouteBatchBegin) || !strings.Contains(joined, "ip4-lookup-in-table") {
		t.Fatalf("incremental route did not use the native FIB chain: %s", joined)
	}
}

func TestLargeRouteApplyWithoutFIBContextFailsClosed(t *testing.T) {
	destinations := make([]string, maxRoutePolicyACLIPv4Prefixes+1)
	for i := range destinations {
		destinations[i] = fmt.Sprintf("10.%d.%d.0/24", i/256, i%256)
	}
	_, err := BuildRouteWANGroupOperations(RouteWANGroupPlan{
		TransactionID: "txn-large-acl-guard",
		Routes:        []trafficpolicy.RoutePolicy{{ID: "large", Priority: 10, Action: "route", Match: trafficpolicy.Match{Destinations: destinations}}},
	})
	if err == nil || !strings.Contains(err.Error(), "refusing large ACL fallback") {
		t.Fatalf("error = %v, want large ACL fallback guard", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
