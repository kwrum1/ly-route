package httpapi

import (
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func TestRuntimePolicyDefaultWANDenyRequiresStaticWANAssignment(t *testing.T) {
	policy := trafficpolicy.Config{SecurityACLs: []trafficpolicy.SecurityACL{
		{ID: "sec-acl-default-deny-wan"},
		{ID: "user-rule"},
	}}
	pppoe := runtimePolicyForAddressAssignments(policy, []vpp.AddressAssignment{{Role: "lan", VPPInterface: "lyroute-eth1"}})
	if len(pppoe.SecurityACLs) != 1 || pppoe.SecurityACLs[0].ID != "user-rule" {
		t.Fatalf("PPPoE runtime ACLs = %#v", pppoe.SecurityACLs)
	}
	static := runtimePolicyForAddressAssignments(policy, []vpp.AddressAssignment{{Role: "wan", VPPInterface: "lyroute-eth2"}})
	if len(static.SecurityACLs) != 2 {
		t.Fatalf("static WAN runtime ACLs = %#v", static.SecurityACLs)
	}
}
