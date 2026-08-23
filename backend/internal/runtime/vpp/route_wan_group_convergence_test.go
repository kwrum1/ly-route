package vpp

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRetryRouteWANGroupSnapshotReturnsConvergedReadback(t *testing.T) {
	calls := 0
	want := Snapshot{RoutePolicies: nil, WANGroups: nil}

	got, err := retryRouteWANGroupSnapshot(context.Background(), 3, 0, func() (Snapshot, error) {
		calls++
		if calls < 3 {
			return Snapshot{}, fmt.Errorf("%w: FIB path is still converging", ErrSnapshotIncomplete)
		}
		return want, nil
	})

	if err != nil || calls != 3 || len(got.RoutePolicies) != 0 || len(got.WANGroups) != 0 {
		t.Fatalf("retry result = %#v, %v, calls=%d", got, err, calls)
	}
}

func TestRetryRouteWANGroupSnapshotDoesNotRetryPermanentError(t *testing.T) {
	calls := 0
	wantErr := fmt.Errorf("permanent channel failure")

	_, err := retryRouteWANGroupSnapshot(context.Background(), 5, 0, func() (Snapshot, error) {
		calls++
		return Snapshot{}, wantErr
	})

	if !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("retry error = %v, calls=%d", err, calls)
	}
}
