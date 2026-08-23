package proxy

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type RTTWeightedNode struct {
	ID     string
	RTT    time.Duration
	Weight int
}

type RTTWeightedTopNPlan struct {
	Nodes       []RTTWeightedNode
	TotalWeight int
}

func BuildRTTWeightedTopNPlan(subscription Subscription, nodes []Node, probes []NodeProbe) (RTTWeightedTopNPlan, error) {
	if strings.TrimSpace(subscription.ID) == "" {
		return RTTWeightedTopNPlan{}, fmt.Errorf("%w: subscription ID is required", ErrInvalidSubscription)
	}
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			return RTTWeightedTopNPlan{}, fmt.Errorf("%w: node ID is required", ErrInvalidSubscription)
		}
		if _, exists := byID[id]; exists {
			return RTTWeightedTopNPlan{}, fmt.Errorf("%w: duplicate node %q", ErrInvalidSubscription, id)
		}
		byID[id] = node
	}
	probeByID := make(map[string]NodeProbe, len(probes))
	for _, probe := range probes {
		probeByID[strings.TrimSpace(probe.NodeID)] = probe
	}
	ids := subscriptionCandidateIDs(subscription, byID)
	ordered := make([]RTTWeightedNode, 0, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if _, exists := byID[id]; !exists {
			return RTTWeightedTopNPlan{}, fmt.Errorf("%w: node %q is missing", ErrInvalidSubscription, id)
		}
		probe, exists := probeByID[id]
		if !exists || !probe.Reachable || probe.RTT <= 0 || probe.ObservedAt.IsZero() || time.Since(probe.ObservedAt) > ProbeMaxAge || time.Until(probe.ObservedAt) > ProbeMaxAge {
			continue
		}
		ordered = append(ordered, RTTWeightedNode{ID: id, RTT: probe.RTT})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].RTT != ordered[j].RTT {
			return ordered[i].RTT < ordered[j].RTT
		}
		return ordered[i].ID < ordered[j].ID
	})
	if len(ordered) == 0 {
		return RTTWeightedTopNPlan{}, fmt.Errorf("%w: no healthy nodes are available", ErrInvalidSubscription)
	}
	topN := subscription.TopN
	if topN == 0 {
		topN = 2
	}
	if topN != 2 && topN != 3 {
		return RTTWeightedTopNPlan{}, fmt.Errorf("%w: top_n must be 2 or 3", ErrInvalidSubscription)
	}
	if topN > len(ordered) {
		topN = len(ordered)
	}
	ordered = ordered[:topN]
	plan := RTTWeightedTopNPlan{Nodes: ordered}
	assignInverseRTTWeights(&plan, 100)
	return plan, nil
}

func (plan RTTWeightedTopNPlan) NodeForHash(hash uint64) string {
	if plan.TotalWeight <= 0 {
		return ""
	}
	bucket := int(hash % uint64(plan.TotalWeight))
	for _, node := range plan.Nodes {
		if bucket < node.Weight {
			return node.ID
		}
		bucket -= node.Weight
	}
	return plan.Nodes[len(plan.Nodes)-1].ID
}

func assignInverseRTTWeights(plan *RTTWeightedTopNPlan, buckets int) {
	type remainder struct {
		index int
		value float64
	}
	inverseTotal := 0.0
	for _, node := range plan.Nodes {
		inverseTotal += 1 / float64(node.RTT)
	}
	remainders := make([]remainder, len(plan.Nodes))
	assigned := 0
	for index := range plan.Nodes {
		exact := (1 / float64(plan.Nodes[index].RTT)) / inverseTotal * float64(buckets)
		weight := int(math.Floor(exact))
		if weight < 1 {
			weight = 1
		}
		plan.Nodes[index].Weight = weight
		assigned += weight
		remainders[index] = remainder{index: index, value: exact - math.Floor(exact)}
	}
	sort.SliceStable(remainders, func(i, j int) bool {
		if remainders[i].value != remainders[j].value {
			return remainders[i].value > remainders[j].value
		}
		return remainders[i].index < remainders[j].index
	})
	for offset := 0; assigned < buckets; offset++ {
		plan.Nodes[remainders[offset%len(remainders)].index].Weight++
		assigned++
	}
	cursor := len(remainders) - 1
	for assigned > buckets {
		index := remainders[cursor].index
		if plan.Nodes[index].Weight > 1 {
			plan.Nodes[index].Weight--
			assigned--
		}
		cursor--
		if cursor < 0 {
			cursor = len(remainders) - 1
		}
	}
	plan.TotalWeight = assigned
}
