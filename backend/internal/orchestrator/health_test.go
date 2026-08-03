package orchestrator

import (
	"context"
	"testing"
	"time"
)

type mapHealthProbe map[string]bool

func (probe mapHealthProbe) Reachable(_ context.Context, address string) bool { return probe[address] }

func TestHealthTrackerBypassesImmediatelyAndRequiresConsecutiveRecovery(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	tracker := NewHealthTracker(3, func() time.Time { return now })
	bindings := []ServiceChainBindingInput{{Group: "inline-a", WANFacingNextHop: "198.18.1.2", LANFacingNextHop: "198.18.2.2"}}
	healthy := mapHealthProbe{"198.18.1.2": true, "198.18.2.2": true}
	failed := mapHealthProbe{"198.18.1.2": true, "198.18.2.2": false}

	unavailable, reports := tracker.Evaluate(context.Background(), bindings, healthy)
	if unavailable["inline-a"] || reports[0].Unavailable {
		t.Fatalf("initial healthy report = %#v", reports)
	}
	unavailable, reports = tracker.Evaluate(context.Background(), bindings, failed)
	if !unavailable["inline-a"] || !reports[0].Unavailable {
		t.Fatalf("failed arm did not trigger bypass: %#v", reports)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		unavailable, reports = tracker.Evaluate(context.Background(), bindings, healthy)
		if !unavailable["inline-a"] || reports[0].RecoverySuccesses != attempt {
			t.Fatalf("recovery attempt %d released group: %#v", attempt, reports)
		}
	}
	unavailable, reports = tracker.Evaluate(context.Background(), bindings, healthy)
	if unavailable["inline-a"] || reports[0].Unavailable || reports[0].RecoverySuccesses != 3 {
		t.Fatalf("third recovery proof did not rejoin group: %#v", reports)
	}
}

func TestHealthTrackerRequiresBothLogicalEndpoints(t *testing.T) {
	tracker := NewHealthTracker(3, time.Now)
	bindings := []ServiceChainBindingInput{{Group: "inline-a", WANFacingNextHop: "wan", LANFacingNextHop: "lan"}}
	for name, probe := range map[string]mapHealthProbe{"wan down": {"lan": true}, "lan down": {"wan": true}, "both down": {}} {
		t.Run(name, func(t *testing.T) {
			local := NewHealthTracker(3, time.Now)
			unavailable, _ := local.Evaluate(context.Background(), bindings, probe)
			if !unavailable["inline-a"] {
				t.Fatal("group remained available without bidirectional proof")
			}
		})
	}
	_ = tracker
}
