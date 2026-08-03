package nat

import (
	"strings"
	"testing"
)

func TestCompileConfigBuildsStaticAndPortMappings(t *testing.T) {
	compiled, err := CompileConfig(
		[]map[string]any{{"id": "static-main", "external_address": "203.0.113.10", "internal_address": "192.168.88.10", "wan_link": "wan0"}},
		[]map[string]any{{"id": "web", "protocol": "TCP", "external_address": "203.0.113.10", "external_port": float64(8443), "internal_target": "192.168.88.20:8443", "hairpin": true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.StaticMappings) != 1 || compiled.StaticMappings[0].WANInterface != "wan0" {
		t.Fatalf("static mappings = %#v", compiled.StaticMappings)
	}
	if len(compiled.PortMappings) != 1 {
		t.Fatalf("port mappings = %#v", compiled.PortMappings)
	}
	mapping := compiled.PortMappings[0]
	if mapping.Protocol != "tcp" || mapping.InternalHost != "192.168.88.20" || mapping.InternalPort != 8443 || mapping.ExternalPort != 8443 || !mapping.Hairpin {
		t.Fatalf("port mapping = %#v", mapping)
	}
}

func TestCompileConfigRejectsIncompletePortMap(t *testing.T) {
	_, err := CompileConfig(nil, []map[string]any{{"id": "bad", "external_port": 8443, "internal_host": "192.168.88.20"}})
	if err == nil {
		t.Fatal("expected incomplete port map to fail")
	}
}

func TestCompileConfigResolvesWANAddressForPortMap(t *testing.T) {
	compiled, err := CompileConfigWithWANs(nil,
		[]map[string]any{{"id": "web", "protocol": "tcp", "wan_link": "wan0", "external_port": 8443, "internal_host": "192.168.88.20", "internal_port": 443}},
		[]map[string]any{{"id": "wan0", "current_address": "203.0.113.10/30"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.PortMappings[0].ExternalAddress != "203.0.113.10" {
		t.Fatalf("port mapping = %#v", compiled.PortMappings[0])
	}
}

func TestCompileConfigRejectsIPv6PortMap(t *testing.T) {
	_, err := CompileConfig(nil, []map[string]any{{"id": "bad-v6", "protocol": "tcp", "external_address": "2001:db8::10", "external_port": 8443, "internal_host": "192.168.88.20", "internal_port": 443}})
	if err == nil {
		t.Fatal("expected IPv6 NAT request to fail")
	}
}

func TestCompileConfigRejectsIPv6StaticNAT(t *testing.T) {
	_, err := CompileConfig([]map[string]any{{"id": "bad-v6", "external_address": "2001:db8::10", "internal_address": "192.168.88.20"}}, nil)
	if err == nil {
		t.Fatal("expected IPv6 NAT request to fail")
	}
}

func TestCompileConfigRejectsCommandCharacters(t *testing.T) {
	_, err := CompileConfig(nil, []map[string]any{{"id": "bad", "protocol": "tcp", "external_address": "203.0.113.10;show", "external_port": 8443, "internal_host": "192.168.88.20", "internal_port": 443}})
	if err == nil {
		t.Fatal("expected command characters to fail")
	}
}

func TestNAT_remains_IPv4_only_without_NAT66_surface(t *testing.T) {
	// Given / When
	compiled, err := CompileConfig(
		[]map[string]any{{"id": "static-v4", "external_address": "203.0.113.10", "internal_address": "192.168.88.10"}},
		[]map[string]any{{"id": "port-v4", "protocol": "tcp", "external_address": "203.0.113.10", "external_port": 8443, "internal_host": "192.168.88.20", "internal_port": 443}},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if len(compiled.StaticMappings) != 1 || len(compiled.PortMappings) != 1 {
		t.Fatalf("compiled NAT44 = %#v", compiled)
	}
	if _, err := CompileConfig([]map[string]any{{"id": "nat66", "external_address": "2001:db8::10", "internal_address": "fd00::10"}}, nil); err == nil {
		t.Fatal("IPv6 static mapping exposed a NAT66 surface")
	}
	if _, err := CompileConfig(nil, []map[string]any{{"id": "nat66-port", "protocol": "tcp", "external_address": "2001:db8::10", "external_port": 443, "internal_host": "fd00::10", "internal_port": 443}}); err == nil {
		t.Fatal("IPv6 port mapping exposed a NAT66 surface")
	}
}

func TestBindWANInterfacesResolvesRuntimeVPPNames(t *testing.T) {
	config := CompiledConfig{
		StaticMappings: []StaticMapping{{ID: "one-to-one", WANInterface: "wan-primary"}},
		PortMappings:   []PortMapping{{ID: "https", WANInterface: "wan-primary"}},
	}
	if err := BindWANInterfaces(&config, map[string]string{"wan-primary": "lyroute-eth1"}); err != nil {
		t.Fatal(err)
	}
	if config.StaticMappings[0].WANInterface != "lyroute-eth1" || config.PortMappings[0].WANInterface != "lyroute-eth1" {
		t.Fatalf("bound NAT config = %#v", config)
	}
}

func TestBindWANInterfacesRejectsUnknownWAN(t *testing.T) {
	config := CompiledConfig{PortMappings: []PortMapping{{ID: "https", WANInterface: "missing"}}}
	if err := BindWANInterfaces(&config, nil); err == nil {
		t.Fatal("unknown NAT WAN binding was accepted")
	}
}

func TestCompileConfigRejectsUnqualifiedHairpinPortRewrite(t *testing.T) {
	_, err := CompileConfig(nil, []map[string]any{{
		"id": "unsafe-hairpin", "protocol": "tcp", "external_address": "203.0.113.10",
		"external_port": 8443, "internal_host": "192.168.88.20", "internal_port": 443, "hairpin": true,
	}})
	if err == nil || !strings.Contains(err.Error(), "identical external_port and internal_port") {
		t.Fatalf("hairpin port rewrite error = %v", err)
	}
}
