package apply

import (
	"context"
	"errors"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/vpp"
)

func TestGatewayNAT44LiveFailureKeepsPriorStoreGeneration(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-nat-live-prior", "txn-nat-live-prior")
	applyErr := errors.New("live NAT command failed")
	client := &nat44GenerationClient{err: applyErr}
	prior := vpp.Snapshot{TransactionID: "txn-nat-live-prior", NAT: nat.CompiledConfig{PortMappings: []nat.PortMapping{{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 443}}}}
	executor := Executor{
		Store: store,
		Now:   deterministicClock(time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC)),
		Apply: func(ctx context.Context, plan Plan) error {
			_, err := (vpp.Adapter{Client: client}).ApplyNAT44(ctx, vpp.NAT44Plan{TransactionID: plan.Request.TransactionID, PortMappings: prior.NAT.PortMappings}, prior)
			return err
		},
		Rollback: func(context.Context, Plan) error { return nil },
	}
	request := validRequest("txn-nat-live-failure")
	request.Resource = "gateway.nat-port-map"
	request.SnapshotID = "snapshot-nat-live-failed"
	request.PreviousSnapshotID = "snapshot-nat-live-prior"

	// When
	result, err := executor.Run(ctx, request)

	// Then
	if err == nil || !errors.Is(err, applyErr) {
		t.Fatalf("result = %#v err = %v, want live NAT failure", result, err)
	}
	snapshots, err := store.RuntimeSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != "snapshot-nat-live-prior" {
		t.Fatalf("committed generations = %#v, want unchanged prior generation", snapshots)
	}
}

type nat44GenerationClient struct{ err error }

func (client *nat44GenerationClient) OpenChannel(context.Context) (vpp.Channel, error) {
	return &nat44GenerationChannel{client: client}, nil
}

type nat44GenerationChannel struct{ client *nat44GenerationClient }

func (channel *nat44GenerationChannel) Do(_ context.Context, operation vpp.Operation) (vpp.Reply, error) {
	if operation.Name == "vpp.nat44-ed.port-map" {
		return vpp.Reply{}, channel.client.err
	}
	return vpp.Reply{}, nil
}

func (channel *nat44GenerationChannel) Close() error { return nil }
