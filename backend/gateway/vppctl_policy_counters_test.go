package gateway

import (
	"context"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
)

func TestVPPCTLPolicyCountersReadRealACLHitCounts(t *testing.T) {
	store := memoryGatewayTelemetryConfig{documents: map[string][]persistence.ConfigDocument{
		"route_policy": {{ResourceType: "route_policy", ResourceID: "route-40", Payload: []byte(`{"id":"route-40","enabled":true}`)}},
	}}
	collector := newVPPCTLPolicyCounters(store, "vppctl")
	collector.run = func(_ context.Context, _ string, args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "show acl-plugin acl":
			return "acl-index 2 count 1 tag {ly-route-route_40}\n  0: ipv4 permit src 10.1.18.101/32 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n", nil
		case "show acl-plugin tables":
			return "lookup applied entries:\n  2: acl 2 rule 0 action 1 bitmask-ready rule 0 mask type index: 3 colliding_rules: 1 collision_head_ae_idx 2 hitcount 14 acl_pos: 1\n", nil
		default:
			t.Fatalf("unexpected vppctl command: %v", args)
			return "", nil
		}
	}
	items, err := collector.PolicyHits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0]["id"] != "route-40" || items[0]["hits"] != uint64(14) || items[0]["readback_state"] != "available" {
		t.Fatalf("policy hits = %#v", items)
	}
}

func TestParseVPPACLHitsSumsRulesForConfiguredACL(t *testing.T) {
	observed := parseVPPACLHits(
		"acl-index 7 count 2 tag {ly-route-sec_office}\n",
		"0: acl 7 rule 0 action 1 hitcount 8 acl_pos: 0\n1: acl 7 rule 1 action 0 hitcount 3 acl_pos: 0\n",
	)
	if observed["ly-route-sec_office"].hits != 11 {
		t.Fatalf("hits = %d, want 11", observed["ly-route-sec_office"].hits)
	}
}
