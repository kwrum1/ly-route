package vpp

import (
	"context"
	"os"
	"testing"
)

func TestManagementLCPVPPCTLIntegration(t *testing.T) {
	binary := os.Getenv("LY_ROUTE_VPPCTL_INTEGRATION_BINARY")
	if binary == "" {
		t.Skip("LY_ROUTE_VPPCTL_INTEGRATION_BINARY is not set")
	}
	vppInterface := os.Getenv("LY_ROUTE_LCP_VPP_INTERFACE")
	if vppInterface == "" {
		t.Fatal("LY_ROUTE_LCP_VPP_INTERFACE is required")
	}
	enabled := os.Getenv("LY_ROUTE_LCP_ENABLED") != "false"
	management := ManagementLCP{Enabled: enabled, VPPInterface: vppInterface, HostInterface: "lymgmt0"}
	operation := Operation{Name: "vpp.management-lcp", RequestID: "management-lcp-integration", Resource: "management-network", Payload: management, VPPCtlCommands: managementLCPCommands(management)}
	adapter := Adapter{Client: NewProductionVPPCTLClient(binary)}
	attempts := 1
	if enabled {
		attempts = 2
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := adapter.ExecuteOperations(context.Background(), []Operation{operation}); err != nil {
			t.Fatalf("management LCP apply %d failed: %v", attempt, err)
		}
	}
}
