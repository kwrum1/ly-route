package vpp

import (
	"os"
	"strings"
	"testing"

	"ly-route/backend/internal/geodata"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestGeoIPRoutePolicyUsesBatchedFIBChain(t *testing.T) {
	data, err := geodata.LoadSource(geodata.Source{
		Format:   geodata.FormatGeoIP,
		Category: "CN",
		File:     "geoip.dat",
		SHA256:   "6ba63d75f307d16a81ae09406ddcf2779fa75cb642d4aae59613370d62d33509",
	})
	if err != nil {
		t.Skipf("routing geodata is not installed: %v", err)
	}
	policies := []trafficpolicy.RoutePolicy{
		{ID: "geoip-cn-probe", Priority: 10, Action: "route", Egress: "pppoe", Path: &trafficpolicy.WANPath{VPPInterface: "lyppp-test"}, Match: trafficpolicy.Match{Destinations: data.Entries}},
		{ID: "geosite-cn-probe", Priority: 20, Action: "route", Egress: "pppoe", Path: &trafficpolicy.WANPath{VPPInterface: "lyppp-test"}, Match: trafficpolicy.Match{Destinations: []string{"203.0.113.0/24"}}},
		{ID: "proxy-default-probe", Priority: 100, Action: "route", Egress: "proxy", Path: &trafficpolicy.WANPath{VPPInterface: "lypxinc0", NextHop: "198.18.16.2"}, Match: trafficpolicy.Match{Destinations: []string{"0.0.0.0/0"}}},
	}
	ordered := orderedRoutePoliciesForVPP(policies, nil)
	options := buildRoutePolicyCommandOptions(ordered, nil)
	if len(options) != len(policies) {
		t.Fatalf("route chain was not selected: %#v", options)
	}
	if ordered[0].ID != "proxy-default-probe" || ordered[len(ordered)-1].ID != "geoip-cn-probe" {
		t.Fatalf("route operations were not ordered from terminal to highest priority: %#v", ordered)
	}
	commands := routePolicyCommandsWithOptions(policies[0], nil, options[policies[0].ID])
	if len(commands) == 0 || !strings.Contains(commands[0], "dst 0.0.0.0/0") {
		t.Fatalf("optimized ACL command = %#v", commands[:minInt(len(commands), 2)])
	}
	joined := strings.Join(commands, "\n")
	if strings.Count(commands[0], " dst ") != 1 {
		t.Fatalf("optimized route unexpectedly expanded the ACL: %s", commands[0])
	}
	routeCommands := 0
	for _, command := range commands {
		if strings.HasPrefix(strings.TrimPrefix(command, "?"), "ip route add table") {
			routeCommands++
		}
		if len(command) > 4096 {
			t.Fatalf("optimized VPP command is too long: %d bytes", len(command))
		}
	}
	v4Count := len(routePolicyIPv4Destinations(data.Entries))
	if routeCommands != v4Count+1 {
		t.Fatalf("optimized route command count = %d, want %d IPv4 prefixes plus default", routeCommands, v4Count+1)
	}
	if !strings.Contains(joined, vppRouteBatchBegin) || !strings.Contains(joined, vppRouteBatchEnd) || !strings.Contains(joined, "ip4-lookup-in-table ") {
		t.Fatalf("optimized route chain markers/default are missing: %s", joined[:minInt(len(joined), 500)])
	}
	if path := strings.TrimSpace(os.Getenv("LY_ROUTE_WRITE_VPP_PROBE")); path != "" {
		lines := []string{"ip table add 55549"}
		for _, prefix := range routePolicyIPv4Destinations(data.Entries) {
			lines = append(lines, "ip route add table 55549 "+prefix+" via ip4-lookup-in-table 0")
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
