package httpapi

import (
	"context"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
)

func TestCurrentWANGroupBindingsKeepsPPPoEInterfaceDynamic(t *testing.T) {
	// Given: an enabled PPPoE WAN whose native session can be recreated during apply.
	server, store, _ := productTestServer(t, product.Gateway())
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	document := configDocument(t, "wan_link", "wan-primary", map[string]any{
		"id":            "wan-primary",
		"enabled":       true,
		"wan_type":      "pppoe",
		"interface_id":  "ens33",
		"pppoe_peer_id": "wan-primary",
	}, now)
	if err := store.SaveConfig(context.Background(), persistence.ConfigDocument(document)); err != nil {
		t.Fatal(err)
	}

	// When: route and WAN-group bindings are compiled before the session exists.
	bindings, err := server.currentWANGroupBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Then: execution resolves the current session instead of freezing a generated name.
	if got := bindings["wan-primary"].VPPInterface; got != "pppoe-runtime:wan-primary" {
		t.Fatalf("PPPoE VPP interface = %q, want runtime binding", got)
	}
}
