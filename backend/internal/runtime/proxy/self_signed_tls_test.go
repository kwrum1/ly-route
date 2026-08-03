package proxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestPrepareNodeTLSAutomaticallyPinsSelfSignedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(endpoint.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	node, err := PrepareNodeTLS(context.Background(), Node{
		ID: "private", Protocol: "trojan", Address: host, Port: port, Secret: "secret",
		Settings: map[string]any{"security": "tls", "tlsSettings": map[string]any{"serverName": "private.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tlsSettings := node.Settings["tlsSettings"].(map[string]any)
	pin := settingString(tlsSettings, "pinnedPeerCertSha256", "")
	if len(pin) != 64 {
		t.Fatalf("automatic certificate pin = %q", pin)
	}
	outbound, err := CompileNodeOutbound(node)
	if err != nil {
		t.Fatal(err)
	}
	compiledTLS := outbound.StreamSettings["tlsSettings"].(map[string]any)
	if compiledTLS["pinnedPeerCertSha256"] != pin {
		t.Fatalf("compiled TLS settings = %#v", compiledTLS)
	}
}

func TestCompileRealityNodeKeepsPublicKeyVerification(t *testing.T) {
	outbound, err := CompileNodeOutbound(Node{
		ID: "reality", Protocol: "vless", Address: "reality.example", Port: 443,
		Secret:   "11111111-1111-1111-1111-111111111111",
		Settings: map[string]any{"security": "reality", "realitySettings": map[string]any{"publicKey": "key"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := outbound.StreamSettings["tlsSettings"]; exists {
		t.Fatalf("Reality outbound unexpectedly received TLS settings: %#v", outbound.StreamSettings)
	}
}
