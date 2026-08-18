package vpp

import "testing"

func TestSecurityACLTargetUsesResolvedLANInterface(t *testing.T) {
	channel := vppctlChannel{lanVPPInterface: " lyroute-ens34 "}
	got, directions := channel.securityACLTarget(Operation{}, "input")
	if got != "lyroute-ens34" || len(directions) != 1 || directions[0] != "input" {
		t.Fatalf("security ACL target = %q %#v, want lyroute-ens34 input", got, directions)
	}
}

func TestSecurityACLTargetUsesGeneratedWANCommand(t *testing.T) {
	operation := Operation{VPPCtlCommands: []string{"?set interface input acl intfc pppoe_session1 ip4-table 42"}}
	got, directions := (vppctlChannel{lanVPPInterface: "lyroute-ens34"}).securityACLTarget(operation, "output")
	if got != "pppoe_session1" || len(directions) != 1 || directions[0] != "input" {
		t.Fatalf("security ACL target = %q %#v, want pppoe_session1 input", got, directions)
	}
}
