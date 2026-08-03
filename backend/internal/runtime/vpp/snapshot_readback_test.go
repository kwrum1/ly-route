package vpp

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdapterSnapshotReadsInterfaceAndBondStateForTransaction(t *testing.T) {
	// Given
	readbackAt := time.Date(2026, 7, 23, 10, 11, 12, 0, time.UTC)
	client := &snapshotClient{replies: map[string]Reply{
		"vpp.interface.snapshot": {Payload: InterfaceReadback{Interfaces: []InterfaceState{
			{Name: "eth0", AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.1/24"}},
			{Name: "lyroute-eth1", AdminState: "up", LinkState: "up", Addresses: []string{"192.168.88.1/24"}},
		}}},
		"vpp.interface-bond.snapshot": {Payload: BondReadback{Bonds: []BondState{{Name: "bond0", Mode: "active-backup", Members: []string{"lyroute-eth1", "lyroute-eth2"}}}}},
	}}

	// When
	snapshot, err := (Adapter{Client: client}).Snapshot(context.Background(), SnapshotRequest{
		TransactionID:       "txn-7b",
		ManagementInterface: "eth0",
		Capabilities:        []SnapshotCapability{SnapshotCapabilityInterfaces, SnapshotCapabilityBonds},
		ReadbackAt:          readbackAt,
	})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TransactionID != "txn-7b" || snapshot.RequestID != "txn-7b" || !snapshot.ReadbackAt.Equal(readbackAt) {
		t.Fatalf("snapshot identity = %#v", snapshot)
	}
	if len(snapshot.Interfaces) != 1 || snapshot.Interfaces[0].Name != "lyroute-eth1" {
		t.Fatalf("interfaces = %#v, want management interface excluded", snapshot.Interfaces)
	}
	if len(snapshot.Bonds) != 1 || snapshot.Bonds[0].Members[0] != "lyroute-eth1" {
		t.Fatalf("bonds = %#v", snapshot.Bonds)
	}
	if len(client.operations) != 2 || client.operations[0].RequestID != "txn-7b" || client.operations[1].RequestID != "txn-7b" {
		t.Fatalf("operations = %#v, want transaction-bound readback", client.operations)
	}
}

func TestAdapterSnapshotRejectsNonzeroReplyRetvalWithValidPayload(t *testing.T) {
	// Given
	client := &snapshotClient{replies: map[string]Reply{
		"vpp.interface.snapshot":      {Retval: -7, Payload: InterfaceReadback{Interfaces: []InterfaceState{{Name: "lyroute-eth1"}}}},
		"vpp.interface-bond.snapshot": {Payload: BondReadback{Bonds: []BondState{{Name: "bond0", Members: []string{"lyroute-eth1"}}}}},
	}}

	// When
	snapshot, err := (Adapter{Client: client}).Snapshot(context.Background(), SnapshotRequest{
		TransactionID: "txn-ret-7b",
		Capabilities:  []SnapshotCapability{SnapshotCapabilityInterfaces, SnapshotCapabilityBonds},
	})

	// Then
	if err == nil || snapshot.Hash != "" {
		t.Fatalf("snapshot = %#v, error = %v; want failed readback without snapshot success", snapshot, err)
	}
	var vppErr VPPError
	if !errors.As(err, &vppErr) || vppErr.Retval != -7 {
		t.Fatalf("error = %T %v, want VPPError retval -7", err, err)
	}
}

func TestAdapterSnapshotReturnsUnavailableWhenVPPCannotOpen(t *testing.T) {
	// Given
	client := &snapshotClient{openErr: errors.New("socket unavailable")}

	// When
	_, err := (Adapter{Client: client}).Snapshot(context.Background(), SnapshotRequest{TransactionID: "txn-down"})

	// Then
	if !errors.Is(err, ErrVPPUnavailable) {
		t.Fatalf("error = %v, want ErrVPPUnavailable", err)
	}
}

func TestAdapterSnapshotReturnsIncompleteWhenReadbackPayloadIsMissing(t *testing.T) {
	// Given
	client := &snapshotClient{replies: map[string]Reply{
		"vpp.interface.snapshot": {Payload: InterfaceReadback{}},
	}}

	// When
	_, err := (Adapter{Client: client}).Snapshot(context.Background(), SnapshotRequest{
		TransactionID: "txn-incomplete",
		Capabilities:  []SnapshotCapability{SnapshotCapabilityInterfaces},
	})

	// Then
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("error = %v, want ErrSnapshotIncomplete", err)
	}
}

type snapshotClient struct {
	openErr    error
	replies    map[string]Reply
	operations []Operation
}

func (client *snapshotClient) OpenChannel(context.Context) (Channel, error) {
	if client.openErr != nil {
		return nil, client.openErr
	}
	return &snapshotChannel{client: client}, nil
}

type snapshotChannel struct{ client *snapshotClient }

func (channel *snapshotChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	channel.client.operations = append(channel.client.operations, operation)
	return channel.client.replies[operation.Name], nil
}

func (channel *snapshotChannel) Close() error { return nil }
