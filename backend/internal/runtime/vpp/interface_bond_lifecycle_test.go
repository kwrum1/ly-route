package vpp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGatewayInterfaceBondApplySuccessReadsBackTransaction(t *testing.T) {
	// Given
	prior := lifecycleSnapshot("txn-prior", "lyroute-eth1", "bond0")
	client := &lifecycleClient{replies: map[string]Reply{
		"vpp.interface-bond":          {Payload: "applied"},
		"vpp.interface.address":       {Payload: "applied"},
		"vpp.interface.snapshot":      {Payload: InterfaceReadback{Interfaces: []InterfaceState{{Name: "lyroute-eth1", AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.1/24"}}}}},
		"vpp.interface-bond.snapshot": {Payload: BondReadback{Bonds: []BondState{{Name: "bond0", Mode: "active-backup", Members: []string{"lyroute-eth1"}}}}},
	}}

	// When
	result, err := (Adapter{Client: client}).ApplyInterfaceBond(context.Background(), InterfaceBondPlan{
		TransactionID: "txn-apply",
		Interfaces:    []InterfaceState{{Name: "lyroute-eth1", AdminState: "up", Addresses: []string{"192.0.2.1/24"}}},
		Bonds:         []BondState{{Name: "bond0", Mode: "active-backup", Members: []string{"lyroute-eth1"}}},
	}, prior)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.RequestID != "txn-apply" || result.Readback.TransactionID != "txn-apply" {
		t.Fatalf("result identity = %#v", result)
	}
	if len(client.operations) != 4 || client.operations[0].RequestID != "txn-apply" || client.operations[2].RequestID != "txn-apply" {
		t.Fatalf("operations = %#v, want transaction-bound apply/readback", client.operations)
	}
}

func TestGatewayInterfaceBondDeleteCompilesSymmetricOperations(t *testing.T) {
	// Given
	plan := InterfaceBondPlan{TransactionID: "txn-delete", DeleteInterfaces: []string{"lyroute-eth1"}, DeleteInterfaceState: []InterfaceState{{Name: "lyroute-eth1", Addresses: []string{"192.0.2.1/24"}}}, DeleteBonds: []string{"bond0"}}

	// When
	operations, err := BuildInterfaceBondOperations(plan)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || !strings.Contains(strings.Join(operations[0].VPPCtlCommands, "\n"), "del") || !strings.Contains(strings.Join(operations[1].VPPCtlCommands, "\n"), "delete bond bond0") {
		t.Fatalf("delete operations = %#v", operations)
	}
}

func TestGatewayInterfaceBondUsesVPP2510CLIAndStableLogicalName(t *testing.T) {
	operations, err := BuildInterfaceBondOperations(InterfaceBondPlan{
		TransactionID: "txn-bond-cli",
		Bonds:         []BondState{{Name: "bond0", Mode: "active-backup", Members: []string{"lyroute-eth1", "lyroute-eth2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %#v", operations)
	}
	id, createdName := vppBondIdentity("bond0")
	want := []string{
		fmt.Sprintf("create bond mode active-backup id %d", id),
		fmt.Sprintf("set interface name %s bond0", createdName),
		"bond add bond0 lyroute-eth1",
		"bond add bond0 lyroute-eth2",
	}
	if !reflect.DeepEqual(operations[0].VPPCtlCommands, want) {
		t.Fatalf("bond commands = %#v, want %#v", operations[0].VPPCtlCommands, want)
	}
}

func TestGatewayInterfaceBondDeleteExecutesThroughChannelAndReadsBack(t *testing.T) {
	// Given
	client := &lifecycleClient{replies: map[string]Reply{
		"vpp.interface.snapshot":      {Payload: InterfaceReadback{Interfaces: []InterfaceState{{Name: "lyroute-eth2", AdminState: "up", LinkState: "up"}}}},
		"vpp.interface-bond.snapshot": {Payload: BondReadback{Bonds: []BondState{{Name: "bond1", Mode: "active-backup", Members: []string{"lyroute-eth2"}}}}},
	}}

	// When
	result, err := (Adapter{Client: client}).ApplyInterfaceBond(context.Background(), InterfaceBondPlan{
		TransactionID:    "txn-delete-live",
		DeleteInterfaces: []string{"lyroute-eth1"},
		DeleteBonds:      []string{"bond0"},
	}, lifecycleSnapshot("txn-prior", "lyroute-eth1", "bond0"))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Receipt.Operations) != 2 || len(client.operations) != 4 {
		t.Fatalf("receipt=%#v operations=%#v, want two deletes and two readbacks", result.Receipt, client.operations)
	}
	if client.operations[0].Name != "vpp.interface.address" || client.operations[1].Name != "vpp.interface-bond" || client.operations[2].Name != "vpp.interface.snapshot" || client.operations[3].Name != "vpp.interface-bond.snapshot" {
		t.Fatalf("operations = %#v, want delete/readback sequence", client.operations)
	}
	if result.Readback.Interfaces[0].Name != "lyroute-eth2" || result.Readback.Bonds[0].Name != "bond1" {
		t.Fatalf("readback = %#v, want post-delete state", result.Readback)
	}
}

func TestGatewayInterfaceBondApplyFailureRestoresPriorSnapshot(t *testing.T) {
	// Given
	prior := lifecycleSnapshot("txn-prior", "lyroute-eth1", "bond0")
	client := &lifecycleClient{errors: map[string]error{"vpp.interface-bond": errors.New("bond command failed")}, replies: map[string]Reply{
		"vpp.interface.address":       {Payload: "applied"},
		"vpp.interface.snapshot":      {Payload: InterfaceReadback{Interfaces: prior.Interfaces}},
		"vpp.interface-bond.snapshot": {Payload: BondReadback{Bonds: prior.Bonds}},
	}}

	// When
	_, err := (Adapter{Client: client}).ApplyInterfaceBond(context.Background(), InterfaceBondPlan{TransactionID: "txn-failure", Interfaces: []InterfaceState{{Name: "lyroute-eth1", AdminState: "up", Addresses: []string{"192.0.2.1/24"}}}, Bonds: []BondState{{Name: "bond0", Mode: "active-backup", Members: []string{"lyroute-eth1"}}}}, prior)

	// Then
	var lifecycleErr *InterfaceBondLifecycleError
	if !errors.As(err, &lifecycleErr) || lifecycleErr.Operation != "vpp.interface-bond" || lifecycleErr.RollbackResult != RollbackSucceeded {
		t.Fatalf("error = %T %v, want failed operation and successful rollback", err, err)
	}
	if client.rollbackOperations == 0 {
		t.Fatal("rollback issued no VPP operations")
	}
}

func TestGatewayInterfaceBondApplyReportsRollbackFailure(t *testing.T) {
	// Given
	prior := lifecycleSnapshot("txn-prior", "lyroute-eth1", "bond0")
	client := &lifecycleClient{errors: map[string]error{"vpp.interface-bond": errors.New("bond command failed"), "vpp.interface-bond.rollback": errors.New("rollback command failed")}}

	// When
	_, err := (Adapter{Client: client}).ApplyInterfaceBond(context.Background(), InterfaceBondPlan{TransactionID: "txn-rollback-failure", Bonds: []BondState{{Name: "bond0", Mode: "active-backup", Members: []string{"lyroute-eth1"}}}}, prior)

	// Then
	var lifecycleErr *InterfaceBondLifecycleError
	if !errors.As(err, &lifecycleErr) || lifecycleErr.RollbackResult != RollbackFailed || lifecycleErr.Rollback == nil {
		t.Fatalf("error = %T %v, want truthful rollback failure", err, err)
	}
}

func TestGatewayInterfaceBondRejectsManagementInterface(t *testing.T) {
	// Given
	plan := InterfaceBondPlan{TransactionID: "txn-locked", ManagementInterface: "eth0", Interfaces: []InterfaceState{{Name: "eth0", AdminState: "up"}}}

	// When
	operations, err := BuildInterfaceBondOperations(plan)

	// Then
	var locked *DataplaneLockedError
	if operations != nil || !errors.As(err, &locked) {
		t.Fatalf("operations = %#v, error = %v, want dataplane lock", operations, err)
	}
}

func TestGatewayInterfaceBondRejectsManagementBondMember(t *testing.T) {
	// Given
	plan := InterfaceBondPlan{TransactionID: "txn-locked-member", ManagementInterface: "eth0", Bonds: []BondState{{Name: "bond0", Members: []string{"eth0", "eth1"}}}}

	// When
	operations, err := BuildInterfaceBondOperations(plan)

	// Then
	var locked *DataplaneLockedError
	if operations != nil || !errors.As(err, &locked) {
		t.Fatalf("operations = %#v, error = %v, want bond member dataplane lock", operations, err)
	}
	if locked.Prerequisites[0].Interface != "eth0" || !strings.Contains(locked.Prerequisites[0].Reason, "vpp.interface-bond") {
		t.Fatalf("lock = %#v, want management bond-member proof", locked.Prerequisites)
	}
}

func lifecycleSnapshot(transactionID, interfaceName, bondName string) Snapshot {
	return Snapshot{TransactionID: transactionID, RequestID: transactionID, ReadbackAt: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), Interfaces: []InterfaceState{{Name: interfaceName, AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.1/24"}}}, Bonds: []BondState{{Name: bondName, Mode: "active-backup", Members: []string{interfaceName}}}, Hash: "prior-hash"}
}

type lifecycleClient struct {
	replies            map[string]Reply
	errors             map[string]error
	operations         []Operation
	rollbackOperations int
}

func (client *lifecycleClient) OpenChannel(context.Context) (Channel, error) {
	return &lifecycleChannel{client: client}, nil
}

type lifecycleChannel struct{ client *lifecycleClient }

func (channel *lifecycleChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	channel.client.operations = append(channel.client.operations, operation)
	if strings.HasSuffix(operation.Name, ".rollback") {
		channel.client.rollbackOperations++
	}
	if err := channel.client.errors[operation.Name]; err != nil {
		return Reply{}, err
	}
	return channel.client.replies[operation.Name], nil
}

func (channel *lifecycleChannel) Close() error { return nil }
