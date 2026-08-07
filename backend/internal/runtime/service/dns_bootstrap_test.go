package service

import (
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/dns"
)

func TestBuiltinDNSBootstrapProfiles(t *testing.T) {
	domestic := BuiltinDNSBootstrap("domestic")
	foreign := BuiltinDNSBootstrap("foreign")
	if domestic.Profile != DNSBootstrapDomestic || len(domestic.BootstrapServers) != 5 {
		t.Fatalf("domestic defaults = %#v", domestic)
	}
	if foreign.Profile != DNSBootstrapForeign || len(foreign.BootstrapServers) != 5 {
		t.Fatalf("foreign defaults = %#v", foreign)
	}
}

func TestRenderSmartDNSBundleRendersPerUpstreamBootstrap(t *testing.T) {
	compiled, err := dns.CompilePolicy(dns.NewPolicy(dns.Reject(), nil), nil)
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	artifacts, err := RenderSmartDNS(SmartDNSPlan{
		ID:     "bootstrap-test",
		Render: compiled.RenderSmartDNS(),
		Upstreams: []SmartDNSUpstream{{
			ID:               "dns-cn",
			Servers:          []string{"https://dns.alidns.com/dns-query"},
			BootstrapServers: []string{"223.5.5.5", "223.6.6.6"},
			Interface:        "lydnsh123456",
		}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	content := artifacts[0].Content
	for _, required := range []string{
		"server 223.5.5.5 -group dns-cn-bootstrap -exclude-default-group -interface lydnsh123456",
		"server 223.6.6.6 -group dns-cn-bootstrap -exclude-default-group -interface lydnsh123456",
		"nameserver /dns.alidns.com/dns-cn-bootstrap",
		"server-https https://dns.alidns.com/dns-query -group dns-cn -exclude-default-group -interface lydnsh123456",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("rendered config missing %q:\n%s", required, content)
		}
	}
}
