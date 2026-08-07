package httpapi

import (
	"context"
	"testing"
)

func TestResolvePPPoENATWANAddressesUsesLiveSessionAndClearsStaleAddress(t *testing.T) {
	items := []map[string]any{
		{"id": "wan-pppoe", "type": "pppoe", "current_address": "198.51.100.9"},
		{"id": "wan-static", "type": "static4", "current_address": "203.0.113.8/24"},
	}
	lookup := func(_ context.Context, id string) (string, string, bool) {
		if id == "wan-pppoe" {
			return "10.67.0.10", "", true
		}
		return "", "", false
	}

	resolved := resolvePPPoENATWANAddresses(context.Background(), items, lookup)
	if got := stringField(resolved[0], "current_address"); got != "10.67.0.10" {
		t.Fatalf("PPPoE current_address = %q, want live session address", got)
	}
	if got := stringField(resolved[1], "current_address"); got != "203.0.113.8/24" {
		t.Fatalf("static WAN current_address = %q", got)
	}
	if got := stringField(items[0], "current_address"); got != "198.51.100.9" {
		t.Fatalf("input was mutated: current_address = %q", got)
	}

	disconnected := resolvePPPoENATWANAddresses(context.Background(), items, func(context.Context, string) (string, string, bool) {
		return "", "", false
	})
	if got := stringField(disconnected[0], "current_address"); got != "" {
		t.Fatalf("disconnected PPPoE retained stale address %q", got)
	}
}
