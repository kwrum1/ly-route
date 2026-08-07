package vpp

import "testing"

func TestRoutePathViaNeverUsesUnsafeInterfaceOnlyPPPoEPath(t *testing.T) {
	if got := routePathVia("pppoe_session0", "", ""); got != "ip4-lookup-in-table 0" {
		t.Fatalf("routePathVia(pppoe) = %q, want native main-table lookup", got)
	}
	if got := routePathVia("pppoe_session0", "10.67.0.1", ""); got != "10.67.0.1 pppoe_session0" {
		t.Fatalf("routePathVia(pppoe,next-hop) = %q", got)
	}
}
