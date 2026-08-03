package apply

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGatewayRouteWANGroupFailuresDoNotAdvanceCommittedGeneration(t *testing.T) {
	for _, test := range []struct {
		name           string
		rollbackFailed bool
	}{
		{name: "apply failure"},
		{name: "rollback failure", rollbackFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			ctx := context.Background()
			store := openStore(t, ctx)
			seedSnapshot(t, ctx, store, "snapshot-route-wan-prior", "txn-route-wan-prior")
			executor := Executor{
				Store: store,
				Now:   deterministicClock(time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)),
				Apply: func(context.Context, Plan) error { return errors.New("route wan-group apply failed") },
				Rollback: func(context.Context, Plan) error {
					if test.rollbackFailed {
						return errors.New("route wan-group rollback failed")
					}
					return nil
				},
			}
			request := validRequest("txn-route-wan-failure")
			request.Resource = "gateway.route-wan-group"
			request.SnapshotID = "snapshot-route-wan-failed"
			request.PreviousSnapshotID = "snapshot-route-wan-prior"

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
			if len(snapshots) != 1 || snapshots[0].ID != "snapshot-route-wan-prior" {
				t.Fatalf("committed generations = %#v, want unchanged prior generation", snapshots)
			}
		})
	}
}
