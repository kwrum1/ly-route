package apply

import (
	"context"
	"errors"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func TestGatewayRouteWANGroupLiveFailureKeepsPriorStoreGeneration(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-route-wan-live-prior", "txn-route-wan-live-prior")
	applyCommandErr := errors.New("route command failed")
	client := &routeWANStoreClient{
		errors: map[string]error{"vpp.route-policy": applyCommandErr},
		replies: map[string]vpp.Reply{
			"vpp.pbr.next-hop-group.snapshot": {Payload: vpp.WANGroupReadback{Groups: []trafficpolicy.WANGroup{{ID: "wan-primary", Members: []string{"wan0", "wan1"}}}}},
			"vpp.route-policy.snapshot":       {Payload: vpp.RoutePolicyReadback{Policies: []trafficpolicy.RoutePolicy{{ID: "route-office", Action: "route", Egress: "wan-primary"}}}},
		},
	}
	prior := vpp.Snapshot{
		TransactionID: "txn-route-wan-live-prior",
		RoutePolicies: []trafficpolicy.RoutePolicy{{ID: "route-office", Action: "route", Egress: "wan-primary"}},
		WANGroups:     []trafficpolicy.WANGroup{{ID: "wan-primary", Members: []string{"wan0", "wan1"}}},
	}
	executor := Executor{
		Store: store,
		Now:   deterministicClock(time.Date(2026, 7, 23, 13, 0, 0, 0, time.UTC)),
		Apply: func(ctx context.Context, plan Plan) error {
			_, err := (vpp.Adapter{Client: client}).ApplyRouteWANGroup(ctx, vpp.RouteWANGroupPlan{
				TransactionID: plan.Request.TransactionID,
				Routes:        prior.RoutePolicies,
				WANGroups:     prior.WANGroups,
			}, prior)
			return err
		},
		Rollback: func(context.Context, Plan) error { return nil },
	}
	request := validRequest("txn-route-wan-live-failure")
	request.Resource = "gateway.route-wan-group"
	request.SnapshotID = "snapshot-route-wan-live-failed"
	request.PreviousSnapshotID = "snapshot-route-wan-live-prior"

	// When
	result, err := executor.Run(ctx, request)

	// Then
	var lifecycleErr *vpp.RouteWANGroupLifecycleError
	if err == nil || !errors.As(err, &lifecycleErr) || !errors.Is(lifecycleErr, applyCommandErr) {
		t.Fatalf("result=%#v err=%v, want live VPP route failure", result, err)
	}
	snapshots, err := store.RuntimeSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != "snapshot-route-wan-live-prior" {
		t.Fatalf("committed generations = %#v, want unchanged prior generation", snapshots)
	}
}

type routeWANStoreClient struct {
	replies map[string]vpp.Reply
	errors  map[string]error
}

func (client *routeWANStoreClient) OpenChannel(context.Context) (vpp.Channel, error) {
	return &routeWANStoreChannel{client: client}, nil
}

type routeWANStoreChannel struct{ client *routeWANStoreClient }

func (channel *routeWANStoreChannel) Do(_ context.Context, operation vpp.Operation) (vpp.Reply, error) {
	if err := channel.client.errors[operation.Name]; err != nil {
		return vpp.Reply{}, err
	}
	return channel.client.replies[operation.Name], nil
}

func (channel *routeWANStoreChannel) Close() error { return nil }
