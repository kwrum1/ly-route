package apply

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGatewayInterfaceBondFailuresDoNotAdvanceCommittedGeneration(t *testing.T) {
	for _, test := range []struct {
		name           string
		rollbackFailed bool
	}{
		{name: "apply failure", rollbackFailed: false},
		{name: "rollback failure", rollbackFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			ctx := context.Background()
			store := openStore(t, ctx)
			seedSnapshot(t, ctx, store, "snapshot-interface-bond-prior", "txn-interface-bond-prior")
			executor := Executor{
				Store: store,
				Now:   deterministicClock(time.Date(2026, 6, 5, 20, 0, 0, 0, time.UTC)),
				Apply: func(context.Context, Plan) error { return errors.New("interface bond apply failed") },
				Rollback: func(context.Context, Plan) error {
					if test.rollbackFailed {
						return errors.New("interface bond rollback failed")
					}
					return nil
				},
			}
			request := validRequest("txn-interface-bond-failure")
			request.Resource = "gateway.interface-bond"
			request.SnapshotID = "snapshot-interface-bond-failed"
			request.PreviousSnapshotID = "snapshot-interface-bond-prior"

			// When
			result, err := executor.Run(ctx, request)

			// Then
			if err == nil || result.Rollback.TargetSnapshotID != request.PreviousSnapshotID {
				t.Fatalf("result=%#v err=%v, want failed transaction tied to prior snapshot", result, err)
			}
			snapshots, err := store.RuntimeSnapshots(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshots) != 1 || snapshots[0].ID != "snapshot-interface-bond-prior" || snapshots[0].PayloadHash == "" {
				t.Fatalf("committed generations = %#v, want unchanged prior generation", snapshots)
			}
		})
	}
}
