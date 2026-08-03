package apply

import (
	"context"
	"errors"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func TestGatewayACLQoSLiveFailureKeepsPriorStoreGeneration(t *testing.T) {
	// Given
	ctx := context.Background()
	store := openStore(t, ctx)
	seedSnapshot(t, ctx, store, "snapshot-acl-qos-live-prior", "txn-acl-qos-live-prior")
	applyErr := errors.New("live ACL command failed")
	client := &aclQoSGenerationClient{errors: map[string]error{"vpp.security-acl": applyErr}}
	prior := vpp.Snapshot{ACLs: []trafficpolicy.SecurityACL{{ID: "acl-prior", Action: "permit"}}, QoS: []flow.VPPObjectGroup{{Kind: "vpp.qos.store", Objects: []flow.VPPObject{{RuleID: "prior"}}}}}
	executor := Executor{
		Store: store,
		Now:   deterministicClock(time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)),
		Apply: func(ctx context.Context, plan Plan) error {
			_, err := (vpp.Adapter{Client: client}).ApplyACLQoS(ctx, vpp.ACLQoSPlan{
				TransactionID: plan.Request.TransactionID,
				ACLs:          []trafficpolicy.SecurityACL{{ID: "acl-new", Action: "deny"}},
				QoS:           prior.QoS,
			}, prior)
			return err
		},
		Rollback: func(context.Context, Plan) error { return nil },
	}
	request := validRequest("txn-acl-qos-live-failure")
	request.Resource = "gateway.acl-qos"
	request.SnapshotID = "snapshot-acl-qos-live-failed"
	request.PreviousSnapshotID = "snapshot-acl-qos-live-prior"

	// When
	result, err := executor.Run(ctx, request)

	// Then
	var lifecycleErr *vpp.ACLQoSLifecycleError
	if err == nil || !errors.As(err, &lifecycleErr) || !errors.Is(err, applyErr) {
		t.Fatalf("result=%#v err=%v, want live ACL failure", result, err)
	}
	snapshots, err := store.RuntimeSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != "snapshot-acl-qos-live-prior" {
		t.Fatalf("committed generations = %#v, want unchanged prior generation", snapshots)
	}
}

type aclQoSGenerationClient struct{ errors map[string]error }

func (client *aclQoSGenerationClient) OpenChannel(context.Context) (vpp.Channel, error) {
	return &aclQoSGenerationChannel{client: client}, nil
}

type aclQoSGenerationChannel struct{ client *aclQoSGenerationClient }

func (channel *aclQoSGenerationChannel) Do(_ context.Context, operation vpp.Operation) (vpp.Reply, error) {
	if err := channel.client.errors[operation.Name]; err != nil {
		return vpp.Reply{}, err
	}
	return vpp.Reply{}, nil
}

func (channel *aclQoSGenerationChannel) Close() error { return nil }
