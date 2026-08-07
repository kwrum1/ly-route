package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"ly-route/backend/internal/httpapi"
	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/proxy"
)

type memoryGatewayTelemetryConfig struct {
	documents map[string][]persistence.ConfigDocument
}

func (store memoryGatewayTelemetryConfig) Configs(_ context.Context, resourceType string) ([]persistence.ConfigDocument, error) {
	return append([]persistence.ConfigDocument(nil), store.documents[resourceType]...), nil
}

func TestVPPCTLGatewayTelemetryUsesVPPLogicalCountersAndLANObservations(t *testing.T) {
	const proxyID = "proxy-egress-test"
	network := proxy.ServiceNetworkForEgressID(proxyID)
	store := memoryGatewayTelemetryConfig{documents: map[string][]persistence.ConfigDocument{
		"interface":    {telemetryConfigDocument(t, "interface", "lan0", map[string]any{"id": "lan0", "system_name": "ens192", "gateway_role": "lan", "cidr": "10.1.18.1/24"})},
		"wan_link":     {telemetryConfigDocument(t, "wan_link", "wan0", map[string]any{"id": "wan0", "name": "家庭宽带", "interface_id": "ens224", "enabled": true, "ipv4": map[string]any{"mode": "pppoe"}})},
		"proxy_egress": {telemetryConfigDocument(t, "proxy_egress", proxyID, map[string]any{"id": proxyID, "name": "代理线路", "underlay_wan_id": "wan0", "node_id": "node-a", "enabled": true})},
	}}
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	samples := []string{
		vppGatewayInterfaceSample(network, 1_000, 1_000, 100, 50, 80, 70),
		vppGatewayInterfaceSample(network, 2_000, 1_500, 500, 250, 180, 270),
	}
	sample := 0
	collector := newVPPCTLGatewayTelemetry(store, "vppctl", func() time.Time { return now })
	collector.run = func(_ context.Context, _ string, args ...string) (string, error) {
		switch fmt.Sprint(args) {
		case "[show interface]":
			output := samples[sample]
			sample++
			return output, nil
		case "[show ip neighbors]":
			return "1.25 10.1.18.101 D 00:11:22:33:44:55 lyroute-ens192\n2.00 198.18.0.2 D 00:aa:bb:cc:dd:ee " + network.IngressVPPInterface + "\n", nil
		case "[show nat44 sessions]":
			return `NAT44 ED sessions:
    i2o 10.1.18.101 proto TCP port 50123 fib 0
    o2i 10.67.0.10 proto TCP port 60123 fib 0
       external host 203.0.113.10:443
       total pkts 4, total bytes 2048

    i2o 198.18.1.2 proto TCP port 12345 fib 3
    o2i 10.67.0.10 proto TCP port 23456 fib 0
       external host 203.0.113.20:443
       total pkts 8, total bytes 4096
`, nil
		default:
			return "", fmt.Errorf("unexpected VPP command %v", args)
		}
	}

	first, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.LogicalEgresses) != 2 || first.LogicalEgresses[0].DownloadBytes != 0 || first.LogicalEgresses[1].DownloadBytes != 0 {
		t.Fatalf("first baseline snapshot = %#v", first.LogicalEgresses)
	}
	if len(first.Neighbors) != 1 || first.Neighbors[0].IP != "10.1.18.101" {
		t.Fatalf("LAN neighbors = %#v", first.Neighbors)
	}
	if len(first.Connections) != 1 || first.Connections[0].SourceIP != "10.1.18.101" || first.Connections[0].DestinationPort != 443 {
		t.Fatalf("LAN NAT connections = %#v", first.Connections)
	}

	now = now.Add(5 * time.Minute)
	second, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wan := logicalEgressByID(t, second.LogicalEgresses, "wan0")
	proxyEgress := logicalEgressByID(t, second.LogicalEgresses, proxyID)
	if wan.DownloadBytes != 800 || wan.UploadBytes != 400 {
		t.Fatalf("direct WAN counters = %#v, want proxy transport removed", wan)
	}
	if proxyEgress.DownloadBytes != 400 || proxyEgress.UploadBytes != 200 || proxyEgress.UnderlayWANID != "wan0" {
		t.Fatalf("proxy counters = %#v", proxyEgress)
	}
}

func telemetryConfigDocument(t *testing.T, resourceType, id string, payload map[string]any) persistence.ConfigDocument {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return persistence.ConfigDocument{ResourceType: resourceType, ResourceID: id, Payload: raw}
}

func vppGatewayInterfaceSample(network proxy.ProxyServiceNetwork, wanRX, wanTX, proxyInRX, proxyInTX, proxyOutRX, proxyOutTX int64) string {
	return fmt.Sprintf(`Name Idx State MTU Counter Count
lyroute-ens192 1 up 9000/0/0/0 rx packets 10
 rx bytes 1000
 tx packets 10
 tx bytes 1000
lyroute-ens224 2 up 9000/0/0/0 rx packets 10
 rx bytes %d
 tx packets 10
 tx bytes %d
%s 3 up 1500/0/0/0 rx packets 10
 rx bytes %d
 tx packets 10
 tx bytes %d
%s 4 up 1500/0/0/0 rx packets 10
 rx bytes %d
 tx packets 10
 tx bytes %d
pppoe_session0 5 up 0/0/0/0 rx packets 10
 rx bytes 1000
 tx packets 10
 tx bytes 1000
`, wanRX, wanTX, network.IngressVPPInterface, proxyInRX, proxyInTX, network.EgressVPPInterface, proxyOutRX, proxyOutTX)
}

func logicalEgressByID(t *testing.T, items []httpapi.LogicalEgressCounter, id string) httpapi.LogicalEgressCounter {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("logical egress %q not found in %#v", id, items)
	return httpapi.LogicalEgressCounter{}
}
