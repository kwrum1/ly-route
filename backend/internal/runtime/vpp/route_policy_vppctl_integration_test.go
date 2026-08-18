package vpp

import (
	"context"
	"os"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestReplaceRoutePolicyACLReferenceUsesRuntimeIndex(t *testing.T) {
	command := "abf policy add id 11198 acl 48810 via 198.18.34.90 lypxinffdc88"
	want := "abf policy add id 11198 acl 7 via 198.18.34.90 lypxinffdc88"
	if got := replaceRoutePolicyACLReference(command, 7); got != want {
		t.Fatalf("rewritten ABF ACL reference = %q, want %q", got, want)
	}
}

func TestVPPCTLRoutePolicyLifecycleIntegration(t *testing.T) {
	binary := os.Getenv("LY_ROUTE_VPPCTL_INTEGRATION_BINARY")
	if binary == "" {
		t.Skip("LY_ROUTE_VPPCTL_INTEGRATION_BINARY is not set")
	}
	compiled, err := trafficpolicy.CompileConfig([]map[string]any{{
		"id": "integration-route", "priority": 10, "action": "route", "egress": "wan0",
		"match": map[string]any{
			"src_ip": "10.0.0.2-10.0.0.3", "dst_ip": "10.0.1.2/32", "protocol": "icmp",
		},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	route := compiled.RoutePolicies[0]
	route.Path = &trafficpolicy.WANPath{VPPInterface: "lyroute-wan0", NextHop: "10.0.1.2"}
	adapter := Adapter{Client: NewProductionVPPCTLClient(binary)}
	ctx := context.Background()
	switch os.Getenv("LY_ROUTE_ROUTE_POLICY_ACTION") {
	case "apply":
		result, err := adapter.ApplyRouteWANGroup(ctx, RouteWANGroupPlan{TransactionID: "integration-route-apply", Routes: []trafficpolicy.RoutePolicy{route}}, Snapshot{})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Readback.RoutePolicies) != 1 || result.Readback.RoutePolicies[0].ID != route.ID {
			t.Fatalf("unexpected route readback: %#v", result.Readback.RoutePolicies)
		}
	case "delete":
		result, err := adapter.ApplyRouteWANGroup(ctx, RouteWANGroupPlan{
			TransactionID: "integration-route-delete", DeleteRoutes: []string{route.ID}, DeleteRouteState: []trafficpolicy.RoutePolicy{route},
		}, Snapshot{RoutePolicies: []trafficpolicy.RoutePolicy{route}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Readback.RoutePolicies) != 0 {
			t.Fatalf("deleted route remains in readback: %#v", result.Readback.RoutePolicies)
		}
	default:
		t.Fatal("LY_ROUTE_ROUTE_POLICY_ACTION must be apply or delete")
	}
}
