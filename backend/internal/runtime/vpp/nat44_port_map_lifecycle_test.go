package vpp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/nat"
)

func TestGatewayNAT44PortMapApplyReadsBackTypedState(t *testing.T) {
	// Given
	mapping := nat.PortMapping{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 443}
	client := &nat44LifecycleClient{replies: map[string]Reply{
		"vpp.nat44-ed.snapshot": {Payload: NAT44Readback{PortMappings: []nat.PortMapping{mapping}}},
	}}

	// When
	result, err := (Adapter{Client: client}).ApplyNAT44(context.Background(), NAT44Plan{TransactionID: "txn-nat-apply", PortMappings: []nat.PortMapping{mapping}}, Snapshot{})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.RequestID != "txn-nat-apply" || len(result.Readback.NAT.PortMappings) != 1 || result.Readback.NAT.PortMappings[0].ID != "web" {
		t.Fatalf("result = %#v, want typed NAT44 readback", result)
	}
	if client.operations[0].RequestID != "txn-nat-apply" || !strings.Contains(strings.Join(client.operations[0].VPPCtlCommands, "\n"), "nat44 add static mapping") {
		t.Fatalf("operations = %#v, want transaction-bound NAT44 command", client.operations)
	}
	if client.openCount != 1 {
		t.Fatalf("opened channels = %d, want apply/readback to share one channel", client.openCount)
	}
}

func TestGatewayNAT44PortMapReadbackRejectsMismatchedPayload(t *testing.T) {
	// Given
	client := &nat44LifecycleClient{replies: map[string]Reply{
		"vpp.nat44-ed.snapshot": {Payload: NAT44Readback{PortMappings: []nat.PortMapping{{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 9443, InternalHost: "192.168.88.20", InternalPort: 443}}}},
	}}

	// When
	_, err := (Adapter{Client: client}).ApplyNAT44(context.Background(), NAT44Plan{TransactionID: "txn-nat-mismatch", PortMappings: []nat.PortMapping{{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 443}}}, Snapshot{})

	// Then
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("error = %v, want mismatched NAT44 payload failure", err)
	}
}

func TestGatewayNAT44PortMapDeleteConfirmsAbsence(t *testing.T) {
	// Given
	client := &nat44LifecycleClient{replies: map[string]Reply{
		"vpp.nat44-ed.snapshot": {Payload: NAT44Readback{}},
	}}
	prior := nat44PriorSnapshot()

	// When
	result, err := (Adapter{Client: client}).ApplyNAT44(context.Background(), NAT44Plan{TransactionID: "txn-nat-delete", DeletePortMappings: []string{"web"}}, prior)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Readback.NAT.PortMappings) != 0 || !strings.Contains(strings.Join(client.operations[0].VPPCtlCommands, "\n"), " del") {
		t.Fatalf("result = %#v operations = %#v, want deleted mapping absent", result, client.operations)
	}
}

func TestGatewayNAT44FailureRestoresPriorSnapshot(t *testing.T) {
	// Given
	applyErr := errors.New("nat port-map command failed")
	prior := nat44PriorSnapshot()
	client := &nat44LifecycleClient{
		errors:  map[string]error{"vpp.nat44-ed.port-map": applyErr},
		replies: map[string]Reply{"vpp.nat44-ed.snapshot": {Payload: NAT44Readback{PortMappings: prior.NAT.PortMappings}}},
	}

	// When
	_, err := (Adapter{Client: client}).ApplyNAT44(context.Background(), NAT44Plan{TransactionID: "txn-nat-failure", PortMappings: []nat.PortMapping{{ID: "new", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 9443, InternalHost: "192.168.88.30", InternalPort: 443}}}, prior)

	// Then
	var lifecycleErr *NAT44LifecycleError
	if !errors.As(err, &lifecycleErr) || !errors.Is(err, applyErr) || lifecycleErr.RollbackResult != RollbackSucceeded || client.rollbackOperations != 2 {
		t.Fatalf("error = %T %v, want failed apply with prior NAT rollback", err, err)
	}
	for _, operation := range client.operations {
		if operation.Name == "vpp.nat44-ed.port-map.rollback-delete" && operation.Resource == "new" {
			return
		}
	}
	t.Fatalf("operations = %#v, want rollback deletion for newly added mapping", client.operations)
}

func TestGatewayNAT44ReportsExactRollbackCommandFailure(t *testing.T) {
	// Given
	rollbackErr := errors.New("rollback NAT command failed exactly")
	prior := nat44PriorSnapshot()
	client := &nat44LifecycleClient{errors: map[string]error{
		"vpp.nat44-ed.port-map":          errors.New("apply NAT command failed"),
		"vpp.nat44-ed.port-map.rollback": rollbackErr,
	}}

	// When
	_, err := (Adapter{Client: client}).ApplyNAT44(context.Background(), NAT44Plan{TransactionID: "txn-nat-rollback-failure", PortMappings: prior.NAT.PortMappings}, prior)

	// Then
	var lifecycleErr *NAT44LifecycleError
	if !errors.As(err, &lifecycleErr) || lifecycleErr.RollbackResult != RollbackFailed || !errors.Is(lifecycleErr.Rollback, rollbackErr) {
		t.Fatalf("error = %T %v, rollback = %v; want exact rollback command failure", err, err, lifecycleErr.Rollback)
	}
}

func TestGatewayNAT44RejectsIncompleteReadback(t *testing.T) {
	// Given
	client := &nat44LifecycleClient{replies: map[string]Reply{"vpp.nat44-ed.snapshot": {Payload: NAT44Readback{}}}}

	// When
	_, err := (Adapter{Client: client}).ApplyNAT44(context.Background(), NAT44Plan{TransactionID: "txn-nat-incomplete", PortMappings: []nat.PortMapping{{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 443}}}, Snapshot{})

	// Then
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("error = %v, want incomplete NAT readback failure", err)
	}
}

func nat44PriorSnapshot() Snapshot {
	return Snapshot{TransactionID: "txn-nat-prior", RequestID: "txn-nat-prior", NAT: nat.CompiledConfig{PortMappings: []nat.PortMapping{{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 443}}}, Hash: "prior-nat-hash"}
}

type nat44LifecycleClient struct {
	replies            map[string]Reply
	errors             map[string]error
	operations         []Operation
	rollbackOperations int
	openCount          int
}

func (client *nat44LifecycleClient) OpenChannel(context.Context) (Channel, error) {
	client.openCount++
	return &nat44LifecycleChannel{client: client}, nil
}

type nat44LifecycleChannel struct{ client *nat44LifecycleClient }

func (channel *nat44LifecycleChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	channel.client.operations = append(channel.client.operations, operation)
	if strings.Contains(operation.Name, ".rollback") {
		channel.client.rollbackOperations++
	}
	if err := channel.client.errors[operation.Name]; err != nil {
		return Reply{}, err
	}
	return channel.client.replies[operation.Name], nil
}

func (channel *nat44LifecycleChannel) Close() error { return nil }
