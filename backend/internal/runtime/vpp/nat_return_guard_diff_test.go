package vpp

import (
	"slices"
	"testing"

	"ly-route/backend/internal/runtime/nat"
)

func TestGatewayDiffRepairsMissingNATReturnPathGuard(t *testing.T) {
	wanted := nat.PortMapping{
		ID: "web", Protocol: "tcp", ExternalAddress: "10.67.0.12", ExternalPort: 18080,
		InternalHost: "192.168.88.100", InternalPort: 18080, WANInterface: "pppoe_session0",
		WANNextHop: "10.67.0.1", ReturnPathGuard: true,
	}
	observed := wanted
	observed.ReturnPathGuard = false
	plan := Plan{NAT: nat.CompiledConfig{PortMappings: []nat.PortMapping{wanted}}}

	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID: "txn-return-path-drift", Prior: plan, Desired: plan,
		Live:                Snapshot{NAT: nat.CompiledConfig{PortMappings: []nat.PortMapping{observed}}},
		RepairVerifiedDrift: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(diff.PortMaps.DeletePortMappings, []string{"web"}) || len(diff.PortMaps.PortMappings) != 1 || diff.PortMaps.PortMappings[0] != wanted {
		t.Fatalf("return-path repair diff = %#v", diff.PortMaps)
	}
}
