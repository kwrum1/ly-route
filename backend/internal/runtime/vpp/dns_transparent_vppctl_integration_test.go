package vpp

import (
	"context"
	"os"
	"testing"
)

func TestDNSTransparentVPPCTLIntegration(t *testing.T) {
	binary := os.Getenv("LY_ROUTE_VPPCTL_INTEGRATION_BINARY")
	if binary == "" {
		t.Skip("LY_ROUTE_VPPCTL_INTEGRATION_BINARY is not set")
	}
	interception := DNSTransparentInterception{
		LANInterface: "tap0",
		IPv4Prefixes: []string{"192.0.2.0/24"},
		IPv6Prefixes: []string{"2001:db8:1::/64"},
	}
	operation := Operation{
		Name:           "vpp.dns-transparent-interception",
		RequestID:      "dns-transparent-integration",
		Resource:       "gateway-dns",
		Payload:        interception,
		VPPCtlCommands: dnsTransparentCommands(interception),
	}
	adapter := Adapter{Client: NewProductionVPPCTLClient(binary)}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := adapter.ExecuteOperations(context.Background(), []Operation{operation}); err != nil {
			t.Fatalf("production DNS interception apply %d failed: %v", attempt, err)
		}
	}
}
