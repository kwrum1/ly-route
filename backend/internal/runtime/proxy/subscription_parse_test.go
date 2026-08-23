package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSubscriptionDecodesVLESSReality(t *testing.T) {
	uri := "vless://11111111-1111-1111-1111-111111111111@node.example:443?type=tcp&encryption=none&security=reality&pbk=public-key&pqv=post-quantum-verify-key&fp=chrome&sni=www.example.com&sid=98&flow=xtls-rprx-vision#Primary"
	content := base64.StdEncoding.EncodeToString([]byte(uri + "\n"))
	nodes, err := ParseSubscription([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Protocol != "vless" || nodes[0].Address != "node.example" || nodes[0].Port != 443 || nodes[0].Name != "Primary" {
		t.Fatalf("node = %#v", nodes)
	}
	if nodes[0].Settings["security"] != "reality" || nodes[0].Settings["flow"] != "xtls-rprx-vision" {
		t.Fatalf("settings = %#v", nodes[0].Settings)
	}
	reality := nodes[0].Settings["realitySettings"].(map[string]any)
	if reality["publicKey"] != "public-key" || reality["mldsa65Verify"] != "post-quantum-verify-key" || reality["fingerprint"] != "chrome" || reality["serverName"] != "www.example.com" || reality["shortId"] != "98" {
		t.Fatalf("Reality settings = %#v", reality)
	}
	outbound, err := CompileNodeOutbound(nodes[0])
	if err != nil || outbound.StreamSettings["realitySettings"] == nil {
		t.Fatalf("outbound = %#v, err=%v", outbound, err)
	}
}

func TestParseNodeURICoversVMessTrojanAndShadowsocks(t *testing.T) {
	vmessPayload, _ := json.Marshal(map[string]any{"v": "2", "ps": "VMess", "add": "vmess.example", "port": "443", "id": "22222222-2222-2222-2222-222222222222", "net": "tcp", "tls": "tls", "sni": "vmess.example", "scy": "auto"})
	inputs := []string{
		"vmess://" + base64.RawStdEncoding.EncodeToString(vmessPayload),
		"trojan://password@trojan.example:443?security=tls&sni=trojan.example#Trojan",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password")) + "@ss.example:8388#SS",
	}
	for _, input := range inputs {
		node, err := ParseNodeURI(input)
		if err != nil {
			t.Fatalf("ParseNodeURI(%q): %v", input[:8], err)
		}
		if _, err := CompileNodeOutbound(node); err != nil {
			t.Fatalf("compile %s: %v", node.Protocol, err)
		}
	}
}

func TestParseSubscriptionRejectsUnsupportedOrOversizedContent(t *testing.T) {
	if _, err := ParseSubscription([]byte("socks://user@example:1080")); err == nil {
		t.Fatal("unsupported subscription node was accepted")
	}
	if _, err := ParseSubscription(make([]byte, MaxSubscriptionBytes+1)); err == nil {
		t.Fatal("oversized subscription was accepted")
	}
}

func TestParseSubscriptionSkipsUnsupportedNodesWhenSupportedNodesRemain(t *testing.T) {
	vless := "vless://11111111-1111-1111-1111-111111111111@node.example:443?type=tcp&encryption=none&security=reality&pbk=public-key&fp=chrome&sni=www.example.com&sid=98&flow=xtls-rprx-vision#Primary"
	content := base64.StdEncoding.EncodeToString([]byte(vless + "\nhysteria2://secret@unsupported.example:443#Unsupported\n"))

	nodes, err := ParseSubscription([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Protocol != "vless" || nodes[0].Name != "Primary" {
		t.Fatalf("nodes = %#v, want the supported VLESS node only", nodes)
	}
}

func TestPrivateSubscriptionRealityNodeCarriesLiveTraffic(t *testing.T) {
	privateFile := strings.TrimSpace(os.Getenv("LY_ROUTE_PRIVATE_SUBSCRIPTION_FILE"))
	xrayBinary := strings.TrimSpace(os.Getenv("LY_ROUTE_XRAY_BINARY"))
	if privateFile == "" || xrayBinary == "" {
		t.Skip("private subscription and Xray binary are not configured")
	}
	content, err := os.ReadFile(privateFile)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := ParseSubscription(content)
	if err != nil || len(nodes) == 0 {
		t.Fatalf("parse private subscription: nodes=%d err=%v", len(nodes), err)
	}
	outbound, err := CompileNodeOutbound(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	payload := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  []any{map[string]any{"tag": "private-test-in", "listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"udp": false}}},
		"outbounds": []XrayOutbound{outbound},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "private-xray.json")
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(xrayBinary, "run", "-config", configPath)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("private Xray test listener did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	curl := exec.Command("curl", "--silent", "--show-error", "--max-time", "15", "--noproxy", "", "--socks5-hostname", fmt.Sprintf("127.0.0.1:%d", port), "--output", "/dev/null", "--write-out", "%{http_code}", "https://www.gstatic.com/generate_204")
	curl.Env = withoutProxyEnvironment(os.Environ())
	output, err := curl.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "204" {
		t.Fatalf("private Reality traffic verification failed: status=%q err=%v", strings.TrimSpace(string(output)), err)
	}
}

func withoutProxyEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		name := strings.ToLower(strings.SplitN(item, "=", 2)[0])
		if name == "http_proxy" || name == "https_proxy" || name == "all_proxy" || name == "no_proxy" {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}
