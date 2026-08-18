package vpp

import (
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestRoutePolicyAnySourceRemainsAny(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{
		ID:       "default-proxy",
		Priority: 100,
		Action:   "route",
		Egress:   "proxy-wan",
		Match: trafficpolicy.Match{
			Sources:      []string{"0.0.0.0/0"},
			Destinations: []string{"0.0.0.0/0"},
			Protocols:    []string{"any"},
			SourcePorts:  []string{"any"},
			DestPorts:    []string{"any"},
		},
	}
	commands := routePolicyCommandsWithOptions(policy, nil, routePolicyCommandOptions{optimizedIPv4: true})
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "src 0.0.0.0/0") {
		t.Fatalf("route policy any source was narrowed: %s", joined)
	}
}

func TestRoutePolicyCatchAllPreservesLocalLAN(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{
		ID:       "default-proxy",
		Priority: 100,
		Action:   "route",
		Egress:   "proxy-wan",
		Match: trafficpolicy.Match{
			Sources:      []string{"192.168.50.101/32"},
			Destinations: []string{"0.0.0.0/0"},
			Protocols:    []string{"any"},
			SourcePorts:  []string{"any"},
			DestPorts:    []string{"any"},
		},
	}
	options := routePolicyCommandOptions{localDestinations: []string{"192.168.50.0/24"}}
	commands := routePolicyCommandsWithOptions(policy, nil, options)
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "deny src 192.168.50.101/32 dst 192.168.50.0/24") {
		t.Fatalf("catch-all proxy ACL does not bypass local LAN: %s", joined)
	}
	if !strings.Contains(joined, "permit src 192.168.50.101/32 dst 0.0.0.0/0") {
		t.Fatalf("catch-all proxy ACL lost its external permit: %s", joined)
	}
	if strings.Index(joined, "deny src 192.168.50.101/32 dst 192.168.50.0/24") > strings.Index(joined, "permit src 192.168.50.101/32 dst 0.0.0.0/0") {
		t.Fatalf("local bypass deny must precede the catch-all permit: %s", joined)
	}
}

func TestRoutePolicyLocalBypassAlsoCoversNonOptimizedPolicy(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{
		ID:     "source-proxy",
		Action: "route",
		Match: trafficpolicy.Match{
			Sources:      []string{"192.168.50.101/32"},
			Destinations: []string{"any"},
		},
	}
	options := map[string]routePolicyCommandOptions{}
	addRoutePolicyLocalDestinations(options, []trafficpolicy.RoutePolicy{policy}, []AddressAssignment{{Role: "lan", CIDR: "192.168.50.1/24"}})
	commands := routePolicyCommandsWithOptions(policy, nil, options[policy.ID])
	if !strings.Contains(strings.Join(commands, "\n"), "deny src 192.168.50.101/32 dst 192.168.50.0/24") {
		t.Fatalf("non-optimized source policy did not receive the LAN bypass: %v", commands)
	}
}

func TestRoutePolicyOptimizedCatchAllInstallsLocalLANRoute(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{
		ID:       "default-proxy",
		Priority: 100,
		Action:   "route",
		Egress:   "proxy-wan",
		Match: trafficpolicy.Match{
			Destinations: []string{"0.0.0.0/0"},
		},
	}
	commands := routePolicyCommandsWithOptions(policy, nil, routePolicyCommandOptions{
		optimizedIPv4:    true,
		localDestinations: []string{"192.168.50.0/24"},
	})
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "ip route add table ") || !strings.Contains(joined, "192.168.50.0/24 via lyroute-$LY_ROUTE_LAN_INTERFACE") {
		t.Fatalf("optimized catch-all policy does not install local LAN route: %s", joined)
	}
}
