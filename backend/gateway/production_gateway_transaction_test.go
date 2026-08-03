package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/vpp"
)

func TestProductionGatewayTransactionUsesConfiguredVPPCTL(t *testing.T) {
	// Given
	directory := t.TempDir()
	trace := filepath.Join(directory, "trace.log")
	binary := filepath.Join(directory, "vppctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MAIN_VPPCTL_TRACE\"\nif [ \"$*\" = 'show interface address' ]; then printf 'lyroute-eth1 (up):\\n  L3 192.0.2.2/24\\n'; fi\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_VPPCTL", binary)
	t.Setenv("MAIN_VPPCTL_TRACE", trace)
	prior := vpp.Plan{}
	desired := vpp.Plan{Interfaces: []vpp.InterfaceState{{Name: "lyroute-eth1", AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.2/24"}}}}
	transaction := productionGatewayTransaction()
	plan := apply.Plan{Request: apply.Request{TransactionID: "txn-main-composition", Resource: "/api/v1/config/apply"}, Previous: apply.PreviousState{Available: true, GatewayPlan: &prior}, GatewayPlan: desired}

	// When
	result, err := transaction.Run(context.Background(), plan)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Resource != "interfaces" || !result.Evidence[0].Readback.Fresh {
		t.Fatalf("main composition evidence = %#v", result.Evidence)
	}
	commands, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), "set interface state lyroute-eth1 up") || !strings.Contains(string(commands), "show interface address") {
		t.Fatalf("main vppctl trace = %s", commands)
	}
	if result.Evidence[0].ApplyReceipt.AppliedAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("main production clock was not used: %s", result.Evidence[0].ApplyReceipt.AppliedAt)
	}
}
