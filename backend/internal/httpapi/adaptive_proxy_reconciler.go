package httpapi

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"ly-route/backend/internal/product"
	"ly-route/backend/internal/runtime/proxy"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

const defaultAdaptiveProxyReconcileInterval = 10 * time.Second

// StartAdaptiveProxyReconciler keeps new proxy flows on the healthiest
// subscription nodes without replaying the VPP or Xray runtime.
func (server *Server) StartAdaptiveProxyReconciler(ctx context.Context, interval time.Duration) {
	if server == nil || server.profile.ID() != product.Gateway().ID() || server.services == nil || server.services.Controller == nil {
		return
	}
	observer, ok := server.services.Controller.(serviceRuntime.XrayObservatoryStateController)
	if !ok {
		return
	}
	if interval <= 0 {
		interval = defaultAdaptiveProxyReconcileInterval
	}
	go server.runAdaptiveProxyReconciler(ctx, observer, interval)
}

func (server *Server) runAdaptiveProxyReconciler(ctx context.Context, observer serviceRuntime.XrayObservatoryStateController, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastHash := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hash, changed, err := server.reconcileAdaptiveProxy(ctx, observer, lastHash)
			if err != nil {
				log.Printf("adaptive proxy reconcile skipped: %v", err)
				continue
			}
			if changed {
				lastHash = hash
			}
		}
	}
}

func (server *Server) reconcileAdaptiveProxy(ctx context.Context, observer serviceRuntime.XrayObservatoryStateController, lastHash string) (string, bool, error) {
	server.runtimeApplyMu.Lock()
	defer server.runtimeApplyMu.Unlock()

	egress, configured, err := server.runtimeProxyEgress(ctx)
	if err != nil || !configured || strings.TrimSpace(egress.SubscriptionID) == "" {
		return lastHash, false, err
	}
	subscription, found, err := server.adaptiveSubscription(ctx, egress.SubscriptionID)
	if err != nil || !found {
		return lastHash, false, err
	}
	states, err := observer.XrayObservatoryStates(ctx)
	if err != nil {
		return lastHash, false, err
	}
	compiled, err := proxy.CompileEgress(egress)
	if err != nil {
		return lastHash, false, err
	}
	lanes, err := adaptiveLanesFromObservations(subscription, states, compiled.XrayRuntime.ListenPort, server.now().UTC())
	if err != nil {
		return lastHash, false, err
	}
	capture, err := proxy.BuildAdaptiveCapturePlan(compiled.NftablesCapture, lanes)
	if err != nil {
		return lastHash, false, err
	}
	assignments, err := server.runtimeAddressAssignments(ctx)
	if err != nil {
		return lastHash, false, err
	}
	artifacts, err := serviceRuntime.RenderGatewayNftablesCapture(capture, dnsLCPGuardPlan(assignments))
	if err != nil {
		return lastHash, false, err
	}
	if len(artifacts) != 1 {
		return lastHash, false, fmt.Errorf("adaptive proxy rendered %d nftables artifacts", len(artifacts))
	}
	if artifacts[0].ContentHash == lastHash {
		return lastHash, false, nil
	}
	if err := server.services.Controller.ReloadOrRestart(ctx, serviceRuntime.Nftables, artifacts); err != nil {
		return lastHash, false, err
	}
	return artifacts[0].ContentHash, true, nil
}

func (server *Server) adaptiveSubscription(ctx context.Context, subscriptionID string) (proxy.Subscription, bool, error) {
	items, err := server.desiredItems(ctx, "proxy_subscription")
	if err != nil {
		return proxy.Subscription{}, false, err
	}
	for _, item := range items {
		enabled, hasEnabled := item["enabled"]
		if stringField(item, "id") != strings.TrimSpace(subscriptionID) || (hasEnabled && !truthy(enabled)) {
			continue
		}
		selection := proxy.SelectionMode(strings.TrimSpace(stringField(item, "selection")))
		strategy := strings.TrimSpace(stringField(item, "strategy"))
		if selection != proxy.SelectionAdaptive && strategy != proxy.AdaptiveSubscriptionStrategy {
			return proxy.Subscription{}, false, nil
		}
		return proxy.Subscription{
			ID: stringField(item, "id"), URL: "runtime-observer", Enabled: true,
			NodeRefs: stringSliceField(item, "node_refs"), Selection: selection,
			Strategy: strategy, TopN: intField(item, "top_n"),
		}, true, nil
	}
	return proxy.Subscription{}, false, nil
}

func adaptiveLanesFromObservations(subscription proxy.Subscription, states []serviceRuntime.XrayObservatoryState, basePort int, observedAt time.Time) ([]proxy.AdaptiveNodeLane, error) {
	ids := append([]string(nil), subscription.NodeRefs...)
	sort.Strings(ids)
	nodes := make([]proxy.Node, 0, len(ids))
	probes := make([]proxy.NodeProbe, 0, len(ids))
	prefix := "subscription-" + subscription.ID + "-node-"
	byTag := make(map[string]serviceRuntime.XrayObservatoryState, len(states))
	for _, state := range states {
		byTag[state.OutboundTag] = state
	}
	for _, id := range ids {
		nodes = append(nodes, proxy.Node{ID: id})
		state, found := byTag[prefix+id]
		if found {
			probes = append(probes, proxy.NodeProbe{NodeID: id, Reachable: state.Alive, RTT: time.Duration(state.DelayMilliseconds) * time.Millisecond, ObservedAt: observedAt})
		}
	}
	plan, err := proxy.BuildRTTWeightedTopNPlan(subscription, nodes, probes)
	if err != nil {
		return nil, err
	}
	weights := make(map[string]int, len(plan.Nodes))
	for _, node := range plan.Nodes {
		weights[node.ID] = node.Weight
	}
	portByID := make(map[string]int, len(ids))
	for index, id := range ids {
		portByID[id] = basePort + index
	}
	lanes := make([]proxy.AdaptiveNodeLane, 0, len(plan.Nodes))
	for _, node := range plan.Nodes {
		if node.Weight > 0 {
			lanes = append(lanes, proxy.AdaptiveNodeLane{NodeID: node.ID, ListenerPort: portByID[node.ID], Weight: node.Weight})
		}
	}
	if len(lanes) == 0 {
		return nil, fmt.Errorf("adaptive proxy has no healthy lanes")
	}
	return lanes, nil
}
