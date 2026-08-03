package gateway

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseSecurityGuardRuntimeRequiresExactReadback(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	rules, err := parseSecurityGuardRuntime("rule guard-a enabled 1 family 4 interface wan0 threshold-pps 100 burst-packets 20 matched 30 conform 20 exceed 10 alerts 2 drops 8\n", now)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules=%#v err=%v", rules, err)
	}
	rule := rules[0]
	if rule.ID != "guard-a" || !rule.Enabled || rule.Family != 4 || rule.ThresholdPPS != 100 || rule.BurstPackets != 20 || rule.Matched != 30 || rule.Alerts != 2 || rule.Dropped != 8 || !rule.ObservedAt.Equal(now) {
		t.Fatalf("rule=%#v", rule)
	}
	for _, bad := range []string{"rule guard-a enabled 1 family 5 interface wan0 threshold-pps 100 burst-packets 20 matched 30 conform 20 exceed 10 alerts 2 drops 8", "rule guard-a enabled 1 family 4 interface wan0 threshold-pps bad burst-packets 20 matched 30 conform 20 exceed 10 alerts 2 drops 8", "unexpected"} {
		if _, err := parseSecurityGuardRuntime(bad, now); err == nil {
			t.Fatalf("invalid readback accepted: %q", bad)
		}
	}
}

func TestProductionSecurityGuardObserverUsesVPPReadback(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	observer := productionSecurityGuardObserver{binary: "fake-vppctl", now: func() time.Time { return now }, run: func(_ context.Context, binary string, args ...string) (string, error) {
		if binary != "fake-vppctl" || strings.Join(args, " ") != "show ly-route security-guard" {
			t.Fatalf("VPP command = %s %s", binary, strings.Join(args, " "))
		}
		return "rule guard-a enabled 1 family 6 interface wan0 threshold-pps 100 burst-packets 20 matched 30 conform 20 exceed 10 alerts 2 drops 8\n", nil
	}}
	rules, err := observer.SecurityGuardRules(context.Background())
	if err != nil || len(rules) != 1 || rules[0].Family != 6 {
		t.Fatalf("rules=%#v err=%v", rules, err)
	}
}
