package nat

import "testing"

func TestCompileConfigRequiresReturnPathGuard(t *testing.T) {
	config, err := CompileConfigWithWANs(
		[]map[string]any{{"id": "host", "external_address": "203.0.113.10", "internal_address": "192.0.2.10"}},
		[]map[string]any{{"id": "web", "wan_link": "wan", "protocol": "tcp", "external_port": 8443, "internal_host": "192.0.2.20", "internal_port": 443}},
		[]map[string]any{{"id": "wan", "current_address": "203.0.113.10"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !config.StaticMappings[0].ReturnPathGuard || !config.PortMappings[0].ReturnPathGuard {
		t.Fatalf("compiled NAT return guards = %#v", config)
	}
}
