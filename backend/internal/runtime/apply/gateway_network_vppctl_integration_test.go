package apply

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

func TestGatewayNetworkProductionTransactionVPPCTLIntegration(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LY_ROUTE_VPPCTL_INTEGRATION_BINARY"))
	if binary == "" {
		t.Skip("LY_ROUTE_VPPCTL_INTEGRATION_BINARY is not set")
	}
	faultMarker := strings.TrimSpace(os.Getenv("LY_ROUTE_NETWORK_FAULT_MARKER"))
	if faultMarker == "" {
		t.Fatal("LY_ROUTE_NETWORK_FAULT_MARKER is required")
	}

	ctx := context.Background()
	now := func() time.Time { return time.Now().UTC() }
	adapter := vpp.Adapter{Client: vpp.NewProductionVPPCTLClient(binary)}
	transaction := NewProductionGatewayTransaction(adapter, now)
	desired := vpp.Plan{
		RequestID: "gateway-network-live-apply",
		Interfaces: []vpp.InterfaceState{
			{Name: "lyroute-lan0", AdminState: "up", LinkState: "up", Addresses: []string{"10.0.0.1/24"}},
			{Name: "lyroute-wan0", AdminState: "up", LinkState: "up", Addresses: []string{"10.0.1.1/24"}},
			{Name: "lyroute-bm0", AdminState: "up", LinkState: "up"},
			{Name: "lyroute-bm1", AdminState: "up", LinkState: "up"},
		},
		Bonds: []vpp.BondState{{Name: "lyroute-bond0", Mode: "active-backup", Members: []string{"lyroute-bm0", "lyroute-bm1"}}},
	}
	action := strings.TrimSpace(os.Getenv("LY_ROUTE_NETWORK_TRANSACTION_ACTION"))
	if action == "apply" {
		result, err := transaction.Run(ctx, Plan{
			Request:     Request{TransactionID: desired.RequestID, Resource: "/api/v1/runtime/apply"},
			GatewayPlan: desired,
		})
		if err != nil {
			t.Fatalf("initial production Gateway transaction: %v", err)
		}
		if !reflect.DeepEqual(result.Order, []string{"interfaces", "bonds"}) || len(result.Evidence) != 2 || result.Rollback.Status != ReceiptApplied {
			t.Fatalf("initial transaction result = %#v", result)
		}
		assertGatewayNetworkLiveState(t, ctx, adapter, desired)
		return
	}
	if action == "reconcile" {
		result, err := transaction.Run(ctx, Plan{
			Request:     Request{TransactionID: "gateway-network-drift-reconcile", Resource: "/api/v1/runtime/apply"},
			Previous:    PreviousState{Available: true, GatewayPlan: &desired},
			GatewayPlan: desired,
		})
		if err != nil {
			t.Fatalf("production Gateway drift reconciliation: %v", err)
		}
		if !containsString(result.Order, "interfaces") || result.Rollback.Status != ReceiptApplied {
			t.Fatalf("drift reconciliation result = %#v", result)
		}
		assertGatewayNetworkLiveState(t, ctx, adapter, desired)
		return
	}
	if action != "fault" {
		t.Fatal("LY_ROUTE_NETWORK_TRANSACTION_ACTION must be apply, fault or reconcile")
	}
	assertGatewayNetworkLiveState(t, ctx, adapter, desired)

	// Add two bonds in one desired generation, then inject a real vppctl failure
	// while the second bond is adding a member. The first new bond has already
	// reached VPP at that point. The production transaction must remove that
	// partial generation and restore the exact prior interfaces and bond.
	faultDesired := desired
	faultDesired.RequestID = "gateway-network-partial-fault"
	faultDesired.Bonds = append(append([]vpp.BondState(nil), desired.Bonds...),
		vpp.BondState{Name: "lyroute-fault-bond1", Mode: "active-backup", Members: []string{"lyroute-bm2", "lyroute-bm3"}},
		vpp.BondState{Name: "lyroute-fault-bond2", Mode: "active-backup", Members: []string{"lyroute-bm4", "lyroute-bm5"}},
	)
	if err := os.WriteFile(faultMarker, []byte("inject bond member apply failure\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, faultErr := transaction.Run(ctx, Plan{
		Request:     Request{TransactionID: faultDesired.RequestID, Resource: "/api/v1/runtime/apply"},
		Previous:    PreviousState{Available: true, GatewayPlan: &desired},
		GatewayPlan: faultDesired,
	})
	if removeErr := os.Remove(faultMarker); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		t.Fatal(removeErr)
	}
	var transactionErr *GatewayTransactionError
	if !errors.As(faultErr, &transactionErr) || transactionErr.Resource != "bonds" || transactionErr.Rollback.Status != ReceiptRolledBack || transactionErr.RollbackErr != nil {
		t.Fatalf("partial fault error = %#v, want bonds failure with successful rollback", faultErr)
	}
	assertGatewayNetworkLiveState(t, ctx, adapter, desired)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func assertGatewayNetworkLiveState(t *testing.T, ctx context.Context, adapter vpp.Adapter, desired vpp.Plan) {
	t.Helper()
	request := vpp.GatewayDiffSnapshotRequest("gateway-network-live-readback", desired, desired)
	live, err := adapter.Snapshot(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	wantInterfaces := append([]vpp.InterfaceState(nil), desired.Interfaces...)
	sort.Slice(wantInterfaces, func(i, j int) bool { return wantInterfaces[i].Name < wantInterfaces[j].Name })
	if !reflect.DeepEqual(live.Interfaces, wantInterfaces) {
		t.Fatalf("live interfaces = %#v, want %#v", live.Interfaces, wantInterfaces)
	}
	if !reflect.DeepEqual(live.Bonds, desired.Bonds) {
		t.Fatalf("live bonds = %#v, want %#v", live.Bonds, desired.Bonds)
	}
}
