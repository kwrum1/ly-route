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
