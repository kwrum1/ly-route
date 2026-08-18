package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/dns"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/vpp"
)

func TestRenderedArtifactHashesAndRedactsAuditSummary(t *testing.T) {
	artifact := NewArtifact(Xray, "/etc/xray/proxy.json", `{"url":"vless://user@example","password":"super-secret"}`, "restart")
	if artifact.ContentHash == "" {
		t.Fatal("content hash is empty")
	}
	for _, forbidden := range []string{"vless://", "super-secret", "password"} {
		if strings.Contains(strings.ToLower(artifact.AuditSummary), forbidden) {
			t.Fatalf("audit summary leaked %q: %s", forbidden, artifact.AuditSummary)
		}
	}
}

func TestSmartDNSUpstreamRoutingSelectorKeepsMarkAndInterface(t *testing.T) {
	var rendered strings.Builder
	writeSmartDNSRoutingSelector(&rendered, SmartDNSUpstream{SocketMark: 0x400dd20, Interface: "lydnsh6e1de0"})

	if got, want := rendered.String(), " -set-mark 67165472 -interface lydnsh6e1de0"; got != want {
		t.Fatalf("SmartDNS routing selector = %q, want %q", got, want)
	}
}

func TestRenderGatewayNftablesCaptureInterceptsTCPAndUDPDNSOnlyOnExplicitLAN(t *testing.T) {
	artifacts, err := RenderGatewayNftablesCapture(proxy.NftablesCapturePlan{}, DNSInterceptionPlan{LANInterfaces: []string{"lan1", "lan0", "lan0"}, ListenPort: 53})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts=%#v", artifacts)
	}
	content := artifacts[0].Content
	for _, wanted := range []string{
		"add table inet ly_route_dns_capture",
		"flush table inet ly_route_dns_capture",
		"table inet ly_route_dns_capture",
		"type nat hook prerouting priority -100",
		`iifname "lan0" udp dport 53 counter redirect to :53`,
		`iifname "lan0" tcp dport 53 counter redirect to :53`,
		`iifname "lan1" udp dport 53 counter redirect to :53`,
	} {
		if !strings.Contains(content, wanted) {
			t.Fatalf("nftables content missing %q:\n%s", wanted, content)
		}
	}
	if strings.Count(content, `iifname "lan0" udp dport 53`) != 1 || strings.Contains(content, "0.0.0.0/0") {
		t.Fatalf("DNS interception is duplicated or not LAN-scoped:\n%s", content)
	}
	if strings.Contains(content, "flush ruleset") || strings.Contains(content, "destroy table") {
		t.Fatalf("nftables renderer used a destructive or unsupported command:\n%s", content)
	}
}

func TestRenderGatewayNftablesCaptureRejectsUnsafeLANInterface(t *testing.T) {
	if _, err := RenderGatewayNftablesCapture(proxy.NftablesCapturePlan{}, DNSInterceptionPlan{LANInterfaces: []string{"lan0; flush ruleset"}}); err == nil {
		t.Fatal("unsafe LAN interface was accepted")
	}
}

func TestExpectedNftablesRulesAllowsProxyOnlyCapture(t *testing.T) {
	content := `table inet ly_route_proxy_capture {
  chain proxy_prerouting {
    iifname "proxy-in" meta l4proto tcp meta mark set 0x101 tproxy to :20001 accept
    iifname "proxy-in" meta l4proto udp meta mark set 0x101 tproxy to :20001 accept
  }
}`
	family, table, rules, err := expectedNftablesRules(content)
	if err != nil {
		t.Fatal(err)
	}
	if family != "inet" || table != "ly_route_proxy_capture" || len(rules) != 2 {
		t.Fatalf("expected nftables rules = %s %s %#v", family, table, rules)
	}
}

func TestExpectedNftablesRulesRejectsDNSAfterTProxy(t *testing.T) {
	content := `table inet ly_route_proxy_capture {
  chain proxy_prerouting {
    iifname "proxy-in" meta l4proto tcp meta mark set 0x101 tproxy to :20001 accept
    iifname "proxy-in" meta l4proto udp meta mark set 0x101 tproxy to :20001 accept
    iifname "lan0" udp dport 53 redirect to :53
    iifname "lan0" tcp dport 53 redirect to :53
  }
}`
	if _, _, _, err := expectedNftablesRules(content); err == nil || !strings.Contains(err.Error(), "DNS-before-TProxy") {
		t.Fatalf("expected DNS-before-TProxy ordering failure, got %v", err)
	}
}

func TestNormalizedNftablesCanonicalizesPaddedMarks(t *testing.T) {
	want := normalizedNftables(`meta mark set 0x100475 accept`)
	got := normalizedNftables(`meta mark set 0x00100475 accept`)
	if got != want {
		t.Fatalf("normalized mark = %q, want %q", got, want)
	}
}

func TestRequirePolicyRuleAcceptsOmittedFullMask(t *testing.T) {
	expected := policyRuleExpectation{Mark: "0x2006cc", Mask: "0xffffffff", Table: 30756, Priority: 2056}
	output := `[{"priority":2056,"src":"all","fwmark":"0x2006cc","table":"30756"}]`
	if err := requirePolicyRule(output, expected); err != nil {
		t.Fatal(err)
	}
	expected.Mask = "0xffff"
	if err := requirePolicyRule(output, expected); err == nil {
		t.Fatal("omitted non-default policy mask was accepted")
	}
}

func TestRequirePolicyRuleAcceptsBoundSource(t *testing.T) {
	expected := policyRuleExpectation{Source: "198.19.28.18", Table: 50716, Priority: 18042}
	output := "[{\"priority\":18042,\"src\":\"198.19.28.18\",\"table\":50716}]"
	if err := requirePolicyRule(output, expected); err != nil {
		t.Fatal(err)
	}
	expected.Source = "198.19.28.19"
	if err := requirePolicyRule(output, expected); err == nil {
		t.Fatal("incorrect source policy rule was accepted")
	}
}

func TestParsePolicyRoutingExpectationKeepsEachRouteTable(t *testing.T) {
	content := strings.Join([]string{
		"ip rule add fwmark 0x100475/0xffffffff table 23653 priority 1253",
		"ip route replace local 0.0.0.0/0 dev lo table 23653",
		"ip rule add fwmark 0x2006cc/0xffffffff table 30756 priority 2056",
		"ip route replace default via 198.18.17.217 dev proxy-out table 30756",
		"ip route replace 10.1.18.0/24 via 198.18.17.213 dev proxy-in",
	}, "\n")
	expected, err := parsePolicyRoutingExpectation(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.Rules) != 2 || len(expected.Routes) != 3 {
		t.Fatalf("expectation = %#v", expected)
	}
	if expected.Routes[0].Table != 23653 || expected.Routes[0].Type != "local" || expected.Routes[1].Table != 30756 || expected.Routes[2].Table != 254 {
		t.Fatalf("route tables = %#v", expected.Routes)
	}
}

func TestParsePolicyRoutingExpectationAcceptsExplicitReset(t *testing.T) {
	expected, err := parsePolicyRoutingExpectation(LinuxRoutingResetArtifact().Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.Rules) != 0 || len(expected.Routes) != 0 {
		t.Fatalf("reset expectation = %#v", expected)
	}
}

func TestRuntimeAppliesArtifactsInServiceOrder(t *testing.T) {
	controller := &fakeController{}
	runtime := Runtime{Controller: controller}
	err := runtime.Apply(context.Background(), []RenderedArtifact{
		NewArtifact(Xray, "/etc/xray/config.json", "{}", "restart"),
		NewArtifact(SmartDNS, "/etc/smartdns/domain.conf", "server 1.1.1.1", "reload"),
		NewArtifact(Kea, "/etc/kea/kea-dhcp4.json", "{}", "restart"),
		NewArtifact(Nftables, "/etc/nftables.conf", "flush ruleset", "reload"),
		NewArtifact(LinuxRouting, "/var/lib/ly-route/policy-routing/apply.sh", "#!/bin/sh\n", "restart"),
		NewArtifact(VPP, "/var/lib/ly-route/vpp/operations.json", `{"operations":[]}`, "restart"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "vpp,linux-routing,smartdns,kea,xray,nftables"
	if got := strings.Join(controller.applied, ","); got != want {
		t.Fatalf("apply order = %s, want %s", got, want)
	}
}

func TestRuntimeRollsBackInReverseServiceOrder(t *testing.T) {
	controller := &fakeController{}
	runtime := Runtime{Controller: controller}
	err := runtime.Rollback(context.Background(), []RenderedArtifact{
		NewArtifact(SmartDNS, "/etc/smartdns/domain.conf", "server 1.1.1.1", "reload"),
		NewArtifact(Xray, "/etc/xray/config.json", "{}", "restart"),
		NewArtifact(Nftables, "/etc/nftables.conf", "flush ruleset", "reload"),
		NewArtifact(LinuxRouting, "/var/lib/ly-route/policy-routing/apply.sh", "#!/bin/sh\n", "restart"),
		NewArtifact(VPP, "/var/lib/ly-route/vpp/operations.json", `{"operations":[]}`, "restart"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(controller.rolledBack, ","), "nftables,xray,smartdns,linux-routing,vpp"; got != want {
		t.Fatalf("rollback order = %s, want %s", got, want)
	}
}

func TestRuntimeRollbackContinuesAndJoinsFailures(t *testing.T) {
	// Given
	first := errors.New("vpp rollback failed")
	second := errors.New("smartdns rollback failed")
	controller := &fakeController{rollbackErrs: map[ServiceName]error{VPP: first, SmartDNS: second}}
	runtime := Runtime{Controller: controller}

	// When
	err := runtime.Rollback(context.Background(), []RenderedArtifact{
		NewArtifact(SmartDNS, "/etc/smartdns/domain.conf", "{}", "reload"),
		NewArtifact(VPP, "/var/lib/ly-route/vpp/operations.json", "{}", "restart"),
	})

	// Then
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("rollback error = %v, want both controller failures", err)
	}
	if got, want := strings.Join(controller.rolledBack, ","), "smartdns,vpp"; got != want {
		t.Fatalf("rollback order = %s, want %s", got, want)
	}
}

func TestRuntimeReturnsControllerFailures(t *testing.T) {
	runtime := Runtime{Controller: &fakeController{applyErr: errors.New("reload failed")}}
	err := runtime.Apply(context.Background(), []RenderedArtifact{NewArtifact(Kea, "/etc/kea/kea-dhcp4.json", "{}", "restart")})
	if err == nil || !strings.Contains(err.Error(), "reload failed") {
		t.Fatalf("apply error = %v, want reload failure", err)
	}
}

func TestRuntimeHealthCheckUsesControllerStatus(t *testing.T) {
	runtime := Runtime{Controller: &fakeController{health: map[ServiceName]Health{SmartDNS: {Service: SmartDNS, Available: true}}}}
	results, err := runtime.HealthCheck(context.Background(), SmartDNS)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Service != SmartDNS || !results[0].Available {
		t.Fatalf("health = %#v", results)
	}
}

func TestRenderServiceArtifactsForRuntimeTargets(t *testing.T) {
	compiledDNS, err := dns.CompilePolicy(dns.NewPolicy(dns.Reject(), []dns.Rule{
		{ID: "direct-lan", Domains: []string{"lan.example"}, Outcome: dns.Direct()},
		{ID: "reject-ads", Domains: []string{"ads.example"}, Outcome: dns.Reject()},
		{ID: "fixed-smoke", Domains: []string{"smoke.example"}, Outcome: dns.FixedAnswer("203.0.113.10")},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	smartdnsArtifacts, err := RenderSmartDNS(SmartDNSPlan{
		ID:        "default",
		Render:    compiledDNS.RenderSmartDNS(),
		Upstreams: []SmartDNSUpstream{{ID: "dns-direct-default", Servers: []string{"1.1.1.1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(smartdnsArtifacts) != 2 || smartdnsArtifacts[0].Service != SmartDNS || smartdnsArtifacts[0].Path != "/etc/smartdns/conf.d/ly-route-active.conf" || smartdnsArtifacts[1].Path != "/etc/ly-route/dns-source-routes.conf" || !strings.Contains(smartdnsArtifacts[0].Content, "address /-.ads.example/#") || !strings.Contains(smartdnsArtifacts[0].Content, "address /-.smoke.example/203.0.113.10") {
		t.Fatalf("smartdns artifacts = %#v", smartdnsArtifacts)
	}

	keaArtifacts, err := RenderKeaDHCP4(KeaDHCP4Plan{ID: "lan", InterfaceID: "eth1", Subnet: "192.168.88.0/24", Pools: []string{"192.168.88.100-192.168.88.199"}, Routers: []string{"192.168.88.1"}, NameServers: []string{"192.168.88.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(keaArtifacts) != 1 || keaArtifacts[0].Service != Kea || keaArtifacts[0].Path != "/etc/kea/kea-dhcp4.conf" || !strings.Contains(keaArtifacts[0].Content, "Dhcp4") || !strings.Contains(keaArtifacts[0].Content, "eth1") || !strings.Contains(keaArtifacts[0].Content, "option-data") || !strings.Contains(keaArtifacts[0].Content, "domain-name-servers") || strings.Contains(keaArtifacts[0].Content, "loggers") {
		t.Fatalf("kea artifacts = %#v", keaArtifacts)
	}

	compiledProxy, err := proxy.CompileEgress(proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound"))
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.BindServiceNetwork(&compiledProxy, "192.0.2.1 lyroute-wan0", []string{"192.168.88.0/24"}, 1460); err != nil {
		t.Fatal(err)
	}
	dnsNetwork, err := vpp.DNSServiceNetworkForUpstreamID("dns-proxy", compiledProxy.ID, []string{"1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := vpp.BindDNSServiceNetwork(&dnsNetwork, compiledProxy.ServiceNetwork.IngressHostAddress+" "+compiledProxy.ServiceNetwork.IngressVPPInterface, 1460); err != nil {
		t.Fatal(err)
	}
	xrayArtifacts, err := RenderXray(compiledProxy)
	if err != nil {
		t.Fatal(err)
	}
	if len(xrayArtifacts) != 1 || xrayArtifacts[0].Service != Xray || !strings.Contains(xrayArtifacts[0].Content, "dokodemo-door") {
		t.Fatalf("xray artifacts = %#v", xrayArtifacts)
	}
	vppArtifacts, err := RenderVPPOperations(compiledProxyDataplaneOperationsForTest(compiledProxy))
	if err != nil {
		t.Fatal(err)
	}
	if len(vppArtifacts) != 1 || vppArtifacts[0].Service != VPP || vppArtifacts[0].Path != "/var/lib/ly-route/vpp/operations.json" || !strings.Contains(vppArtifacts[0].Content, "vpp.proxy-service.network") {
		t.Fatalf("vpp artifacts = %#v", vppArtifacts)
	}
	routingArtifacts, err := RenderLinuxPolicyRouting(compiledProxy.LinuxPolicyRouting, nat.BehaviorEndpointDependent, dnsNetwork)
	if err != nil {
		t.Fatal(err)
	}
	if len(routingArtifacts) != 1 || !strings.Contains(routingArtifacts[0].Content, "ensure_tap") || !strings.Contains(routingArtifacts[0].Content, "wait_vpp_underlay lyroute-wan0") || !strings.Contains(routingArtifacts[0].Content, "set interface mtu packet 1460") || !strings.Contains(routingArtifacts[0].Content, "mtu 1460 up") || !strings.Contains(routingArtifacts[0].Content, "set interface ip address del "+compiledProxy.ServiceNetwork.EgressVPPInterface+" "+compiledProxy.ServiceNetwork.EgressCIDR) || !strings.Contains(routingArtifacts[0].Content, "nat44 plugin enable") || !strings.Contains(routingArtifacts[0].Content, "set interface nat44 in "+compiledProxy.ServiceNetwork.EgressVPPInterface+" out lyroute-wan0 output-feature del") || !strings.Contains(routingArtifacts[0].Content, "set interface nat44 in "+compiledProxy.ServiceNetwork.EgressVPPInterface+" out lyroute-wan0\n") || !strings.Contains(routingArtifacts[0].Content, "ip route replace 192.168.88.0/24") || !strings.Contains(routingArtifacts[0].Content, "net.ipv4.conf."+compiledProxy.ServiceNetwork.IngressHostInterface+".accept_local=1") || !strings.Contains(routingArtifacts[0].Content, "net.ipv4.ip_nonlocal_bind=1") || !strings.Contains(routingArtifacts[0].Content, "IN_TAP_MAC") || !strings.Contains(routingArtifacts[0].Content, "set ip neighbor "+compiledProxy.ServiceNetwork.IngressVPPInterface+" "+compiledProxy.ServiceNetwork.IngressHostAddress) || !strings.Contains(routingArtifacts[0].Content, "set ip neighbor "+compiledProxy.ServiceNetwork.EgressVPPInterface+" "+compiledProxy.ServiceNetwork.EgressHostAddress) || !strings.Contains(routingArtifacts[0].Content, "ip route add "+dnsNetwork.HostAddress+"/32 via "+dnsNetwork.HostAddress+" "+dnsNetwork.VPPInterface) || !strings.Contains(routingArtifacts[0].Content, "vpp-return "+dnsNetwork.HostAddress+" "+dnsNetwork.VPPInterface) || !strings.Contains(routingArtifacts[0].Content, "lypxhin*|lypxhout*") || !strings.Contains(routingArtifacts[0].Content, "lydnsh*") || !strings.Contains(routingArtifacts[0].Content, "ip link delete dev \"$host_if\"") {
		t.Fatalf("proxy service routing artifacts = %#v", routingArtifacts)
	}

	pppoeArtifacts, err := RenderPPPoE(PPPoEPeer{ID: "wan", Interface: "eth0", Username: "user", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pppoeArtifacts) != 1 || pppoeArtifacts[0].Service != PPPd {
		t.Fatalf("pppoe artifacts = %#v", pppoeArtifacts)
	}
	if pppoeArtifacts[0].Path != "/etc/ly-route/pppoe/ly-route-wan.json" {
		t.Fatalf("pppoe peer path = %q", pppoeArtifacts[0].Path)
	}
	if strings.Contains(strings.ToLower(pppoeArtifacts[0].AuditSummary), "secret") {
		t.Fatalf("pppoe secret leaked in audit summary: %s", pppoeArtifacts[0].AuditSummary)
	}
	if !strings.Contains(pppoeArtifacts[0].Content, `"wan_interface": "lyroute-eth0"`) || !strings.Contains(pppoeArtifacts[0].Content, `"control_interface": "lyppp-`) || !strings.Contains(pppoeArtifacts[0].Content, `"mru": 1492`) || !strings.Contains(pppoeArtifacts[0].Content, `"password": "secret"`) || strings.Contains(pppoeArtifacts[0].Content, "rp-pppoe") {
		t.Fatalf("pppoe peer content = %q", pppoeArtifacts[0].Content)
	}

	if compiledProxy.NftablesCapture.IngressInterface != compiledProxy.ServiceNetwork.IngressHostInterface || compiledProxy.LinuxPolicyRouting.Network.EgressID != compiledProxy.ID {
		t.Fatalf("proxy service handoff plans are incomplete: %#v %#v", compiledProxy.NftablesCapture, compiledProxy.LinuxPolicyRouting)
	}
}

func TestRenderSmartDNSBundlePinsDNSPolicyToSelectedWANAndBoundsCache(t *testing.T) {
	policy := dns.NewPolicy(dns.Reject(), []dns.Rule{{ID: "fixed-wan", Domains: []string{"updates.example"}, Outcome: dns.Outcome{Kind: dns.OutcomeDirect, WANEgressID: "wan-primary"}}})
	compiled, err := dns.CompilePolicy(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderSmartDNSBundle([]SmartDNSPlan{{
		ID:        "fixed-wan",
		Render:    compiled.RenderSmartDNS(),
		Upstreams: []SmartDNSUpstream{{ID: "dns-wan-primary", Servers: []string{"9.9.9.9", "149.112.112.112"}, Interface: "wan0", WANEgressID: "wan-primary"}},
		Cache:     SmartDNSCache{Size: 32768, TTLMin: 60, TTLMax: 600, Prefetch: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	content := artifacts[0].Content
	for _, required := range []string{"server 9.9.9.9 -group dns-wan-primary -exclude-default-group -interface wan0", "nameserver /-.updates.example/dns-wan-primary", "ipset /-.updates.example/lyroute_dns_fixed-wan", "address #", "cache-persist no", "ipset-timeout yes", "cache-size 32768", "rr-ttl-min 60", "rr-ttl-max 600", "prefetch-domain yes"} {
		if !strings.Contains(content, required) {
			t.Fatalf("smartdns bundle missing %q: %s", required, content)
		}
	}
	if strings.Contains(content, "address /-.updates.example/-") {
		t.Fatalf("direct SmartDNS rule must not suppress upstream answers: %s", content)
	}
}

func TestRenderSmartDNSBundleSupportsDoHAndFixedWANMiss(t *testing.T) {
	policy := dns.NewPolicy(dns.Outcome{Kind: dns.OutcomeDirect, UpstreamID: "dns-cf", WANEgressID: "proxy"}, []dns.Rule{{
		ID:      "cn",
		Domains: []string{"cn.example"},
		Outcome: dns.Outcome{Kind: dns.OutcomeDirect, UpstreamID: "dns-ali", WANEgressID: "pppoe"},
	}})
	compiled, err := dns.CompilePolicy(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderSmartDNSBundle([]SmartDNSPlan{{
		ID:     "active",
		Render: compiled.RenderSmartDNS(),
		Upstreams: []SmartDNSUpstream{
			{ID: "dns-ali", Servers: []string{"https://dns.alidns.com/dns-query"}, BootstrapServers: []string{"223.5.5.5"}, ResolvedHostIPs: map[string][]string{"dns.alidns.com": {"223.6.6.6"}}, Interface: "ppp0", WANEgressID: "pppoe"},
			{ID: "dns-cf", Servers: []string{"https://cloudflare-dns.com/dns-query"}, BootstrapServers: []string{"1.1.1.1"}, ResolvedHostIPs: map[string][]string{"cloudflare-dns.com": {"1.1.1.1"}}, Interface: "proxy0", WANEgressID: "proxy"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	content := artifacts[0].Content
	for _, required := range []string{
		"server-https https://223.6.6.6/dns-query -group dns-ali -exclude-default-group -host-name dns.alidns.com -http-host dns.alidns.com -tls-host-verify dns.alidns.com -interface ppp0",
		"server-https https://1.1.1.1/dns-query -group dns-cf -exclude-default-group -host-name cloudflare-dns.com -http-host cloudflare-dns.com -tls-host-verify cloudflare-dns.com -interface proxy0",
		"nameserver /./dns-cf",
		"nameserver /dns.alidns.com/dns-ali-bootstrap",
		"nameserver /cloudflare-dns.com/dns-cf-bootstrap",
		"ipset /-.cn.example/lyroute_dns_cn",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("DoH/fixed-WAN SmartDNS output missing %q: %s", required, content)
		}
	}
	if strings.Index(content, "nameserver /dns.alidns.com/dns-ali-bootstrap") < strings.Index(content, "nameserver /./dns-cf") {
		t.Fatalf("Bootstrap hostname rule must follow the global DNS miss rule: %s", content)
	}
}

func TestRenderSmartDNSBundleScopesSourcePrefixWithoutBroadeningPolicy(t *testing.T) {
	policy := dns.NewPolicy(dns.Reject(), []dns.Rule{
		{ID: "scoped", SourcePrefixes: []string{"192.0.2.0/24"}, Domains: []string{"updates.example"}, Outcome: dns.Outcome{Kind: dns.OutcomeDirect, WANEgressID: "wan-primary"}},
		{ID: "scoped-secondary", SourcePrefixes: []string{"198.51.100.0/24"}, Domains: []string{"packages.example"}, Outcome: dns.Outcome{Kind: dns.OutcomeDirect, WANEgressID: "wan-primary"}},
	})
	compiled, err := dns.CompilePolicy(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderSmartDNSBundle([]SmartDNSPlan{{
		ID:        "scoped",
		Render:    compiled.RenderSmartDNS(),
		Upstreams: []SmartDNSUpstream{{ID: "dns-wan-primary", Servers: []string{"9.9.9.9"}, Interface: "wan0", WANEgressID: "wan-primary"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[1].Path != "/etc/ly-route/dns-source-routes.conf" {
		t.Fatalf("smartdns source routing artifacts = %#v", artifacts)
	}
	content := artifacts[0].Content
	for _, required := range []string{"cache-persist no", "bind 127.0.0.1:12000 -group lyroute-client-", "bind-tcp 127.0.0.1:12000 -group lyroute-client-", "bind 127.0.0.1:12001 -group lyroute-client-", "bind-tcp 127.0.0.1:12001 -group lyroute-client-", "group-begin lyroute-client-", "-inherit none", "nameserver /-.updates.example/dns-wan-primary", "ipset /-.updates.example/lyroute_dns_scoped", "group-end"} {
		if !strings.Contains(content, required) {
			t.Fatalf("smartdns source-scoped bundle missing %q: %s", required, content)
		}
	}
	udpLine := strings.SplitN(strings.SplitN(content, "bind 127.0.0.1:12000 -group ", 2)[1], "\n", 2)[0]
	tcpLine := strings.SplitN(strings.SplitN(content, "bind-tcp 127.0.0.1:12000 -group ", 2)[1], "\n", 2)[0]
	udpFields := strings.Fields(udpLine)
	tcpFields := strings.Fields(tcpLine)
	if len(udpFields) != 3 || len(tcpFields) != 3 || udpFields[0] != tcpFields[0] || udpFields[1] != "-no-cache" || tcpFields[1] != "-no-cache" || udpFields[2] != "-no-speed-check" || tcpFields[2] != "-no-speed-check" {
		t.Fatalf("smartdns source-scoped listeners must bypass shared cache and speed selection: %s", content)
	}
	groupAt := strings.Index(content, "group-begin "+udpFields[0]+" -inherit none")
	groupEndAt := -1
	if groupAt >= 0 {
		groupEndAt = strings.Index(content[groupAt:], "group-end\n") + groupAt
	}
	udpAt := strings.Index(content, "bind 127.0.0.1:12000 -group "+udpLine)
	tcpAt := strings.Index(content, "bind-tcp 127.0.0.1:12000 -group "+tcpLine)
	if groupAt < 0 || groupEndAt < groupAt || udpAt < groupEndAt || tcpAt < udpAt || strings.Contains(content, "client-rules ") {
		t.Fatalf("smartdns UDP/TCP listeners are not explicitly bound to one policy group: %s", content)
	}
	if got, want := artifacts[1].Content, "# source-prefix match-kind domain smartdns-port\n192.0.2.0/24 exact updates.example 12000\n198.51.100.0/24 exact packages.example 12001\n"; got != want {
		t.Fatalf("smartdns source route map = %q, want %q", got, want)
	}
}

func TestRenderSmartDNSBundleExpandsSuffixesAndDomainSetsForSourceRouting(t *testing.T) {
	policy := dns.NewPolicy(dns.Reject(), []dns.Rule{{
		ID:             "scoped-domains",
		SourcePrefixes: []string{"192.0.2.0/24"},
		DomainSuffixes: []string{"video.example"},
		DomainSetIDs:   []string{"managed-domains"},
		Outcome:        dns.Reject(),
	}})
	compiled, err := dns.CompilePolicy(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderSmartDNSBundle([]SmartDNSPlan{{
		ID:         "scoped-domains",
		Render:     compiled.RenderSmartDNS(),
		DomainSets: map[string][]string{"managed-domains": {"portal.example", ".updates.example"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	setName := smartDNSDomainSetName("managed-domains")
	for _, required := range []string{"domain-set -name " + setName + " -type list -file /etc/smartdns/domain-sets/" + setName + ".list", "address /video.example/#", "address /domain-set:" + setName + "/#"} {
		if !strings.Contains(artifacts[0].Content, required) {
			t.Fatalf("smartdns content missing %q: %s", required, artifacts[0].Content)
		}
	}
	if strings.Contains(artifacts[0].Content, "portal.example/#") || strings.Contains(artifacts[0].Content, "updates.example/#") {
		t.Fatalf("smartdns domain set was expanded into per-domain rules: %s", artifacts[0].Content)
	}
	wantRoutes := "# source-prefix match-kind domain smartdns-port\n" +
		"192.0.2.0/24 suffix video.example 12000\n" +
		"192.0.2.0/24 exact portal.example 12000\n" +
		"192.0.2.0/24 suffix updates.example 12000\n"
	if artifacts[1].Content != wantRoutes {
		t.Fatalf("source routes = %q, want %q", artifacts[1].Content, wantRoutes)
	}
	if len(artifacts) != 3 || artifacts[2].Path != "/etc/smartdns/domain-sets/"+setName+".list" || artifacts[2].Content != "-.portal.example\nupdates.example\n" {
		t.Fatalf("domain set artifact = %#v", artifacts)
	}
	if _, err := RenderSmartDNSBundle([]SmartDNSPlan{{ID: "missing-set", Render: compiled.RenderSmartDNS()}}); err == nil || !strings.Contains(err.Error(), "domain set \"managed-domains\" is unavailable") {
		t.Fatalf("missing domain set error = %v", err)
	}
}

func TestRenderSmartDNSBundleRejectsMissingPinnedUpstream(t *testing.T) {
	policy := dns.NewPolicy(dns.Reject(), []dns.Rule{{ID: "fixed-wan", Domains: []string{"updates.example"}, Outcome: dns.Outcome{Kind: dns.OutcomeDirect, WANEgressID: "wan-primary"}}})
	compiled, err := dns.CompilePolicy(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RenderSmartDNSBundle([]SmartDNSPlan{{ID: "fixed-wan", Render: compiled.RenderSmartDNS()}}); err == nil {
		t.Fatal("RenderSmartDNSBundle succeeded with an unresolved WAN DNS route")
	}
}

func TestRenderKeaDHCP4IncludesLifetimeAndHostReservationsOnlyInDhcp4(t *testing.T) {
	artifacts, err := RenderKeaDHCP4(KeaDHCP4Plan{ID: "lan", InterfaceID: "eth1", Subnet: "192.168.88.0/24", Pools: []string{"192.168.88.100-192.168.88.199"}, Routers: []string{"192.168.88.1"}, NameServers: []string{"192.168.88.1"}, LeaseTime: 43200, Reservations: []KeaReservation{{HWAddress: "00:11:22:33:44:66", IPAddress: "192.168.88.101", Hostname: "workstation"}}})
	if err != nil {
		t.Fatal(err)
	}
	content := artifacts[0].Content
	for _, required := range []string{"valid-lifetime", "43200", "reservations", "hw-address", "00:11:22:33:44:66", "ip-address", "192.168.88.101", "workstation", "domain-name-servers", "routers"} {
		if !strings.Contains(content, required) {
			t.Fatalf("kea content missing %q: %s", required, content)
		}
	}
	for _, forbidden := range []string{"Dhcp6", "subnet6", "router-advertisement", "RA"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("kea content should not contain %q: %s", forbidden, content)
		}
	}
}

func TestRenderKeaDHCP4ConfigMergesMultipleSubnets(t *testing.T) {
	artifacts, err := RenderKeaDHCP4Config([]KeaDHCP4Plan{
		{ID: "lan", InterfaceID: "eth1", Subnet: "192.168.88.0/24", Pools: []string{"192.168.88.100-192.168.88.199"}, Routers: []string{"192.168.88.1"}, NameServers: []string{"192.168.88.1"}},
		{ID: "guest", InterfaceID: "eth2", Subnet: "192.168.20.0/24", Pools: []string{"192.168.20.100-192.168.20.199"}, Routers: []string{"192.168.20.1"}, NameServers: []string{"192.168.20.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Service != Kea || artifacts[0].Path != "/etc/kea/kea-dhcp4.conf" {
		t.Fatalf("kea artifacts = %#v", artifacts)
	}
	content := artifacts[0].Content
	for _, required := range []string{"eth1", "eth2", "192.168.88.0/24", "192.168.20.0/24", `"id": 1`, `"id": 2`} {
		if !strings.Contains(content, required) {
			t.Fatalf("kea merged config missing %q: %s", required, content)
		}
	}
	if strings.Count(content, `"Dhcp4"`) != 1 || strings.Count(content, `"subnet4"`) != 1 {
		t.Fatalf("kea config should be one native Dhcp4 document: %s", content)
	}
}

func TestRenderPPPoERejectsUnsafeProductionConfig(t *testing.T) {
	for name, peer := range map[string]PPPoEPeer{
		"unsafe id":        {ID: "wan;rm", Interface: "eth0", Username: "user", Password: "secret"},
		"unsafe interface": {ID: "wan", Interface: "eth0\npty bad", Username: "user", Password: "secret"},
		"low mtu":          {ID: "wan", Interface: "eth0", Username: "user", Password: "secret", MTU: 1200},
		"high mru":         {ID: "wan", Interface: "eth0", Username: "user", Password: "secret", MRU: 9000},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RenderPPPoE(peer); err == nil {
				t.Fatal("RenderPPPoE succeeded, want validation failure")
			}
		})
	}
}

func TestRenderPPPoEConfigCreatesIndependentMultiWANPeersAndNativeSecrets(t *testing.T) {
	artifacts, err := RenderPPPoEConfig([]PPPoEPeer{
		{ID: "wan-blue", Interface: "eth7", Username: "same-account-text", Password: "blue-secret", NATInsideInterfaces: []string{"lyroute-lan0"}},
		{ID: "wan-green", Interface: "eth8", Username: "same-account-text", Password: "green-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("PPPoE artifacts = %d, want two native client configs", len(artifacts))
	}
	if artifacts[0].Path != "/etc/ly-route/pppoe/ly-route-wan-blue.json" || artifacts[1].Path != "/etc/ly-route/pppoe/ly-route-wan-green.json" {
		t.Fatalf("PPPoE peer paths = %q, %q", artifacts[0].Path, artifacts[1].Path)
	}
	for _, artifact := range artifacts {
		if strings.Contains(artifact.AuditSummary, "blue-secret") || strings.Contains(artifact.AuditSummary, "green-secret") {
			t.Fatalf("native client secret leaked through audit: %s", artifact.AuditSummary)
		}
		if !strings.Contains(artifact.Content, `"vppctl": "vppctl"`) || strings.Contains(artifact.Content, "/usr/local/bin/ly-route-vppctl") {
			t.Fatalf("native client config does not use the production VPP CLI from PATH: %s", artifact.Content)
		}
		if !strings.Contains(artifact.Content, `"username": "same-account-text"`) {
			t.Fatalf("independent WAN account text was not preserved: %s", artifact.Content)
		}
	}
	if !strings.Contains(artifacts[0].Content, "nat_inside_interfaces") || !strings.Contains(artifacts[0].Content, "lyroute-lan0") {
		t.Fatalf("PPPoE artifact omitted NAT inside interfaces: %s", artifacts[0].Content)
	}
	if strings.Contains(artifacts[0].Content, `"session_interface"`) || strings.Contains(artifacts[1].Content, `"session_interface"`) {
		t.Fatalf("PPPoE artifacts must not pin VPP-native session names: %s %s", artifacts[0].Content, artifacts[1].Content)
	}
	units := applyUnits(PPPd, artifacts)
	if got, want := strings.Join(units, ","), "ly-route-pppoe@ly-route-wan-blue.service,ly-route-pppoe@ly-route-wan-green.service"; got != want {
		t.Fatalf("PPPoE units = %q, want %q", got, want)
	}
}

func TestPPPoEStatusModelsLifecycleAndRouteReadiness(t *testing.T) {
	peer := PPPoEPeer{ID: "wan", Interface: "ppp0", Username: "user"}
	for _, state := range []PPPoEState{PPPoEDisconnected, PPPoEConnecting, PPPoEConnected, PPPoEFailed} {
		status, err := NewPPPoEStatus(peer, state, "", "", 100, "")
		if err != nil {
			t.Fatalf("status %s: %v", state, err)
		}
		if status.State != state || status.RouteReady {
			t.Fatalf("status = %#v, want state %s without route readiness", status, state)
		}
	}
	connected, err := NewPPPoEStatus(peer, PPPoEConnected, "198.51.100.44", "2001:db8::44", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if !connected.RouteReady || connected.AssignedIPv4 != "198.51.100.44" || connected.AssignedIPv6 != "2001:db8::44" || connected.VPPRouteHandoff != "vpp.fib.route" {
		t.Fatalf("connected status = %#v, want assigned addresses and VPP route readiness", connected)
	}
	failed, err := NewPPPoEStatus(peer, PPPoEFailed, "", "", 100, "authentication failed")
	if err != nil {
		t.Fatal(err)
	}
	if failed.RouteReady || failed.LastError != "authentication failed" {
		t.Fatalf("failed status = %#v, want no route and last error", failed)
	}
}

func TestPPPoEVPPRouteHandoffUsesVPPRouteTableOnlyWhenReady(t *testing.T) {
	peer := PPPoEPeer{ID: "wan", Interface: "ppp0", Username: "user"}
	notReady, err := NewPPPoEStatus(peer, PPPoEConnecting, "", "", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PPPoEVPPRouteHandoff(notReady, "req"); err == nil {
		t.Fatal("PPPoEVPPRouteHandoff succeeded before route readiness")
	}
	ready, err := NewPPPoEStatus(peer, PPPoEConnected, "198.51.100.44", "", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	operations, err := PPPoEVPPRouteHandoff(ready, "req-pppoe")
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].Name != "vpp.fib.route" || operations[0].RequestID != "req-pppoe" || operations[0].Resource != "wan" {
		t.Fatalf("operations = %#v", operations)
	}
	payload, ok := operations[0].Payload.(PPPoERouteHandoff)
	if !ok || !payload.Ready || payload.Interface != "ppp0" || payload.VPPTableID != 100 || payload.Destination != "0.0.0.0/0" {
		t.Fatalf("handoff payload = %#v", operations[0].Payload)
	}
}

func TestRenderSmartDNSRejectsProxyDNSWithoutEndpoint(t *testing.T) {
	compiledDNS, err := dns.CompilePolicy(dns.NewPolicy(dns.Reject(), []dns.Rule{{ID: "proxy-media", Domains: []string{"media.example"}, Outcome: dns.Proxy("proxy-media")}}), []proxy.Egress{proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")})
	if err != nil {
		t.Fatal(err)
	}

	_, err = RenderSmartDNS(SmartDNSPlan{ID: "default", Render: compiledDNS.RenderSmartDNS()})
	if err == nil || !strings.Contains(err.Error(), "requires proxy DNS endpoint") {
		t.Fatalf("RenderSmartDNS error = %v, want proxy DNS endpoint failure", err)
	}
}

func compiledProxyDataplaneOperationsForTest(compiled proxy.CompiledEgress) []vpp.Operation {
	operations := make([]vpp.Operation, 0, len(compiled.VPPSteering))
	for _, steering := range compiled.VPPSteering {
		operations = append(operations, vpp.Operation{Name: steering.TargetKind, RequestID: "test", Resource: steering.EgressID, Payload: steering})
	}
	return operations
}

func TestFilesystemControllerWritesArtifactsAndRunsServiceCommand(t *testing.T) {
	runner := availableRunner(SmartDNS, map[string]string{
		"systemctl show smartdns.service --property=ActiveEnterTimestampMonotonic --value": "77123",
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}
	artifacts := []RenderedArtifact{
		NewArtifact(SmartDNS, "/etc/smartdns/ly-route-default.json", `{"ok":true}`, "reload"),
		NewArtifact(SmartDNS, "/run/ly-route/apply.sh", "#!/bin/sh\nexit 0\n", "reload"),
	}

	if err := controller.ReloadOrRestart(context.Background(), SmartDNS, artifacts); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(controller.RootDir, "etc/smartdns/ly-route-default.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != `{"ok":true}` {
		t.Fatalf("written artifact = %q", string(written))
	}
	scriptInfo, err := os.Stat(filepath.Join(controller.RootDir, "run/ly-route/apply.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if scriptInfo.Mode().Perm() != 0750 {
		t.Fatalf("script permissions = %v, want 0750", scriptInfo.Mode().Perm())
	}
	if got, want := strings.Join(runner.commands, " "), "systemctl reload-or-restart smartdns.service systemctl show smartdns.service --property=ActiveEnterTimestampMonotonic --value"; got != want {
		t.Fatalf("commands = %q, want %q", got, want)
	}
}

func TestFilesystemControllerSkipsUnchangedHealthyServiceRestart(t *testing.T) {
	runner := availableRunner(SmartDNS, map[string]string{
		"systemctl show smartdns.service --property=ActiveEnterTimestampMonotonic --value": "77123",
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}
	artifacts := []RenderedArtifact{
		NewArtifact(SmartDNS, "/etc/smartdns/ly-route-default.json", `{"ok":true}`, "reload"),
	}

	if err := controller.ReloadOrRestart(context.Background(), SmartDNS, artifacts); err != nil {
		t.Fatal(err)
	}
	runner.commands = nil
	if err := controller.ReloadOrRestart(context.Background(), SmartDNS, artifacts); err != nil {
		t.Fatal(err)
	}

	if got, want := strings.Join(runner.commands, " "), "systemctl show smartdns.service --property=ActiveEnterTimestampMonotonic --value"; got != want {
		t.Fatalf("unchanged service commands = %q, want %q", got, want)
	}
}

func TestFilesystemControllerKeaRestartRemovesStaleLeaseCleanupLock(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "var/lib/kea/kea-leases4.csv.pid")
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidPath, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	controller := FilesystemController{RootDir: root, Runner: runner}

	if err := controller.runApplyCommand(context.Background(), Kea, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale Kea lease cleanup lock error = %v, want not exist", err)
	}
	if got, want := strings.Join(runner.commands, ","), "systemctl stop kea-dhcp4-server.service,systemctl start kea-dhcp4-server.service"; got != want {
		t.Fatalf("Kea restart commands = %q, want %q", got, want)
	}
}

func TestFilesystemControllerProtectsPPPoESecrets(t *testing.T) {
	controller := FilesystemController{RootDir: t.TempDir()}
	artifacts := []RenderedArtifact{
		NewArtifact(PPPd, "/etc/ly-route/pppoe/ly-route-wan.json", `{"username":"user","password":"secret"}`+"\n", "restart"),
	}
	if err := controller.writeArtifacts(PPPd, artifacts); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"etc/ly-route/pppoe/ly-route-wan.json"} {
		info, err := os.Stat(filepath.Join(controller.RootDir, path))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0600); got != want {
			t.Fatalf("%s permissions = %o, want %o", path, got, want)
		}
	}
}

func TestFilesystemControllerRemovesStaleSmartDNSArtifacts(t *testing.T) {
	runner := availableRunner(SmartDNS, map[string]string{
		"systemctl show smartdns.service --property=ActiveEnterTimestampMonotonic --value": "77123",
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}
	confDir := filepath.Join(controller.RootDir, "etc/smartdns/conf.d")
	if err := os.MkdirAll(confDir, 0750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"ly-route-current.conf": "server 1.1.1.1",
		"ly-route-default.conf": "address #",
		"ly-route-active.conf":  "address /old.example/#",
		"ly-route-stale.conf":   "address /stale.example/#",
		"manual.conf":           "server 8.8.8.8",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(confDir, name), []byte(content), 0640); err != nil {
			t.Fatal(err)
		}
	}

	compiledDNS, err := dns.CompilePolicy(dns.NewPolicy(dns.Reject(), []dns.Rule{
		{ID: "current", Domains: []string{"current.example"}, Outcome: dns.FixedAnswer("203.0.113.10")},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderSmartDNS(SmartDNSPlan{ID: "current", Render: compiledDNS.RenderSmartDNS()})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ReloadOrRestart(context.Background(), SmartDNS, artifacts); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(confDir, "ly-route-stale.conf")); !os.IsNotExist(err) {
		t.Fatalf("stale managed artifact error = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(confDir, "manual.conf")); err != nil {
		t.Fatalf("manual smartdns conf removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(confDir, "ly-route-default.conf")); !os.IsNotExist(err) {
		t.Fatalf("obsolete default managed artifact error = %v, want not exist", err)
	}
	written, err := os.ReadFile(filepath.Join(confDir, "ly-route-active.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "address /-.current.example/203.0.113.10") {
		t.Fatalf("active artifact = %q", string(written))
	}
}

func TestFilesystemControllerHealthAndRollback(t *testing.T) {
	runner := &fakeRunner{health: map[ServiceName]Health{Kea: {Service: Kea, Available: false, Reason: "kea inactive"}}}
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}
	health, err := controller.Status(context.Background(), Kea)
	if err != nil {
		t.Fatal(err)
	}
	if health.Available || health.Reason != "kea inactive" {
		t.Fatalf("health = %#v", health)
	}
	if err := controller.Rollback(context.Background(), Xray, []RenderedArtifact{NewArtifact(Xray, "/etc/xray/config.json", `{"rollback":true}`, "restart")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(controller.RootDir, "etc/xray/config.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRenderedArtifactsAreValidJSONWhereExpected(t *testing.T) {
	artifacts, err := RenderKeaDHCP4(KeaDHCP4Plan{ID: "lan", InterfaceID: "eth1", Subnet: "192.168.88.0/24", Pools: []string{"192.168.88.100-192.168.88.199"}, Routers: []string{"192.168.88.1"}, NameServers: []string{"192.168.88.1"}})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(artifacts[0].Content), &payload); err != nil {
		t.Fatalf("kea content is not json: %v", err)
	}
	if strings.Contains(artifacts[0].Content, "interface_id") || strings.Contains(artifacts[0].Content, "name_servers") || strings.Contains(artifacts[0].Content, "\"routers\":") {
		t.Fatalf("kea content leaked internal desired-state fields: %s", artifacts[0].Content)
	}
	if !strings.Contains(artifacts[0].Content, "option-data") || !strings.Contains(artifacts[0].Content, "domain-name-servers") {
		t.Fatalf("kea content missing native DHCP options: %s", artifacts[0].Content)
	}
}

type fakeController struct {
	applied      []string
	rolledBack   []string
	applyErr     error
	applyErrs    map[ServiceName]error
	rollbackErrs map[ServiceName]error
	health       map[ServiceName]Health
}

type fakeRunner struct {
	commands     []string
	health       map[ServiceName]Health
	outputs      map[string]string
	readErrs     map[ServiceName]error
	runErrs      map[string]error
	runErrCounts map[string]int
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	command := strings.TrimSpace(name + " " + strings.Join(args, " "))
	runner.commands = append(runner.commands, command)
	if err := runner.runErrs[command]; err != nil {
		if runner.runErrCounts != nil {
			if remaining := runner.runErrCounts[command]; remaining > 0 {
				runner.runErrCounts[command] = remaining - 1
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

func (runner *fakeRunner) Status(_ context.Context, service ServiceName) (Health, error) {
	if runner.health != nil {
		if health, ok := runner.health[service]; ok {
			return health, nil
		}
	}
	return Health{Service: service, Available: false, Reason: "service inactive"}, nil
}

func (runner *fakeRunner) Readback(_ context.Context, service ServiceName, _ []RenderedArtifact) error {
	return runner.readErrs[service]
}

func (controller *fakeController) ReloadOrRestart(_ context.Context, service ServiceName, _ []RenderedArtifact) error {
	controller.applied = append(controller.applied, string(service))
	if err := controller.applyErrs[service]; err != nil {
		return err
	}
	return controller.applyErr
}

func (controller *fakeController) Status(_ context.Context, service ServiceName) (Health, error) {
	if controller.health != nil {
		if health, ok := controller.health[service]; ok {
			return health, nil
		}
	}
	return Health{Service: service, Available: false, Reason: "not available"}, nil
}

func (controller *fakeController) Rollback(_ context.Context, service ServiceName, _ []RenderedArtifact) error {
	controller.rolledBack = append(controller.rolledBack, string(service))
	if err := controller.rollbackErrs[service]; err != nil {
		return err
	}
	return nil
}
