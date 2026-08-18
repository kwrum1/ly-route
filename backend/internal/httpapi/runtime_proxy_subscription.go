package httpapi

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ly-route/backend/internal/runtime/proxy"
)

func (server *Server) compileProxySubscription(ctx context.Context, egress proxy.Egress, compiled *proxy.CompiledEgress) string {
	if compiled == nil {
		return "proxy runtime compilation requires a compiled egress"
	}
	subscriptions, err := server.desiredItems(ctx, "proxy_subscription")
	if err != nil {
		return fmt.Sprintf("proxy subscription read failed: %v", err)
	}
	nodes, err := server.desiredItems(ctx, "proxy_node")
	if err != nil {
		return fmt.Sprintf("proxy node read failed: %v", err)
	}
	proxyNodes := make([]proxy.Node, 0, len(nodes))
	for _, item := range nodes {
		if enabled, exists := item["enabled"].(bool); exists && !enabled {
			continue
		}
		secret := stringField(item, "secret")
		if secret == "" && server.store != nil {
			secret, _ = server.store.Secret(ctx, "proxy_node", stringField(item, "id"), "secret")
		}
		settings, _ := item["settings"].(map[string]any)
		proxyNodes = append(proxyNodes, proxy.Node{
			ID:       stringField(item, "id"),
			Name:     stringField(item, "name"),
			Protocol: stringField(item, "protocol"),
			Address:  stringField(item, "address"),
			Port:     intField(item, "port"),
			Secret:   secret,
			Settings: settings,
		})
	}

	compileDirectNode := func(nodeID string) string {
		for _, node := range proxyNodes {
			if node.ID != strings.TrimSpace(nodeID) {
				continue
			}
			runtimeNodes := prepareProxyNodeTLS(ctx, []proxy.Node{node}, []string{node.ID})
			if len(runtimeNodes) != 1 {
				return fmt.Sprintf("proxy node %q could not be prepared", node.ID)
			}
			resolved, resolveErr := proxy.ResolveNodeAddress(ctx, runtimeNodes[0])
			if resolveErr != nil {
				return resolveErr.Error()
			}
			runtimeNodes[0] = resolved
			outbound, compileErr := proxy.CompileNodeOutbound(runtimeNodes[0])
			if compileErr != nil {
				return compileErr.Error()
			}
			compiled.XrayRuntime.ConfigPayload.Outbounds = []proxy.XrayOutbound{outbound}
			compiled.XrayRuntime.ConfigPayload.Routing = nil
			compiled.XrayRuntime.ConfigPayload.Observatory = nil
			compiled.XrayRuntime.ConfigPayload.API = nil
			compiled.XrayRuntime.OutboundTag = outbound.Tag
			return ""
		}
		return fmt.Sprintf("configured proxy node %q is missing or disabled", strings.TrimSpace(nodeID))
	}

	if selectedNodeID := strings.TrimSpace(egress.NodeID); selectedNodeID != "" {
		return compileDirectNode(selectedNodeID)
	}

	selectedSubscriptionID := strings.TrimSpace(egress.SubscriptionID)
	activeSubscriptions := make([]map[string]any, 0, len(subscriptions))
	for _, item := range subscriptions {
		enabled, exists := item["enabled"].(bool)
		if exists && !enabled {
			continue
		}
		if selectedSubscriptionID != "" && stringField(item, "id") != selectedSubscriptionID {
			continue
		}
		activeSubscriptions = append(activeSubscriptions, item)
	}
	if selectedSubscriptionID != "" && len(activeSubscriptions) == 0 {
		return fmt.Sprintf("configured proxy subscription %q is missing or disabled", selectedSubscriptionID)
	}
	if selectedSubscriptionID == "" && len(activeSubscriptions) > 1 {
		return "multiple enabled proxy subscriptions require subscription_id on the proxy egress"
	}
	if len(activeSubscriptions) == 0 {
		if len(proxyNodes) == 1 {
			return compileDirectNode(proxyNodes[0].ID)
		}
		return "proxy egress requires node_id or subscription_id"
	}

	for _, item := range activeSubscriptions {
		subscriptionURL := stringField(item, "url")
		if subscriptionURL == "" && server.store != nil {
			subscriptionURL, _ = server.store.Secret(ctx, "proxy_subscription", stringField(item, "id"), "url")
		}
		subscription := proxy.Subscription{
			ID:        stringField(item, "id"),
			Name:      stringField(item, "name"),
			URL:       subscriptionURL,
			Enabled:   true,
			NodeRefs:  stringSliceField(item, "node_refs"),
			Selection: proxy.SelectionMode(strings.TrimSpace(stringField(item, "selection"))),
		}
		runtimeNodes := prepareProxyNodeTLS(ctx, proxyNodes, subscription.NodeRefs)
		resolvedNodes := make([]proxy.Node, 0, len(runtimeNodes))
		for _, node := range runtimeNodes {
			resolved, resolveErr := proxy.ResolveNodeAddress(ctx, node)
			if resolveErr != nil {
				continue
			}
			resolvedNodes = append(resolvedNodes, resolved)
		}
		runtimeNodes = resolvedNodes
		var probes []proxy.NodeProbe
		if subscription.Selection == proxy.SelectionFastest {
			probes = probeProxyNodes(ctx, runtimeNodes, subscription.NodeRefs)
			if len(compiled.XrayRuntime.ConfigPayload.Inbounds) == 0 {
				return "proxy runtime has no inbound for fastest-node routing"
			}
			outbounds, routing, observatory, compileErr := proxy.CompileFastestSubscriptionRuntime(subscription, runtimeNodes, probes, compiled.XrayRuntime.ConfigPayload.Inbounds[0].Tag)
			if compileErr != nil {
				return compileErr.Error()
			}
			compiled.XrayRuntime.ConfigPayload.Outbounds = outbounds
			compiled.XrayRuntime.ConfigPayload.Routing = &routing
			compiled.XrayRuntime.ConfigPayload.Observatory = &observatory
			if apiErr := proxy.EnableXrayRoutingAPI(&compiled.XrayRuntime.ConfigPayload); apiErr != nil {
				return apiErr.Error()
			}
			compiled.XrayRuntime.OutboundTag = routing.Balancers[0].Tag
			return ""
		}
		outbound, err := proxy.CompileSubscriptionWithSelection(subscription, runtimeNodes, probes)
		if err != nil {
			return err.Error()
		}
		compiled.XrayRuntime.ConfigPayload.Outbounds = []proxy.XrayOutbound{outbound}
		return ""
	}
	return "proxy egress requires a usable proxy node or subscription"
}

func prepareProxyNodeTLS(ctx context.Context, nodes []proxy.Node, selected []string) []proxy.Node {
	allowed := make(map[string]bool, len(selected))
	for _, id := range selected {
		allowed[strings.TrimSpace(id)] = true
	}
	prepared := append([]proxy.Node(nil), nodes...)
	for index, node := range prepared {
		if len(allowed) > 0 && !allowed[node.ID] {
			continue
		}
		if candidate, err := proxy.PrepareNodeTLS(ctx, node); err == nil {
			prepared[index] = candidate
		}
	}
	return prepared
}

var proxyPingRTT = regexp.MustCompile(`time[=<]([0-9]+(?:\.[0-9]+)?)\s*ms`)

func probeProxyNodes(ctx context.Context, nodes []proxy.Node, selected []string) []proxy.NodeProbe {
	allowed := make(map[string]bool, len(selected))
	for _, id := range selected {
		allowed[strings.TrimSpace(id)] = true
	}
	probes := make([]proxy.NodeProbe, 0, len(nodes))
	for _, node := range nodes {
		if len(allowed) > 0 && !allowed[node.ID] {
			continue
		}
		probe := proxy.NodeProbe{NodeID: node.ID, ObservedAt: time.Now().UTC()}
		commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		output, err := exec.CommandContext(commandCtx, "ping", "-n", "-c", "1", "-W", "1", node.Address).CombinedOutput()
		cancel()
		if err == nil {
			match := proxyPingRTT.FindSubmatch(output)
			if len(match) == 2 {
				if milliseconds, parseErr := strconv.ParseFloat(string(match[1]), 64); parseErr == nil {
					probe.Reachable = true
					probe.RTT = time.Duration(milliseconds * float64(time.Millisecond))
				}
			}
		}
		probes = append(probes, probe)
	}
	return probes
}

func degradeProxyRuntime(compiled *proxy.CompiledEgress) {
	compiled.ServiceTargets = nil
	compiled.DataplaneTargets = nil
	compiled.VPPSteering = nil
	compiled.NftablesCapture = proxy.NftablesCapturePlan{}
	compiled.LinuxPolicyRouting = proxy.LinuxPolicyRoutingPlan{}
	compiled.XrayRuntime = proxy.XrayRuntime{}
}
