package vpp

import (
	"context"
	"os"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestVPPCTLSecurityACLLifecycleIntegration(t *testing.T) {
	binary := os.Getenv("LY_ROUTE_VPPCTL_INTEGRATION_BINARY")
	if binary == "" {
		t.Skip("LY_ROUTE_VPPCTL_INTEGRATION_BINARY is not set")
	}
	acl := trafficpolicy.SecurityACL{
		ID: "integration-deny", Priority: 10, Action: "deny",
		Match: trafficpolicy.Match{
			Sources: []string{"10.0.0.2/32"}, Destinations: []string{"10.0.1.2/32"},
			Protocols: []string{"icmp"}, SourcePorts: []string{"any"}, DestPorts: []string{"any"}, Direction: "input",
		},
	}
	adapter := Adapter{Client: NewProductionVPPCTLClient(binary)}
	ctx := context.Background()
	switch os.Getenv("LY_ROUTE_SECURITY_ACL_ACTION") {
	case "apply":
		result, err := adapter.ApplyACLQoS(ctx, ACLQoSPlan{TransactionID: "integration-acl-apply", ACLs: []trafficpolicy.SecurityACL{acl}}, Snapshot{})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Readback.ACLs) != 1 || result.Readback.ACLs[0].ID != acl.ID {
			t.Fatalf("unexpected ACL readback: %#v", result.Readback.ACLs)
		}
	case "delete":
		result, err := adapter.ApplyACLQoS(ctx, ACLQoSPlan{
			TransactionID: "integration-acl-delete", DeleteACLs: []string{acl.ID}, DeleteACLState: []trafficpolicy.SecurityACL{acl},
		}, Snapshot{ACLs: []trafficpolicy.SecurityACL{acl}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Readback.ACLs) != 0 {
			t.Fatalf("deleted ACL remains in readback: %#v", result.Readback.ACLs)
		}
	default:
		t.Fatal("LY_ROUTE_SECURITY_ACL_ACTION must be apply or delete")
	}
}
