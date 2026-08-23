package httpapi

import (
	"testing"
	"time"

	"ly-route/backend/internal/runtime/proxy"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestAdaptiveLanesUseTopNInverseRTTAndRemoveDeadNodes(t *testing.T) {
	subscription := proxy.Subscription{ID: "sub", URL: "https://subscription.invalid", Enabled: true, NodeRefs: []string{"c", "a", "b"}, TopN: 2}
	now := time.Now().UTC()
	states := []serviceRuntime.XrayObservatoryState{
		{OutboundTag: "subscription-sub-node-a", Alive: true, DelayMilliseconds: 10},
		{OutboundTag: "subscription-sub-node-b", Alive: true, DelayMilliseconds: 20},
		{OutboundTag: "subscription-sub-node-c", Alive: true, DelayMilliseconds: 100},
	}

	lanes, err := adaptiveLanesFromObservations(subscription, states, 25656, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 2 || lanes[0].NodeID != "a" || lanes[0].ListenerPort != 25656 || lanes[0].Weight != 67 || lanes[1].NodeID != "b" || lanes[1].ListenerPort != 25657 || lanes[1].Weight != 33 {
		t.Fatalf("initial adaptive lanes = %#v", lanes)
	}

	states[0].Alive = false
	lanes, err = adaptiveLanesFromObservations(subscription, states, 25656, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 2 || lanes[0].NodeID != "b" || lanes[0].ListenerPort != 25657 || lanes[1].NodeID != "c" || lanes[1].ListenerPort != 25658 {
		t.Fatalf("failover adaptive lanes = %#v", lanes)
	}
}

func TestAdaptiveLanesFromObservationsOrdersHashBucketsByRTT(t *testing.T) {
	lanes, err := adaptiveLanesFromObservations(proxy.Subscription{
		ID: "sub", NodeRefs: []string{"slow", "fast"}, TopN: 2,
	}, []serviceRuntime.XrayObservatoryState{
		{OutboundTag: "subscription-sub-node-slow", Alive: true, DelayMilliseconds: 20},
		{OutboundTag: "subscription-sub-node-fast", Alive: true, DelayMilliseconds: 10},
	}, 21000, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 2 || lanes[0].NodeID != "fast" || lanes[1].NodeID != "slow" || lanes[0].Weight != 67 || lanes[1].Weight != 33 {
		t.Fatalf("reconciled lanes = %#v", lanes)
	}
}
