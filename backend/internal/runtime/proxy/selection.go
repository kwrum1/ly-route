package proxy

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type SelectionMode string

const (
	SelectionFixed   SelectionMode = "fixed"
	SelectionFastest SelectionMode = "fastest"
	// Fastest selection must use a fresh probe; stale health must never keep a dead node active.
	ProbeMaxAge = 15 * time.Second
)

type NodeProbe struct {
	NodeID     string        `json:"node_id"`
	Reachable  bool          `json:"reachable"`
	RTT        time.Duration `json:"rtt"`
	ObservedAt time.Time     `json:"observed_at"`
}

func SelectSubscriptionNode(subscription Subscription, nodes []Node, probes []NodeProbe) (Node, error) {
	if strings.TrimSpace(subscription.ID) == "" {
		return Node{}, fmt.Errorf("%w: subscription ID is required", ErrInvalidSubscription)
	}
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			return Node{}, fmt.Errorf("%w: node ID is required", ErrInvalidSubscription)
		}
		if _, exists := byID[id]; exists {
			return Node{}, fmt.Errorf("%w: duplicate node %q", ErrInvalidSubscription, id)
		}
		byID[id] = node
	}
	candidateIDs := append([]string(nil), subscription.NodeRefs...)
	if len(candidateIDs) == 0 {
		for id := range byID {
			candidateIDs = append(candidateIDs, id)
		}
		sort.Strings(candidateIDs)
	}
	if len(candidateIDs) == 0 {
		return Node{}, fmt.Errorf("%w: subscription %q has no candidate nodes", ErrInvalidSubscription, subscription.ID)
	}
	mode := SelectionMode(strings.TrimSpace(string(subscription.Selection)))
	if mode == "" {
		mode = SelectionFixed
	}
	switch mode {
	case SelectionFixed:
		node, exists := byID[strings.TrimSpace(candidateIDs[0])]
		if !exists {
			return Node{}, fmt.Errorf("%w: active node %q is missing", ErrInvalidSubscription, candidateIDs[0])
		}
		return node, nil
	case SelectionFastest:
		probeByID := make(map[string]NodeProbe, len(probes))
		for _, probe := range probes {
			probeByID[strings.TrimSpace(probe.NodeID)] = probe
		}
		var selected Node
		selectedRTT := time.Duration(1<<63 - 1)
		selectedID := ""
		for _, rawID := range candidateIDs {
			id := strings.TrimSpace(rawID)
			node, exists := byID[id]
			if !exists {
				return Node{}, fmt.Errorf("%w: fastest node %q is missing", ErrInvalidSubscription, id)
			}
			probe, exists := probeByID[id]
			if !exists || !probe.Reachable || probe.RTT < 0 || probe.ObservedAt.IsZero() || time.Since(probe.ObservedAt) > ProbeMaxAge || time.Until(probe.ObservedAt) > ProbeMaxAge {
				continue
			}
			if selectedID == "" || probe.RTT < selectedRTT || probe.RTT == selectedRTT && id < selectedID {
				selected, selectedRTT, selectedID = node, probe.RTT, id
			}
		}
		if selectedID == "" {
			return Node{}, fmt.Errorf("%w: no healthy fastest node is available", ErrInvalidSubscription)
		}
		return selected, nil
	default:
		return Node{}, fmt.Errorf("%w: unsupported selection mode %q", ErrInvalidSubscription, mode)
	}
}
