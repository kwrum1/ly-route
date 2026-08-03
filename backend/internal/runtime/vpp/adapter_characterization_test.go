package vpp

import (
	"reflect"
	"testing"
)

func TestAdapterCharacterization_static_address_commands_match_base_output(t *testing.T) {
	// Given
	assignment := AddressAssignment{
		LinuxInterface: "eth1",
		VPPInterface:   "lyroute-eth1",
		CIDR:           "192.0.2.1/24",
	}
	want := []string{
		"set interface state lyroute-eth1 up",
		"?set interface ip address lyroute-eth1 192.0.2.1/24",
		"show interface address lyroute-eth1",
	}

	// When
	got := interfaceAddressCommands(assignment)

	// Then
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
}
