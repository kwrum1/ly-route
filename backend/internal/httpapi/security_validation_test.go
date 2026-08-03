package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
)

func TestSecurityDesiredContractAcceptsOnlyCanonicalL2L4Resources(t *testing.T) {
	tests := []struct {
		resource string
		payload  map[string]any
	}{
		{
			resource: "security_acl",
			payload: map[string]any{"id": "sec-acl-wan", "priority": float64(10), "action": "deny", "match": map[string]any{
				"src_ip": "any", "dst_ip": "10.0.0.0/8", "protocol": "tcp", "dst_port": "443", "direction": "input",
			}},
		},
		{
			resource: "security_ip_mac_binding",
			payload: map[string]any{"id": "sec-bind-lan", "priority": float64(20), "interface_id": "lan0", "binding_mode": "enforce", "unbound_behavior": "block", "bindings": []any{
				map[string]any{"ip": "192.0.2.10", "mac": "02:00:00:00:00:10"},
			}},
		},
		{
			resource: "security_threat_intel",
			payload:  map[string]any{"id": "sec-threat-wan", "priority": float64(30), "interface_id": "wan0", "direction": "both", "list_type": "blacklist", "entries": []any{"203.0.113.9", "2001:db8::/48"}},
		},
		{
			resource: "security_attack_rule",
			payload:  map[string]any{"id": "sec-syn-wan", "priority": float64(40), "interface_id": "wan0", "attack_type": "syn_flood", "threshold_pps": float64(2000), "burst_packets": float64(4000), "enforcement_mode": "enforce", "source_prefix": "any", "destination_prefix": "192.0.2.0/24"},
		},
	}
	for _, test := range tests {
		t.Run(test.resource, func(t *testing.T) {
			if err := validateDesiredPayload(test.resource, test.payload); err != nil {
				t.Fatalf("canonical payload rejected: %v", err)
			}
			if test.payload["kind"] == "" || test.payload["enabled"] != true {
				t.Fatalf("canonical defaults missing: %#v", test.payload)
			}
		})
	}
}

func TestSecurityConfigImportCannotBypassLivePayloadContract(t *testing.T) {
	invalid := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: product.Gateway().ID(), DeviceMode: "gateway", Resources: map[string][]json.RawMessage{
		"security_threat_intel": {json.RawMessage(`{"id":"sec-import","priority":1,"interface_id":"wan0","direction":"input","list_type":"blacklist","entries":["bad.example"],"dpi":true}`)},
	}}
	documents, err := configDocumentsFromImport(invalid, fixedClock()(), product.Gateway())
	if err == nil || len(documents) != 0 || !strings.Contains(err.Error(), "unsupported security capability") {
		t.Fatalf("documents = %#v, error = %v", documents, err)
	}

	valid := invalid
	valid.Resources = map[string][]json.RawMessage{
		"security_threat_intel": {json.RawMessage(`{"id":"sec-import","priority":1,"interface_id":"wan0","direction":"input","list_type":"blacklist","entries":["198.51.100.9/32"]}`)},
	}
	documents, err = configDocumentsFromImport(valid, fixedClock()(), product.Gateway())
	if err != nil || len(documents) != 1 {
		t.Fatalf("valid import documents = %#v, error = %v", documents, err)
	}
}

func TestSecurityDesiredContractRejectsUnsupportedAndUnsafeCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		payload  map[string]any
		contains string
	}{
		{"dpi", "security_acl", map[string]any{"id": "sec-dpi", "priority": float64(1), "action": "deny", "dpi": true, "match": map[string]any{"src_ip": "any"}}, "unsupported security capability"},
		{"domain selector", "security_acl", map[string]any{"id": "sec-domain", "priority": float64(1), "action": "deny", "match": map[string]any{"domain": "example.com"}}, "unsupported security capability"},
		{"threat domain", "security_threat_intel", map[string]any{"id": "sec-feed", "priority": float64(1), "interface_id": "wan0", "direction": "input", "list_type": "blacklist", "entries": []any{"evil.example"}}, "domains and ranges are not accepted"},
		{"threat range", "security_threat_intel", map[string]any{"id": "sec-feed", "priority": float64(1), "interface_id": "wan0", "direction": "input", "list_type": "blacklist", "entries": []any{"192.0.2.1-192.0.2.10"}}, "domains and ranges are not accepted"},
		{"multicast mac", "security_ip_mac_binding", map[string]any{"id": "sec-bind", "priority": float64(1), "interface_id": "lan0", "binding_mode": "enforce", "unbound_behavior": "block", "bindings": []any{map[string]any{"ip": "192.0.2.1", "mac": "01:00:5e:00:00:01"}}}, "unicast"},
		{"alert blocks", "security_ip_mac_binding", map[string]any{"id": "sec-bind", "priority": float64(1), "interface_id": "lan0", "binding_mode": "alert", "unbound_behavior": "block", "bindings": []any{map[string]any{"ip": "192.0.2.1", "mac": "02:00:00:00:00:01"}}}, "audit_only"},
		{"zero attack threshold", "security_attack_rule", map[string]any{"id": "sec-syn", "priority": float64(1), "interface_id": "wan0", "attack_type": "syn_flood", "threshold_pps": float64(0), "burst_packets": float64(10), "enforcement_mode": "enforce"}, "threshold_pps"},
		{"legacy string threshold", "security_attack_rule", map[string]any{"id": "sec-syn", "priority": float64(1), "interface_id": "wan0", "attack_type": "syn_flood", "threshold": "2000pps", "burst_packets": float64(10), "enforcement_mode": "enforce"}, "unsupported field"},
		{"application identification", "security_attack_rule", map[string]any{"id": "sec-app", "priority": float64(1), "interface_id": "wan0", "attack_type": "udp_flood", "threshold_pps": float64(10), "burst_packets": float64(20), "enforcement_mode": "alert", "application_id": "dns"}, "unsupported security capability"},
		{"fractional priority", "security_acl", map[string]any{"id": "sec-priority", "priority": 1.5, "action": "deny", "match": map[string]any{"src_ip": "any"}}, "positive integer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDesiredPayload(test.resource, test.payload)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

func TestSecurityInvalidMutationIsAtomicAndAudited(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-security-contract-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	invalid := []struct {
		path string
		body string
	}{
		{"/api/v1/security/acls", `{"id":"sec-dpi","priority":1,"action":"deny","match":{"src_ip":"any"},"dpi":true}`},
		{"/api/v1/security/ip-mac-bindings", `{"id":"sec-bind","priority":1,"interface_id":"lan0","binding_mode":"enforce","unbound_behavior":"block","bindings":[{"ip":"192.0.2.2","mac":"not-a-mac"}]}`},
		{"/api/v1/security/threat-intel", `{"id":"sec-feed","priority":1,"interface_id":"wan0","direction":"input","list_type":"blacklist","entries":["bad.example"]}`},
		{"/api/v1/security/attack-rules", `{"id":"sec-syn","priority":1,"interface_id":"wan0","attack_type":"syn_flood","threshold_pps":0,"burst_packets":10,"enforcement_mode":"enforce"}`},
		{"/api/v1/security/attack-rules", `{"id":`},
	}
	for _, item := range invalid {
		response := authenticatedJSONRequest(t, server, http.MethodPost, item.path, item.body, cookie)
		if response.Code != http.StatusUnprocessableEntity && response.Code != http.StatusBadRequest {
			t.Fatalf("invalid mutation %s = %d: %s", item.path, response.Code, response.Body.String())
		}
	}
	for _, resource := range []string{"security_acl", "security_ip_mac_binding", "security_threat_intel", "security_attack_rule"} {
		items, err := server.desiredItems(ctx, resource)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if id := stringField(item, "id"); id == "sec-dpi" || id == "sec-bind" || id == "sec-feed" || id == "sec-syn" {
				t.Fatalf("invalid %s mutation persisted state: %#v", resource, items)
			}
		}
	}
	events, err := server.auditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failures := 0
	for _, event := range events {
		if strings.HasPrefix(event.Resource, "/api/v1/security/") && event.Status == "failure" {
			failures++
		}
	}
	if failures != len(invalid) {
		t.Fatalf("security failure audits = %d, want %d: %#v", failures, len(invalid), events)
	}
}
