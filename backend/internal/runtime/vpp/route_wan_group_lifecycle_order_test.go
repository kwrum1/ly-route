package vpp

import (
	"fmt"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

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
