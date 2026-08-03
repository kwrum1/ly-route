package proxy

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrInvalidSubscription = errors.New("invalid proxy subscription")

func CompileSubscription(subscription Subscription, nodes []Node) (XrayOutbound, error) {
	return CompileSubscriptionWithSelection(subscription, nodes, nil)
}

func CompileSubscriptionWithSelection(subscription Subscription, nodes []Node, probes []NodeProbe) (XrayOutbound, error) {
	if strings.TrimSpace(subscription.ID) == "" || strings.TrimSpace(subscription.URL) == "" {
		return XrayOutbound{}, fmt.Errorf("%w: id and URL are required", ErrInvalidSubscription)
	}
	if !subscription.Enabled {
		return XrayOutbound{}, fmt.Errorf("%w: subscription %q is disabled", ErrInvalidSubscription, subscription.ID)
	}
	active, err := SelectSubscriptionNode(subscription, nodes, probes)
	if err != nil {
		return XrayOutbound{}, err
	}
	activeID := strings.TrimSpace(active.ID)
	if strings.TrimSpace(active.Secret) == "" {
		return XrayOutbound{}, fmt.Errorf("%w: active node %q has no runtime credential", ErrInvalidSubscription, activeID)
	}
	outbound, err := CompileNodeOutbound(active)
	if err != nil {
		return XrayOutbound{}, fmt.Errorf("%w: compile active node %q: %v", ErrInvalidSubscription, activeID, err)
	}
	return outbound, nil
}

func CompileFastestSubscriptionRuntime(subscription Subscription, nodes []Node, probes []NodeProbe, inboundTag string) ([]XrayOutbound, XrayRouting, XrayObservatory, error) {
	if strings.TrimSpace(subscription.ID) == "" || strings.TrimSpace(subscription.URL) == "" || !subscription.Enabled {
		return nil, XrayRouting{}, XrayObservatory{}, fmt.Errorf("%w: enabled subscription with id and URL is required", ErrInvalidSubscription)
	}
	if SelectionMode(strings.TrimSpace(string(subscription.Selection))) != SelectionFastest {
		return nil, XrayRouting{}, XrayObservatory{}, fmt.Errorf("%w: fastest selection mode is required", ErrInvalidSubscription)
	}
	first, err := SelectSubscriptionNode(subscription, nodes, probes)
	if err != nil {
		return nil, XrayRouting{}, XrayObservatory{}, err
	}
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			return nil, XrayRouting{}, XrayObservatory{}, fmt.Errorf("%w: node ID is required", ErrInvalidSubscription)
		}
		if _, exists := byID[id]; exists {
			return nil, XrayRouting{}, XrayObservatory{}, fmt.Errorf("%w: duplicate node %q", ErrInvalidSubscription, id)
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
	ordered := []string{strings.TrimSpace(first.ID)}
	seen := map[string]bool{ordered[0]: true}
	for _, rawID := range candidateIDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		ordered = append(ordered, id)
		seen[id] = true
	}
	prefix := "subscription-" + strings.TrimSpace(subscription.ID) + "-node-"
	outbounds := make([]XrayOutbound, 0, len(ordered))
	for _, id := range ordered {
		node, exists := byID[id]
		if !exists {
			return nil, XrayRouting{}, XrayObservatory{}, fmt.Errorf("%w: fastest node %q is missing", ErrInvalidSubscription, id)
		}
		outbound, compileErr := CompileNodeOutbound(node)
		if compileErr != nil {
			return nil, XrayRouting{}, XrayObservatory{}, fmt.Errorf("%w: compile candidate node %q: %v", ErrInvalidSubscription, id, compileErr)
		}
		outbound.Tag = prefix + id
		outbounds = append(outbounds, outbound)
	}
	balancerTag := "subscription-" + strings.TrimSpace(subscription.ID) + "-fastest"
	routing := XrayRouting{
		Rules:     []XrayRoutingRule{{Type: "field", InboundTags: []string{inboundTag}, BalancerTag: balancerTag}},
		Balancers: []XrayBalancer{{Tag: balancerTag, Selector: []string{prefix}, Strategy: XrayBalancerStrategy{Type: "leastPing", Settings: map[string]any{}}}},
	}
	observatory := XrayObservatory{SubjectSelector: []string{prefix}, ProbeURL: "https://www.gstatic.com/generate_204", ProbeInterval: "10s", EnableConcurrency: true}
	return outbounds, routing, observatory, nil
}
