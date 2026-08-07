package httpapi

import (
	"context"
	"testing"

	"ly-route/backend/internal/persistence"
)

func TestWANLinkEffectiveMTU(t *testing.T) {
	tests := []struct {
		name    string
		item    map[string]any
		want    int
		wantErr bool
	}{
		{name: "explicit PPPoE MTU", item: map[string]any{"wan_type": "pppoe", "mtu": 1460}, want: 1460},
		{name: "PPPoE defaults to protocol maximum", item: map[string]any{"wan_type": "pppoe"}, want: 1492},
		{name: "MRU constrains service path", item: map[string]any{"type": "pppoe", "mtu": 1492, "mru": 1452}, want: 1452},
		{name: "ethernet defaults to 1500", item: map[string]any{"wan_type": "dhcp"}, want: 1500},
		{name: "nested PPPoE mode", item: map[string]any{"ipv4": map[string]any{"mode": "pppoe"}}, want: 1492},
		{name: "reject undersized MTU", item: map[string]any{"wan_type": "static", "mtu": 500}, wantErr: true},
		{name: "reject oversized MTU", item: map[string]any{"wan_type": "static", "mtu": 9001}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := wanLinkEffectiveMTU(test.item)
			if test.wantErr {
				if err == nil {
					t.Fatalf("wanLinkEffectiveMTU(%v) = %d, want error", test.item, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("wanLinkEffectiveMTU(%v) = %d, want %d", test.item, got, test.want)
			}
		})
	}
}

func TestDNSServiceUnderlayMTUResolvesProxyWAN(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:dns-service-proxy-mtu-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := fixedClock()()
	for _, document := range []persistence.ConfigDocument{
		configDocument(t, "wan_link", "wan-pppoe", map[string]any{"id": "wan-pppoe", "enabled": true, "wan_type": "pppoe", "mtu": 1492}, now),
		configDocument(t, "proxy_egress", "proxy-wan", map[string]any{"id": "proxy-wan", "enabled": true, "underlay_wan_id": "wan-pppoe"}, now),
	} {
		if err := store.SaveConfig(ctx, document); err != nil {
			t.Fatal(err)
		}
	}
	server := New(WithStore(store))
	got, err := server.dnsServiceUnderlayMTU(ctx, "proxy-wan")
	if err != nil {
		t.Fatal(err)
	}
	if got != 1492 {
		t.Fatalf("proxy-backed DNS service MTU = %d, want 1492", got)
	}
}
