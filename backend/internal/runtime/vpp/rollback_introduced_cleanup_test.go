package vpp

import (
	"context"
	"errors"
	"testing"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestRollbackCleanupRemovesChangedAndIntroducedStateDependentFirst(t *testing.T) {
	tests := []struct {
		name    string
		cleanup func(context.Context, Channel, string) error
		want    []string
	}{
		{name: "interfaces and bonds", cleanup: func(ctx context.Context, channel Channel, transactionID string) error {
			return cleanupInterfaceBond(ctx, channel, transactionID, InterfaceBondPlan{Interfaces: []InterfaceState{{Name: "if-changed"}, {Name: "if-new"}}, Bonds: []BondState{{Name: "bond-changed"}, {Name: "bond-new"}}})
		}, want: []string{"vpp.interface-bond.rollback-delete", "vpp.interface-bond.rollback-delete", "vpp.interface.address.rollback-delete", "vpp.interface.address.rollback-delete"}},
		{name: "routes and WAN groups", cleanup: func(ctx context.Context, channel Channel, transactionID string) error {
			return cleanupRouteWANGroup(ctx, channel, transactionID, RouteWANGroupPlan{Routes: []trafficpolicy.RoutePolicy{{ID: "route-changed"}, {ID: "route-new"}}, WANGroups: []trafficpolicy.WANGroup{{ID: "wan-changed"}, {ID: "wan-new"}}})
		}, want: []string{"vpp.route-policy.rollback-delete", "vpp.route-policy.rollback-delete", "vpp.pbr.next-hop-group.rollback-delete", "vpp.pbr.next-hop-group.rollback-delete"}},
		{name: "ACLs and QoS", cleanup: func(ctx context.Context, channel Channel, transactionID string) error {
			return cleanupACLQoS(ctx, channel, transactionID, ACLQoSPlan{ACLs: []trafficpolicy.SecurityACL{{ID: "acl-changed"}, {ID: "acl-new"}}, QoS: []flow.VPPObjectGroup{{Kind: "qos-changed"}, {Kind: "qos-new"}}})
		}, want: []string{"vpp.qos.rollback-delete", "vpp.qos.rollback-delete", "vpp.security-acl.rollback-delete", "vpp.security-acl.rollback-delete"}},
		{name: "NAT44 and port maps", cleanup: func(ctx context.Context, channel Channel, transactionID string) error {
			return cleanupNAT44(ctx, channel, transactionID, NAT44Plan{StaticMappings: []nat.StaticMapping{{ID: "static-changed"}, {ID: "static-new"}}, PortMappings: []nat.PortMapping{{ID: "port-changed"}, {ID: "port-new"}}})
		}, want: []string{"vpp.nat44-ed.port-map.rollback-delete", "vpp.nat44-ed.port-map.rollback-delete", "vpp.nat44-ed.static-mapping.rollback-delete", "vpp.nat44-ed.static-mapping.rollback-delete"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &rollbackOrderClient{}
			err := test.cleanup(context.Background(), &rollbackOrderChannel{client: client}, "txn-cleanup")
			if err != nil {
				t.Fatal(err)
			}
			if len(client.operations) != len(test.want) {
				t.Fatalf("operations=%#v, want %#v", client.operations, test.want)
			}
			for index, operation := range client.operations {
				if operation.Name != test.want[index] {
					t.Fatalf("operation[%d]=%q, want dependent-first cleanup %q", index, operation.Name, test.want[index])
				}
			}
		})
	}
}

func TestRollbackPriorSnapshotAggregatesOriginalCleanupReplayAndReadbackErrors(t *testing.T) {
	tests := []struct {
		name   string
		apply  func(context.Context, map[string]error) error
		errors []error
	}{
		{name: "interfaces and bonds", apply: func(ctx context.Context, failures map[string]error) error {
			client := &lifecycleClient{errors: failures}
			_, err := (Adapter{Client: client}).ApplyInterfaceBond(ctx, InterfaceBondPlan{TransactionID: "txn-interface-errors", Interfaces: []InterfaceState{{Name: "if-new"}}, Bonds: []BondState{{Name: "bond-new"}}}, lifecycleSnapshot("txn-prior", "if-prior", "bond-prior"))
			return err
		}, errors: []error{errors.New("interface apply failed"), errors.New("bond cleanup failed"), errors.New("interface replay failed"), errors.New("interface readback failed")}},
		{name: "routes and WAN groups", apply: func(ctx context.Context, failures map[string]error) error {
			client := &routeWANLifecycleClient{errors: failures}
			_, err := (Adapter{Client: client}).ApplyRouteWANGroup(ctx, RouteWANGroupPlan{TransactionID: "txn-route-errors", WANGroups: []trafficpolicy.WANGroup{{ID: "wan-new"}}, Routes: []trafficpolicy.RoutePolicy{{ID: "route-new", Egress: "wan-new"}}}, routeWANPriorSnapshot())
			return err
		}, errors: []error{errors.New("route apply failed"), errors.New("route cleanup failed"), errors.New("WAN replay failed"), errors.New("route readback failed")}},
		{name: "ACLs and QoS", apply: func(ctx context.Context, failures map[string]error) error {
			client := &aclQoSClient{errors: failures}
			_, err := (Adapter{Client: client}).ApplyACLQoS(ctx, ACLQoSPlan{TransactionID: "txn-acl-errors", ACLs: []trafficpolicy.SecurityACL{{ID: "acl-new"}}, QoS: []flow.VPPObjectGroup{{Kind: "qos-new"}}}, Snapshot{ACLs: []trafficpolicy.SecurityACL{{ID: "acl-prior"}}, QoS: []flow.VPPObjectGroup{{Kind: "qos-prior"}}})
			return err
		}, errors: []error{errors.New("ACL apply failed"), errors.New("QoS cleanup failed"), errors.New("ACL replay failed"), errors.New("ACL readback failed")}},
		{name: "NAT44 and port maps", apply: func(ctx context.Context, failures map[string]error) error {
			client := &nat44LifecycleClient{errors: failures}
			prior := nat44PriorSnapshot()
			prior.NAT.StaticMappings = []nat.StaticMapping{{ID: "static-prior"}}
			_, err := (Adapter{Client: client}).ApplyNAT44(ctx, NAT44Plan{TransactionID: "txn-nat-errors", StaticMappings: []nat.StaticMapping{{ID: "static-new"}}, PortMappings: []nat.PortMapping{{ID: "port-new"}}}, prior)
			return err
		}, errors: []error{errors.New("NAT apply failed"), errors.New("port cleanup failed"), errors.New("static replay failed"), errors.New("NAT readback failed")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failures := map[string]error{}
			switch test.name {
			case "interfaces and bonds":
				failures["vpp.interface-bond"] = test.errors[0]
				failures["vpp.interface-bond.rollback-delete"] = test.errors[1]
				failures["vpp.interface.address.rollback"] = test.errors[2]
				failures["vpp.interface.snapshot"] = test.errors[3]
			case "routes and WAN groups":
				failures["vpp.route-policy"] = test.errors[0]
				failures["vpp.route-policy.rollback-delete"] = test.errors[1]
				failures["vpp.pbr.next-hop-group.rollback"] = test.errors[2]
				failures["vpp.pbr.next-hop-group.snapshot"] = test.errors[3]
			case "ACLs and QoS":
				failures["vpp.qos"] = test.errors[0]
				failures["vpp.qos.rollback-delete"] = test.errors[1]
				failures["vpp.security-acl.rollback"] = test.errors[2]
				failures["vpp.security-acl.snapshot"] = test.errors[3]
			case "NAT44 and port maps":
				failures["vpp.nat44-ed.port-map"] = test.errors[0]
				failures["vpp.nat44-ed.port-map.rollback-delete"] = test.errors[1]
				failures["vpp.nat44-ed.static-mapping.rollback"] = test.errors[2]
				failures["vpp.nat44-ed.snapshot"] = test.errors[3]
			}
			err := test.apply(context.Background(), failures)
			for _, want := range test.errors {
				if !errors.Is(err, want) {
					t.Fatalf("error=%v, missing joined error %q", err, want)
				}
			}
		})
	}
}

func TestRollbackPriorSnapshotContinuesAfterEarlyDependentCleanupError(t *testing.T) {
	// Given
	originalErr := errors.New("bond apply failed")
	cleanupErr := errors.New("first bond cleanup failed")
	replayInterfaceErr := errors.New("interface replay failed")
	replayBondErr := errors.New("bond replay failed")
	readbackErr := errors.New("prior readback failed")
	client := &resourceFailureClient{failures: map[string]error{
		"vpp.interface-bond:bond-1":                 originalErr,
		"vpp.interface-bond.rollback-delete:bond-1": cleanupErr,
		"vpp.interface.address.rollback:if-prior":   replayInterfaceErr,
		"vpp.interface-bond.rollback:bond-prior":    replayBondErr,
		"vpp.interface.snapshot:interfaces":         readbackErr,
	}}
	prior := lifecycleSnapshot("txn-prior", "if-prior", "bond-prior")
	plan := InterfaceBondPlan{
		TransactionID: "txn-continue-cleanup",
		Interfaces:    []InterfaceState{{Name: "if-1"}, {Name: "if-2"}},
		Bonds:         []BondState{{Name: "bond-1"}, {Name: "bond-2"}},
	}

	// When
	_, err := (Adapter{Client: client}).ApplyInterfaceBond(context.Background(), plan, prior)

	// Then
	for _, want := range []error{originalErr, cleanupErr, replayInterfaceErr, replayBondErr, readbackErr} {
		if !errors.Is(err, want) {
			t.Fatalf("error=%v, missing joined failure %q", err, want)
		}
	}
	wantOperations := []string{
		"vpp.interface.address:if-1",
		"vpp.interface.address:if-2",
		"vpp.interface-bond:bond-1",
		"vpp.interface-bond.rollback-delete:bond-1",
		"vpp.interface-bond.rollback-delete:bond-2",
		"vpp.interface.address.rollback-delete:if-1",
		"vpp.interface.address.rollback-delete:if-2",
		"vpp.interface.address.rollback:if-prior",
		"vpp.interface-bond.rollback:bond-prior",
		"vpp.interface.snapshot:interfaces",
	}
	if len(client.operations) != len(wantOperations) {
		t.Fatalf("operations=%#v, want complete cleanup/replay sequence", client.operations)
	}
	for index, operation := range client.operations {
		got := operation.Name + ":" + operation.Resource
		if got != wantOperations[index] {
			t.Fatalf("operation[%d]=%q, want %q", index, got, wantOperations[index])
		}
	}
}

type rollbackOrderClient struct{ operations []Operation }

func (client *rollbackOrderClient) OpenChannel(context.Context) (Channel, error) {
	return &rollbackOrderChannel{client: client}, nil
}

type rollbackOrderChannel struct{ client *rollbackOrderClient }

func (channel *rollbackOrderChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	channel.client.operations = append(channel.client.operations, operation)
	return Reply{}, nil
}

func (channel *rollbackOrderChannel) Close() error { return nil }

type resourceFailureClient struct {
	failures   map[string]error
	operations []Operation
}

func (client *resourceFailureClient) OpenChannel(context.Context) (Channel, error) {
	return &resourceFailureChannel{client: client}, nil
}

type resourceFailureChannel struct{ client *resourceFailureClient }

func (channel *resourceFailureChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	channel.client.operations = append(channel.client.operations, operation)
	if err := channel.client.failures[operation.Name+":"+operation.Resource]; err != nil {
		return Reply{}, err
	}
	return Reply{}, nil
}

func (channel *resourceFailureChannel) Close() error { return nil }
