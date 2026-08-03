package vpp

import (
	"errors"
	"testing"
)

func TestBuildOperationsLocksAddressAssignmentsOutsideProvenAttachment(t *testing.T) {
	tests := []struct {
		name       string
		assignment AddressAssignment
		failedName string
	}{
		{
			name:       "management interface with custom VPP name",
			assignment: AddressAssignment{ID: "crafted", LinuxInterface: "eth0", VPPInterface: "custom-vpp-name", CIDR: "192.0.2.1/24"},
			failedName: "address_assignment_management_excluded",
		},
		{
			name:       "unproven Linux interface",
			assignment: AddressAssignment{ID: "unproven", LinuxInterface: "eth2", VPPInterface: "lyroute-eth2", CIDR: "192.0.2.1/24"},
			failedName: "address_assignment_proven",
		},
		{
			name:       "custom VPP interface for proven Linux interface",
			assignment: AddressAssignment{ID: "mismatch", LinuxInterface: "eth1", VPPInterface: "custom-vpp-name", CIDR: "192.0.2.1/24"},
			failedName: "address_assignment_vpp_interface_matches",
		},
		{
			name:       "missing VPP interface for proven Linux interface",
			assignment: AddressAssignment{ID: "missing", LinuxInterface: "eth1", CIDR: "192.0.2.1/24"},
			failedName: "address_assignment_vpp_interface_matches",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			plan := provenPlan(Plan{RequestID: "req-assignment-binding", AddressAssignments: []AddressAssignment{test.assignment}}, "eth1")

			// When
			operations, err := BuildOperations(plan)

			// Then
			if operations != nil {
				t.Fatalf("operations = %#v, want nil", operations)
			}
			var locked *DataplaneLockedError
			if !errors.As(err, &locked) {
				t.Fatalf("error = %T %v, want DataplaneLockedError", err, err)
			}
			for _, result := range locked.Prerequisites {
				if result.Name == test.failedName && !result.Passed {
					return
				}
			}
			t.Fatalf("missing failed prerequisite %q: %#v", test.failedName, locked.Prerequisites)
		})
	}
}
