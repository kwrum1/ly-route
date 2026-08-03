package vpp

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ly-route/backend/internal/orchestrator"
)

func TestTransparentOrchestratorBondsUsesLogicalVPPNames(t *testing.T) {
	topology, err := orchestrator.ParseTopology(orchestrator.TopologyInput{
		SchemaVersion: 1, ManagementInterface: "mgmt0",
		Interfaces: []orchestrator.InterfaceInput{
			{Name: "lan", Role: orchestrator.RoleLAN, Port: "eth3"},
			{Name: "wan", Role: orchestrator.RoleWAN, Bond: &orchestrator.BondInput{Name: "bond-wan", Members: []string{"eth2", "eth1"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []BondState{{Name: "lyroute-bond-wan", Mode: "active-backup", Members: []string{"lyroute-eth1", "lyroute-eth2"}}}
	if got := TransparentOrchestratorBonds(topology.View()); !reflect.DeepEqual(got, want) {
		t.Fatalf("bonds = %#v, want %#v", got, want)
	}
}

func TestBuildTransparentBondTransitionOperationsDeletesBeforeCreate(t *testing.T) {
	current := []BondState{{Name: "lyroute-bond-wan", Mode: "active-backup", Members: []string{"lyroute-eth1", "lyroute-eth2"}}}
	desired := []BondState{{Name: "lyroute-bond-wan", Mode: "active-backup", Members: []string{"lyroute-eth1", "lyroute-eth4"}}}
	operations, err := BuildTransparentBondTransitionOperations("txn-bond-change", current, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || operations[0].Name != "vpp.transparent-orchestrator.bond-delete" || operations[1].Name != "vpp.transparent-orchestrator.bond-create" {
		t.Fatalf("operations = %#v, want delete then create", operations)
	}
	joined := strings.Join(operations[1].VPPCtlCommands, "\n")
	if !strings.Contains(joined, "bond add lyroute-bond-wan lyroute-eth4") || !strings.Contains(joined, "set interface state lyroute-bond-wan up") {
		t.Fatalf("create commands = %q", joined)
	}
}

func TestParseTransparentBondInventoryMapsStableIDAndMembers(t *testing.T) {
	state := BondState{Name: "lyroute-bond-wan", Mode: "active-backup", Members: []string{"lyroute-eth1", "lyroute-eth2"}}
	id, _ := vppBondIdentity(state.Name)
	output := fmt.Sprintf("BondEthernet%d\n  mode: active-backup\n  load balance: active-backup\n  number of active members: 1\n    lyroute-eth1\n      weight: 1, is_local_numa: 1, sw_if_index: 1\n  number of members: 2\n    lyroute-eth2\n    lyroute-eth1\n  device instance: %d\n  interface id: %d\n  sw_if_index: 5\n  hw_if_index: 5\n", id, id, id)
	got, err := parseTransparentBondInventory(output, []BondState{state})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []BondState{state}) {
		t.Fatalf("inventory = %#v, want %#v", got, state)
	}
}

func TestParseTransparentBondInventoryAllowsNoBonds(t *testing.T) {
	got, err := parseTransparentBondInventory("\n", []BondState{{Name: "lyroute-bond-wan", Mode: "active-backup", Members: []string{"lyroute-eth1", "lyroute-eth2"}}})
	if err != nil || len(got) != 0 {
		t.Fatalf("inventory = %#v, error = %v, want empty", got, err)
	}
}
