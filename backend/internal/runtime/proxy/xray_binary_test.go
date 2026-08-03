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
	outbounds, routing, observatory, err := CompileFastestSubscriptionRuntime(subscription, nodes, []NodeProbe{{NodeID: "a", Reachable: true, RTT: time.Millisecond, ObservedAt: now}, {NodeID: "b", Reachable: true, RTT: 2 * time.Millisecond, ObservedAt: now}}, "proxy-in")
	if err != nil {
		t.Fatal(err)
	}
	payload := XrayConfigPayload{
		Log:         XrayLog{Level: "warning"},
		Inbounds:    []XrayInbound{{Tag: "proxy-in", Listen: "127.0.0.1", Port: 23080, Protocol: "dokodemo-door", Settings: XrayDokodemoSettings{Address: "127.0.0.1", Network: "tcp", FollowRedirect: true}}},
		Outbounds:   outbounds,
		Routing:     &routing,
		Observatory: &observatory,
	}
	if err := EnableXrayRoutingAPI(&payload); err != nil {
		t.Fatal(err)
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
