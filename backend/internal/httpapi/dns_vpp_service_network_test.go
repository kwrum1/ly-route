package httpapi

import (
	"context"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestCurrentDNSUpstreamsPinsSmartDNSToVPPServicePeer(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-vpp-service-network?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		resourceType string
		resourceID   string
		payload      map[string]any
	}{
		{"wan_link", "wan-primary", map[string]any{"id": "wan-primary", "interface_id": "wan0", "enabled": true, "type": "static", "gateway": "192.0.2.1"}},
		{"dns_upstream", "dns-primary", map[string]any{"id": "dns-primary", "enabled": true, "servers": []string{"9.9.9.9"}, "wan_egress_id": "wan-primary", "interface_id": "wan0"}},
	} {
		payload, hash, marshalErr := persistence.MarshalPayload(item.payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if saveErr := store.SaveConfig(ctx, persistence.ConfigDocument{ResourceType: item.resourceType, ResourceID: item.resourceID, Payload: payload, PayloadHash: hash, UpdatedAt: now}); saveErr != nil {
			t.Fatal(saveErr)
		}
	}

	server := New(WithStore(store), WithClock(func() time.Time { return now }))
	upstreams, _, networks, err := server.currentDNSUpstreams(ctx, trafficpolicy.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) != 1 {
		t.Fatalf("DNS VPP service network result = upstreams %#v networks %#v", upstreams, networks)
	}
	var pinnedInterface string
	var socketMark uint32
	for _, upstream := range upstreams {
		if upstream.ID == "dns-primary" {
			pinnedInterface = upstream.Interface
			socketMark = upstream.SocketMark
		}
	}
	if pinnedInterface != networks[0].HostInterface || pinnedInterface == "wan0" {
		t.Fatalf("SmartDNS upstream still points to physical WAN: %#v", upstreams)
	}
	if socketMark != networks[0].SocketMark || socketMark == 0 {
		t.Fatalf("SmartDNS upstream is missing its DNS service socket mark: %#v", upstreams)
	}
	if networks[0].UnderlayRoute != "192.0.2.1 lyroute-wan0" {
		t.Fatalf("VPP DNS underlay route = %q", networks[0].UnderlayRoute)
	}
}
