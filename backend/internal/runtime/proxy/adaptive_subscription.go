package proxy

import (
	"fmt"
	"sort"
	"strings"
)

const AdaptiveSubscriptionStrategy = "adaptive_topn_weighted"

const adaptiveHashSeed = "0x9e3779b9"

type AdaptiveNodeLane struct {
	NodeID       string
	InboundTag   string
	OutboundTag  string
	ListenerPort int
	Weight       int
}

type AdaptiveSubscriptionRuntime struct {
	Inbounds    []XrayInbound
	Outbounds   []XrayOutbound
	Routing     XrayRouting
	Observatory XrayObservatory
	Metrics     *XrayMetrics
	Lanes       []AdaptiveNodeLane
	Candidates  []AdaptiveNodeLane
}

func BuildAdaptiveCapturePlan(plan NftablesCapturePlan, lanes []AdaptiveNodeLane) (NftablesCapturePlan, error) {
	if len(lanes) == 0 {
		return NftablesCapturePlan{}, fmt.Errorf("%w: adaptive capture requires at least one node lane", ErrInvalidSubscription)
	}
	total := 0
	for _, lane := range lanes {
		if lane.ListenerPort < 1 || lane.ListenerPort > 65535 || lane.Weight < 1 {
			return NftablesCapturePlan{}, fmt.Errorf("%w: adaptive node lane %q is invalid", ErrInvalidSubscription, lane.NodeID)
		}
		total += lane.Weight
	}
	if total < 1 || total > 256 {
		return NftablesCapturePlan{}, fmt.Errorf("%w: adaptive hash bucket count %d is unsupported", ErrInvalidSubscription, total)
	}
	chain := "proxy_prerouting"
	if len(plan.Chains) > 0 && strings.TrimSpace(plan.Chains[0].Name) != "" {
		chain = plan.Chains[0].Name
	}
	rules := []NftablesRule{{
		Order: 1, EgressID: plan.EgressID, Chain: chain,
		Expression: fmt.Sprintf("iifname %q meta mark %s", plan.IngressInterface, plan.InboundMark), Action: "return",
	}}
	order := 2
	cumulative := 0
	for _, lane := range lanes {
		cumulative += lane.Weight
		for _, protocol := range []string{"tcp", "udp"} {
			expression := fmt.Sprintf("iifname %q meta l4proto %s", plan.IngressInterface, protocol)
			if cumulative < total {
				ports := protocol + " sport . " + protocol + " dport"
				expression += fmt.Sprintf(" jhash ip saddr . ip daddr . meta l4proto . %s mod %d seed %s < %d", ports, total, adaptiveHashSeed, cumulative)
			}
			rules = append(rules, NftablesRule{
				Order: order, EgressID: plan.EgressID, Chain: chain, Expression: expression,
				Action: fmt.Sprintf("counter meta mark set %s tproxy ip to :%d accept", plan.InboundMark, lane.ListenerPort),
			})
			order++
		}
	}
	plan.TargetPort = lanes[0].ListenerPort
	plan.Rules = rules
	return plan, nil
}

func CompileAdaptiveSubscriptionLanes(subscription Subscription, nodes []Node, probes []NodeProbe, listenerPort int) (AdaptiveSubscriptionRuntime, error) {
	if strings.TrimSpace(subscription.ID) == "" || strings.TrimSpace(subscription.URL) == "" || !subscription.Enabled {
		return AdaptiveSubscriptionRuntime{}, fmt.Errorf("%w: enabled subscription with id and URL is required", ErrInvalidSubscription)
	}
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			return AdaptiveSubscriptionRuntime{}, fmt.Errorf("%w: node ID is required", ErrInvalidSubscription)
		}
		if _, exists := byID[id]; exists {
			return AdaptiveSubscriptionRuntime{}, fmt.Errorf("%w: duplicate node %q", ErrInvalidSubscription, id)
		}
		byID[id] = node
	}
	candidateIDs := subscriptionCandidateIDs(subscription, byID)
	subscription.NodeRefs = candidateIDs
	if subscription.TopN < 1 {
		subscription.TopN = 2
	}
	plan, err := BuildRTTWeightedTopNPlan(subscription, nodes, probes)
	if err != nil {
		return AdaptiveSubscriptionRuntime{}, err
	}
	if listenerPort < 1 || listenerPort > 65520 || listenerPort+len(candidateIDs) > 65535 {
		return AdaptiveSubscriptionRuntime{}, fmt.Errorf("%w: listener port range is invalid", ErrInvalidSubscription)
	}
	prefix := "subscription-" + strings.TrimSpace(subscription.ID) + "-node-"
	sort.Strings(candidateIDs)
	selected := make(map[string]int, len(plan.Nodes))
	for _, item := range plan.Nodes {
		selected[item.ID] = item.Weight
	}
	outbounds := make([]XrayOutbound, 0, len(candidateIDs))
	inbounds := make([]XrayInbound, 0, len(candidateIDs))
	rules := make([]XrayRoutingRule, 0, len(candidateIDs))
	lanesByID := make(map[string]AdaptiveNodeLane, len(plan.Nodes))
	candidateLanes := make([]AdaptiveNodeLane, 0, len(candidateIDs))
	for index, nodeID := range candidateIDs {
		outbound, err := CompileNodeOutbound(byID[nodeID])
		if err != nil {
			return AdaptiveSubscriptionRuntime{}, fmt.Errorf("%w: compile adaptive node %q: %v", ErrInvalidSubscription, nodeID, err)
		}
		outbound.Tag = prefix + nodeID
		outbounds = append(outbounds, outbound)
		laneTag := prefix + nodeID + "-inbound"
		port := listenerPort + index
		inbounds = append(inbounds, XrayInbound{
			Tag: laneTag, Listen: "0.0.0.0", Port: port, Protocol: "dokodemo-door",
			Settings:       XrayDokodemoSettings{Network: "tcp,udp", FollowRedirect: true},
			StreamSettings: map[string]any{"sockopt": map[string]any{"tproxy": "tproxy"}},
		})
		rules = append(rules, XrayRoutingRule{Type: "field", InboundTags: []string{laneTag}, OutboundTag: outbound.Tag})
		lane := AdaptiveNodeLane{NodeID: nodeID, InboundTag: laneTag, OutboundTag: outbound.Tag, ListenerPort: port, Weight: selected[nodeID]}
		candidateLanes = append(candidateLanes, lane)
		lanesByID[nodeID] = lane
	}
	lanes := make([]AdaptiveNodeLane, 0, len(plan.Nodes))
	for _, node := range plan.Nodes {
		lane := lanesByID[node.ID]
		if lane.Weight > 0 {
			lanes = append(lanes, lane)
		}
	}
	return AdaptiveSubscriptionRuntime{
		Inbounds: inbounds, Outbounds: outbounds,
		Routing:     XrayRouting{Rules: rules},
		Observatory: XrayObservatory{SubjectSelector: []string{prefix}, ProbeURL: "https://www.gstatic.com/generate_204", ProbeInterval: "10s", EnableConcurrency: true},
		Metrics:     &XrayMetrics{Listen: "127.0.0.1:11111"},
		Lanes:       lanes,
		Candidates:  candidateLanes,
	}, nil
}

func subscriptionCandidateIDs(subscription Subscription, nodes map[string]Node) []string {
	if len(subscription.NodeRefs) > 0 {
		return append([]string(nil), subscription.NodeRefs...)
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
