package proxy

import (
	"strings"
	"testing"
	"time"
)

func TestBuildRTTWeightedTopNPlanUsesInverseLatencyAndStableBuckets(t *testing.T) {
	now := time.Now()
	nodes := []Node{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	plan, err := BuildRTTWeightedTopNPlan(Subscription{ID: "sub", NodeRefs: []string{"a", "b", "c"}, TopN: 2}, nodes, []NodeProbe{
		{NodeID: "a", Reachable: true, RTT: 10 * time.Millisecond, ObservedAt: now},
		{NodeID: "b", Reachable: true, RTT: 20 * time.Millisecond, ObservedAt: now},
		{NodeID: "c", Reachable: true, RTT: 30 * time.Millisecond, ObservedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Nodes) != 2 || plan.Nodes[0].ID != "a" || plan.Nodes[1].ID != "b" {
		t.Fatalf("top-n nodes = %#v", plan.Nodes)
	}
	if plan.Nodes[0].Weight != 67 || plan.Nodes[1].Weight != 33 || plan.TotalWeight != plan.Nodes[0].Weight+plan.Nodes[1].Weight {
		t.Fatalf("weights = %#v total=%d", plan.Nodes, plan.TotalWeight)
	}
	if got := plan.NodeForHash(0); got != "a" || plan.NodeForHash(uint64(plan.Nodes[0].Weight)) != "b" {
		t.Fatalf("bucket mapping is not stable: %q %q", got, plan.NodeForHash(uint64(plan.Nodes[0].Weight)))
	}
}

func TestBuildAdaptiveCapturePlanUsesWeightedFiveTupleRules(t *testing.T) {
	plan, err := BuildAdaptiveCapturePlan(NftablesCapturePlan{
		EgressID: "sub", IngressInterface: "proxy-in", InboundMark: "0x101",
	}, []AdaptiveNodeLane{
		{NodeID: "a", ListenerPort: 21001, Weight: 2},
		{NodeID: "b", ListenerPort: 21002, Weight: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetPort != 21001 || len(plan.Rules) != 5 {
		t.Fatalf("capture plan = %#v", plan)
	}
	if !strings.Contains(plan.Rules[1].Expression, "jhash ip saddr . ip daddr . meta l4proto . tcp sport . tcp dport mod 3 seed 0x9e3779b9 < 2") {
		t.Fatalf("missing TCP five-tuple hash: %#v", plan.Rules[1])
	}
	if !strings.Contains(plan.Rules[2].Expression, "meta l4proto . udp sport . udp dport mod 3 seed 0x9e3779b9 < 2") {
		t.Fatalf("missing UDP five-tuple hash: %#v", plan.Rules[2])
	}
	if strings.Contains(plan.Rules[3].Expression, "jhash") || plan.Rules[3].Expression != `iifname "proxy-in" meta l4proto tcp` {
		t.Fatalf("second lane does not cover the remaining hash range: %#v", plan.Rules[3])
	}
	if !strings.Contains(plan.Rules[len(plan.Rules)-1].Action, "counter meta mark") || !strings.Contains(plan.Rules[len(plan.Rules)-1].Action, "tproxy ip to :21002") {
		t.Fatalf("last bucket does not select node b: %#v", plan.Rules[len(plan.Rules)-1])
	}
}

func TestBuildRTTWeightedTopNPlanRejectsUnsupportedConfiguredTopN(t *testing.T) {
	now := time.Now()
	nodes := []Node{{ID: "a"}, {ID: "b"}}
	probes := []NodeProbe{{NodeID: "a", Reachable: true, RTT: time.Millisecond, ObservedAt: now}, {NodeID: "b", Reachable: true, RTT: 2 * time.Millisecond, ObservedAt: now}}
	for _, topN := range []int{1, 4, 9} {
		if _, err := BuildRTTWeightedTopNPlan(Subscription{ID: "sub", TopN: topN}, nodes, probes); err == nil {
			t.Fatalf("top_n %d was accepted", topN)
		}
	}
}

func TestBuildRTTWeightedTopNPlanAcceptsTopThree(t *testing.T) {
	now := time.Now()
	plan, err := BuildRTTWeightedTopNPlan(Subscription{ID: "sub", TopN: 3}, []Node{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}, []NodeProbe{
		{NodeID: "a", Reachable: true, RTT: 10 * time.Millisecond, ObservedAt: now},
		{NodeID: "b", Reachable: true, RTT: 20 * time.Millisecond, ObservedAt: now},
		{NodeID: "c", Reachable: true, RTT: 40 * time.Millisecond, ObservedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Nodes) != 3 || plan.Nodes[0].ID != "a" || plan.Nodes[1].ID != "b" || plan.Nodes[2].ID != "c" {
		t.Fatalf("top-three nodes = %#v", plan.Nodes)
	}
	if plan.TotalWeight != 100 {
		t.Fatalf("total weight = %d, want 100", plan.TotalWeight)
	}
}

func TestBuildRTTWeightedTopNPlanDropsFailedNodeAndRebalances(t *testing.T) {
	now := time.Now()
	plan, err := BuildRTTWeightedTopNPlan(Subscription{ID: "sub", NodeRefs: []string{"a", "b", "c"}, TopN: 2}, []Node{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}, []NodeProbe{
		{NodeID: "a", Reachable: false, RTT: 10 * time.Millisecond, ObservedAt: now},
		{NodeID: "b", Reachable: true, RTT: 20 * time.Millisecond, ObservedAt: now},
		{NodeID: "c", Reachable: true, RTT: 40 * time.Millisecond, ObservedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Nodes) != 2 || plan.Nodes[0].ID != "b" || plan.Nodes[1].ID != "c" {
		t.Fatalf("healthy top-n nodes = %#v", plan.Nodes)
	}
	if plan.Nodes[0].Weight != 67 || plan.Nodes[1].Weight != 33 || plan.TotalWeight != 100 {
		t.Fatalf("rebalanced weights = %#v total=%d", plan.Nodes, plan.TotalWeight)
	}
}

func TestBuildRTTWeightedTopNPlanFallsBackToOneHealthyNode(t *testing.T) {
	now := time.Now()
	plan, err := BuildRTTWeightedTopNPlan(Subscription{ID: "sub", NodeRefs: []string{"a", "b"}, TopN: 2}, []Node{{ID: "a"}, {ID: "b"}}, []NodeProbe{
		{NodeID: "a", Reachable: true, RTT: 10 * time.Millisecond, ObservedAt: now},
		{NodeID: "b", Reachable: false, RTT: 20 * time.Millisecond, ObservedAt: now},
	})
	if err != nil || len(plan.Nodes) != 1 || plan.Nodes[0].ID != "a" || plan.Nodes[0].Weight != 100 || plan.TotalWeight != 100 {
		t.Fatalf("adaptive plan did not fail over to one healthy node: plan=%#v err=%v", plan, err)
	}
}

func TestCompileAdaptiveSubscriptionLanesPinsEachNodeToItsOwnInbound(t *testing.T) {
	now := time.Now()
	runtime, err := CompileAdaptiveSubscriptionLanes(Subscription{ID: "sub", URL: "https://provider.invalid/sub", Enabled: true, NodeRefs: []string{"a", "b"}, TopN: 2}, []Node{
		{ID: "a", Protocol: "vless", Address: "127.0.0.1", Port: 443, Secret: "11111111-1111-1111-1111-111111111111"},
		{ID: "b", Protocol: "vless", Address: "127.0.0.2", Port: 443, Secret: "11111111-1111-1111-1111-111111111111"},
	}, []NodeProbe{
		{NodeID: "a", Reachable: true, RTT: 10 * time.Millisecond, ObservedAt: now},
		{NodeID: "b", Reachable: true, RTT: 20 * time.Millisecond, ObservedAt: now},
	}, 21000)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Inbounds) != 2 || len(runtime.Outbounds) != 2 || len(runtime.Routing.Rules) != 2 || len(runtime.Routing.Balancers) != 0 {
		t.Fatalf("adaptive lanes = %#v", runtime)
	}
	if runtime.Lanes[0].ListenerPort != 21000 || runtime.Lanes[1].ListenerPort != 21001 || runtime.Lanes[0].Weight != 67 || runtime.Lanes[1].Weight != 33 {
		t.Fatalf("adaptive lane weights = %#v", runtime.Lanes)
	}
	if runtime.Routing.Rules[0].OutboundTag != "subscription-sub-node-a" || runtime.Routing.Rules[1].OutboundTag != "subscription-sub-node-b" {
		t.Fatalf("adaptive routes = %#v", runtime.Routing.Rules)
	}
	if runtime.Metrics == nil || runtime.Metrics.Listen != "127.0.0.1:11111" {
		t.Fatalf("adaptive observatory metrics = %#v", runtime.Metrics)
	}
}

func TestCompileAdaptiveSubscriptionLanesKeepsStableCandidatePorts(t *testing.T) {
	now := time.Now()
	runtime, err := CompileAdaptiveSubscriptionLanes(Subscription{ID: "sub", URL: "https://provider.invalid/sub", Enabled: true, NodeRefs: []string{"c", "a", "b"}, TopN: 2}, []Node{
		{ID: "c", Protocol: "vless", Address: "127.0.0.3", Port: 443, Secret: "11111111-1111-1111-1111-111111111111"},
		{ID: "a", Protocol: "vless", Address: "127.0.0.1", Port: 443, Secret: "11111111-1111-1111-1111-111111111111"},
		{ID: "b", Protocol: "vless", Address: "127.0.0.2", Port: 443, Secret: "11111111-1111-1111-1111-111111111111"},
	}, []NodeProbe{
		{NodeID: "a", Reachable: true, RTT: 10 * time.Millisecond, ObservedAt: now},
		{NodeID: "b", Reachable: true, RTT: 20 * time.Millisecond, ObservedAt: now},
		{NodeID: "c", Reachable: true, RTT: 30 * time.Millisecond, ObservedAt: now},
	}, 21000)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Candidates) != 3 || runtime.Candidates[0].NodeID != "a" || runtime.Candidates[0].ListenerPort != 21000 || runtime.Candidates[2].NodeID != "c" || runtime.Candidates[2].ListenerPort != 21002 {
		t.Fatalf("candidate lanes = %#v", runtime.Candidates)
	}
	if len(runtime.Lanes) != 2 || runtime.Lanes[0].NodeID != "a" || runtime.Lanes[1].NodeID != "b" {
		t.Fatalf("selected lanes = %#v", runtime.Lanes)
	}
}

func TestCompileAdaptiveSubscriptionLanesKeepsRTTOrderForHashBuckets(t *testing.T) {
	now := time.Now()
	runtime, err := CompileAdaptiveSubscriptionLanes(Subscription{
		ID: "sub", URL: "https://provider.invalid/sub", Enabled: true,
		NodeRefs: []string{"slow", "fast"}, TopN: 2,
	}, []Node{
		{ID: "slow", Protocol: "vless", Address: "127.0.0.2", Port: 443, Secret: "11111111-1111-1111-1111-111111111111"},
		{ID: "fast", Protocol: "vless", Address: "127.0.0.1", Port: 443, Secret: "11111111-1111-1111-1111-111111111111"},
	}, []NodeProbe{
		{NodeID: "slow", Reachable: true, RTT: 20 * time.Millisecond, ObservedAt: now},
		{NodeID: "fast", Reachable: true, RTT: 10 * time.Millisecond, ObservedAt: now},
	}, 21000)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Lanes) != 2 || runtime.Lanes[0].NodeID != "fast" || runtime.Lanes[1].NodeID != "slow" {
		t.Fatalf("hash lane order = %#v", runtime.Lanes)
	}
	if runtime.Lanes[0].Weight != 67 || runtime.Lanes[1].Weight != 33 {
		t.Fatalf("hash lane weights = %#v", runtime.Lanes)
	}
}
