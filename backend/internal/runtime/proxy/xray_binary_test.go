package proxy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestXrayRuntimeConfigsAcceptedByBinary(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LY_ROUTE_XRAY_BINARY"))
	if binary == "" {
		t.Skip("LY_ROUTE_XRAY_BINARY is not set")
	}
	uuid := "11111111-1111-1111-1111-111111111111"
	nodes := []Node{
		{ID: "a", Protocol: "vless", Address: "127.0.0.1", Port: 21001, Secret: uuid, Settings: map[string]any{"encryption": "none", "network": "tcp"}},
		{ID: "b", Protocol: "vless", Address: "127.0.0.1", Port: 21002, Secret: uuid, Settings: map[string]any{"encryption": "none", "network": "tcp", "security": "tls", "tlsSettings": map[string]any{"serverName": "self-signed.example", "pinnedPeerCertSha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}},
	}
	subscription := Subscription{ID: "main", URL: "https://subscription.invalid/list", Enabled: true, NodeRefs: []string{"a", "b"}, Selection: SelectionFastest}
	now := time.Now().UTC()
	outbound, err := CompileSubscriptionWithSelection(subscription, nodes, []NodeProbe{{NodeID: "a", Reachable: true, RTT: time.Millisecond, ObservedAt: now}, {NodeID: "b", Reachable: true, RTT: 2 * time.Millisecond, ObservedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	if outbound.Tag == "" {
		t.Fatal("fastest selection did not compile an outbound")
	}
	payload := XrayConfigPayload{
		Log:       XrayLog{Level: "warning"},
		Inbounds:  []XrayInbound{{Tag: "proxy-in", Listen: "127.0.0.1", Port: 23080, Protocol: "dokodemo-door", Settings: XrayDokodemoSettings{Address: "127.0.0.1", Network: "tcp", FollowRedirect: true}}},
		Outbounds: []XrayOutbound{outbound},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "xray-fastest.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(binary, "run", "-test", "-config", path).CombinedOutput()
	if err != nil {
		t.Fatalf("Xray rejected generated fastest-node config: %v: %s", err, output)
	}
}

func TestCompileAdaptiveSubscriptionRuntimeUsesTopNAndIndependentLanes(t *testing.T) {
	now := time.Now().UTC()
	nodes := []Node{
		{ID: "a", Protocol: "vless", Address: "127.0.0.1", Port: 21001, Secret: "11111111-1111-1111-1111-111111111111"},
		{ID: "b", Protocol: "vless", Address: "127.0.0.1", Port: 21002, Secret: "11111111-1111-1111-1111-111111111111"},
		{ID: "c", Protocol: "vless", Address: "127.0.0.1", Port: 21003, Secret: "11111111-1111-1111-1111-111111111111"},
	}
	subscription := Subscription{ID: "main", URL: "https://subscription.invalid/list", Enabled: true, NodeRefs: []string{"a", "b", "c"}, TopN: 2}
	runtime, err := CompileAdaptiveSubscriptionLanes(subscription, nodes, []NodeProbe{
		{NodeID: "a", Reachable: true, RTT: 10 * time.Millisecond, ObservedAt: now},
		{NodeID: "b", Reachable: true, RTT: 20 * time.Millisecond, ObservedAt: now},
		{NodeID: "c", Reachable: true, RTT: 30 * time.Millisecond, ObservedAt: now},
	}, 23080)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Outbounds) != 3 || len(runtime.Inbounds) != 3 || len(runtime.Routing.Rules) != 3 {
		t.Fatalf("adaptive runtime = %#v", runtime)
	}
	if runtime.Lanes[0].NodeID != "a" || runtime.Lanes[0].Weight != 67 || runtime.Lanes[1].NodeID != "b" || runtime.Lanes[1].Weight != 33 {
		t.Fatalf("adaptive lanes = %#v", runtime.Lanes)
	}
	if len(runtime.Routing.Balancers) != 0 || runtime.Routing.Rules[0].OutboundTag != "subscription-main-node-a" || runtime.Routing.Rules[1].OutboundTag != "subscription-main-node-b" {
		t.Fatalf("adaptive routing = %#v", runtime.Routing)
	}
}
