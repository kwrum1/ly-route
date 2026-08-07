package dns

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/proxy"
)

func TestPolicyRepresentsDirectProxyAndRejectOutcomes(t *testing.T) {
	policy := NewPolicy(Reject(), []Rule{
		{ID: "direct-local", Domains: []string{"lan.example"}, Outcome: Direct()},
		{ID: "proxy-media", Domains: []string{"media.example"}, Outcome: Proxy("proxy-media")},
		{ID: "reject-ads", Domains: []string{"ads.example"}, Outcome: Reject()},
	})

	if policy.Engine != "smartdns" {
		t.Fatalf("engine = %q, want smartdns", policy.Engine)
	}
	if policy.Miss.Kind != OutcomeReject {
		t.Fatalf("miss outcome = %q, want %q", policy.Miss.Kind, OutcomeReject)
	}
	if policy.Rules[0].Outcome.Kind != OutcomeDirect || policy.Rules[0].Outcome.UpstreamID != "dns-direct-default" || policy.Rules[1].Outcome.Kind != OutcomeProxy || policy.Rules[2].Outcome.Kind != OutcomeReject {
		t.Fatalf("policy outcomes = %q/%q/%q, want direct/proxy/reject", policy.Rules[0].Outcome.Kind, policy.Rules[1].Outcome.Kind, policy.Rules[2].Outcome.Kind)
	}
	if policy.Rules[1].Outcome.ProxyEgressID != "proxy-media" {
		t.Fatalf("proxy egress id = %q, want proxy-media", policy.Rules[1].Outcome.ProxyEgressID)
	}
}

func TestNewPolicyDefaultsUnspecifiedMissToReject(t *testing.T) {
	policy := NewPolicy(Outcome{}, []Rule{
		{ID: "direct-local", Domains: []string{"lan.example"}, Outcome: Direct()},
	})

	if policy.Miss.Kind != OutcomeReject {
		t.Fatalf("miss outcome = %q, want %q", policy.Miss.Kind, OutcomeReject)
	}
	if policy.Rules[0].Outcome.Kind != OutcomeDirect {
		t.Fatalf("rule outcome = %q, want %q", policy.Rules[0].Outcome.Kind, OutcomeDirect)
	}
}

func TestDecideUsesManualOrderSuffixMatchAndDefaultDeny(t *testing.T) {
	compiled, err := CompilePolicy(NewPolicy(Reject(), []Rule{
		{ID: "reject-video", DomainSuffixes: []string{"video.example"}, Outcome: Reject()},
		{ID: "direct-example", Domains: []string{"example"}, Outcome: Direct()},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	matched := Decide(compiled, "cdn.video.example.", nil)
	if !matched.Matched || matched.RuleID != "reject-video" || matched.Answer != "NODATA" || matched.RCode != "NOERROR" || matched.ContinueRules {
		t.Fatalf("matched decision = %#v, want first reject rule NODATA/NOERROR without fallthrough", matched)
	}
	unmatched := Decide(compiled, "unknown.test", nil)
	if unmatched.Matched || unmatched.Answer != "NODATA" || unmatched.RCode != "NOERROR" || unmatched.Outcome.Kind != ResolverOutcomeReject || unmatched.ContinueRules {
		t.Fatalf("unmatched decision = %#v, want default-deny NODATA/NOERROR", unmatched)
	}
}

func TestDecideUsesSourceSelectorAndFixedAnswer(t *testing.T) {
	compiled, err := CompilePolicy(NewPolicy(Reject(), []Rule{
		{ID: "fixed-camera", SourcePrefixes: []string{"192.168.88.0/24"}, Domains: []string{"camera.lan"}, Outcome: FixedAnswer("192.168.88.20")},
		{ID: "direct-camera", Domains: []string{"camera.lan"}, Outcome: Direct()},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	matched := DecideForSource(compiled, "camera.lan", "192.168.88.12", nil)
	if !matched.Matched || matched.RuleID != "fixed-camera" || matched.Answer != "FIXED" || len(matched.Answers) != 1 || matched.Answers[0] != "192.168.88.20" {
		t.Fatalf("source fixed decision = %#v, want fixed answer for LAN source", matched)
	}
	fallback := DecideForSource(compiled, "camera.lan", "10.0.0.8", nil)
	if !fallback.Matched || fallback.RuleID != "direct-camera" || fallback.Answer != "RESOLVE" {
		t.Fatalf("source fallback decision = %#v, want lower direct rule for different source", fallback)
	}
}

func TestCompilePolicyReferencesDomainSetsUpstreamsAndWANEgress(t *testing.T) {
	compiled, err := CompilePolicy(NewPolicy(Reject(), []Rule{
		{ID: "wan-media", SourcePrefixes: []string{"192.168.88.10"}, Domains: []string{"media.example"}, DomainSuffixes: []string{"video.example"}, DomainSetIDs: []string{"domain-set-media"}, Outcome: Outcome{Kind: OutcomeDirect, WANEgressID: "wan-primary"}},
		{ID: "upstream-lan", Domains: []string{"lan.example"}, Outcome: Outcome{Kind: OutcomeDirect, UpstreamID: "dns-lan"}},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.ReferencedDomainSetIDs) != 1 || compiled.ReferencedDomainSetIDs[0] != "domain-set-media" {
		t.Fatalf("referenced domain sets = %#v", compiled.ReferencedDomainSetIDs)
	}
	if len(compiled.ReferencedWANEgressIDs) != 1 || compiled.ReferencedWANEgressIDs[0] != "wan-primary" {
		t.Fatalf("referenced WAN egresses = %#v", compiled.ReferencedWANEgressIDs)
	}
	if len(compiled.ReferencedUpstreamIDs) != 1 || compiled.ReferencedUpstreamIDs[0] != "dns-lan" {
		t.Fatalf("referenced upstreams = %#v", compiled.ReferencedUpstreamIDs)
	}
	rendered := compiled.RenderSmartDNS()
	if rendered.Rules[0].Target != "smartdns.nameserver-policy" || rendered.Rules[0].WANEgressID != "wan-primary" || rendered.Rules[0].SourcePrefixes[0] != "192.168.88.10/32" || rendered.Rules[0].DomainSetIDs[0] != "domain-set-media" {
		t.Fatalf("rendered WAN/domain-set rule = %#v", rendered.Rules[0])
	}
	if rendered.Rules[1].UpstreamID != "dns-lan" || rendered.Rules[1].ResolverPath != "upstream:dns-lan" {
		t.Fatalf("rendered upstream rule = %#v", rendered.Rules[1])
	}
}

func TestCompilePolicyRejectsDefaultAnyAndIPDomainRules(t *testing.T) {
	for _, rule := range []Rule{
		{ID: "any", Domains: []string{"any"}, Outcome: Direct()},
		{ID: "ip", Domains: []string{"192.168.88.10"}, Outcome: Direct()},
		{ID: "cidr", DomainSuffixes: []string{"192.168.88.0/24"}, Outcome: Direct()},
		{ID: "bad-source", SourcePrefixes: []string{"not-ip"}, Domains: []string{"example.test"}, Outcome: Direct()},
	} {
		if _, err := CompilePolicy(NewPolicy(Reject(), []Rule{rule}), nil); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("CompilePolicy(%s) error = %v, want ErrInvalidPolicy", rule.ID, err)
		}
	}
}

func TestCompilePolicyRejectsImplicitOrAmbiguousDirectResolver(t *testing.T) {
	_, err := CompilePolicy(NewPolicy(Reject(), []Rule{{ID: "invalid-direct", Domains: []string{"example.test"}, Outcome: Outcome{Kind: OutcomeDirect}}}), nil)
	if !errors.Is(err, ErrInvalidPolicy) || !strings.Contains(err.Error(), "select an upstream or WAN egress") {
		t.Fatalf("implicit direct outcome error = %v, want resolver selection rejection", err)
	}
	compiled, err := CompilePolicy(NewPolicy(Reject(), []Rule{{ID: "fixed-wan", Domains: []string{"example.test"}, Outcome: Outcome{Kind: OutcomeDirect, UpstreamID: "dns-primary", WANEgressID: "wan-primary"}}}), nil)
	if err != nil {
		t.Fatalf("upstream plus WAN binding should be accepted: %v", err)
	}
	if compiled.Rules[0].Outcome.UpstreamID != "dns-primary" || compiled.Rules[0].Outcome.WANEgressID != "wan-primary" {
		t.Fatalf("combined direct binding was not retained: %#v", compiled.Rules[0].Outcome)
	}
}

func TestDecideUnavailableSelectedResolverStopsWithoutLowerRuleFallback(t *testing.T) {
	compiled, err := CompilePolicy(NewPolicy(Reject(), []Rule{
		{ID: "direct-video", Domains: []string{"video.example"}, Outcome: Direct()},
		{ID: "reject-example", Domains: []string{"example"}, Outcome: Reject()},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	decision := Decide(compiled, "video.example", map[string]bool{"upstream:dns-direct-default": true})
	if !decision.Matched || decision.RuleID != "direct-video" || decision.Answer != "NODATA" || decision.Reason != "selected resolver is unavailable" || decision.ContinueRules {
		t.Fatalf("unavailable resolver decision = %#v, want NODATA and no lower-rule fallback", decision)
	}
}

func TestCompilePolicyBindsProxyResolutionSeparatelyFromTrafficRouting(t *testing.T) {
	policy := NewPolicy(Reject(), []Rule{
		{ID: "direct-local", Domains: []string{"lan.example"}, Outcome: Direct()},
		{ID: "proxy-media", Domains: []string{"media.example"}, Outcome: Proxy("proxy-media")},
		{ID: "reject-ads", Domains: []string{"ads.example"}, Outcome: Reject()},
	})

	compiled, err := CompilePolicy(policy, []proxy.Egress{proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")})
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}

	if compiled.Engine != "smartdns" || compiled.Miss.Kind != ResolverOutcomeReject {
		t.Fatalf("compiled policy engine/miss = %q/%q, want smartdns/reject", compiled.Engine, compiled.Miss.Kind)
	}
	if len(compiled.Rules) != 3 {
		t.Fatalf("compiled rules length = %d, want 3", len(compiled.Rules))
	}
	if compiled.Rules[0].Outcome.Kind != ResolverOutcomeDirect {
		t.Fatalf("direct rule outcome = %q, want %q", compiled.Rules[0].Outcome.Kind, ResolverOutcomeDirect)
	}
	if compiled.Rules[1].Outcome.Kind != ResolverOutcomeProxyResolution || compiled.Rules[1].Outcome.ProxyEgressID != "proxy-media" || compiled.Rules[1].Outcome.DNSRequestPath != "proxy_egress_dns" {
		t.Fatalf("proxy rule outcome = %#v, want proxy resolution bound to DNS request path through proxy-media", compiled.Rules[1].Outcome)
	}
	if compiled.Rules[0].Outcome.DNSRequestPath != "" {
		t.Fatalf("direct rule DNS request path = %q, want empty", compiled.Rules[0].Outcome.DNSRequestPath)
	}
	if compiled.Rules[2].Outcome.Kind != ResolverOutcomeReject {
		t.Fatalf("reject rule outcome = %q, want %q", compiled.Rules[2].Outcome.Kind, ResolverOutcomeReject)
	}
	if compiled.Rules[2].Outcome.DNSRequestPath != "" {
		t.Fatalf("reject rule DNS request path = %q, want empty", compiled.Rules[2].Outcome.DNSRequestPath)
	}

	wantBinding := ProxyDNSBinding{RuleID: "proxy-media", ProxyEgressID: "proxy-media", OutcomeKind: ResolverOutcomeProxyResolution, PolicyPrecedence: DNSPolicyPrecedence, ResolverPath: "proxy_egress_resolver", DNSRequestPath: "proxy_egress_dns"}
	if len(compiled.ProxyDNSBindings) != 1 || compiled.ProxyDNSBindings[0] != wantBinding {
		t.Fatalf("proxy DNS bindings = %#v, want %#v", compiled.ProxyDNSBindings, []ProxyDNSBinding{wantBinding})
	}
	if len(compiled.ReferencedEgressIDs) != 1 || compiled.ReferencedEgressIDs[0] != "proxy-media" {
		t.Fatalf("referenced egress ids = %#v, want [proxy-media]", compiled.ReferencedEgressIDs)
	}

	payload, err := json.Marshal(compiled)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, required := range []string{"proxy_dns_bindings", "proxy_resolution", "proxy_egress_id", "referenced_proxy_egress_ids", `"decision_precedence":"dns_policy"`, `"policy_precedence":"dns_policy"`, `"dns_request_path":"proxy_egress_dns"`} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("compiled policy payload missing %q: %s", required, encoded)
		}
	}
	for _, forbidden := range trafficRoutingFieldNamesForTest() {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("compiled DNS payload leaked traffic-routing field %q: %s", forbidden, encoded)
		}
	}
	for _, required := range []string{`"resolver_path":"upstream:dns-direct-default"`, `"upstream_id":"dns-direct-default"`, `"resolver_path":"proxy_egress_resolver"`, `"resolver_path":"reject_response"`} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("compiled DNS payload missing resolver path %q: %s", required, encoded)
		}
	}
}

func TestCompilePolicyDefaultsUnspecifiedMissToExplicitReject(t *testing.T) {
	policy := Policy{Rules: []Rule{
		{ID: "direct-local", Domains: []string{"lan.example"}, Outcome: Direct()},
		{ID: "proxy-media", Domains: []string{"media.example"}, Outcome: Proxy("proxy-media")},
		{ID: "reject-ads", Domains: []string{"ads.example"}, Outcome: Reject()},
	}}

	compiled, err := CompilePolicy(policy, []proxy.Egress{proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")})
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}

	if !reflect.DeepEqual(compiled.Miss, CompiledOutcome{Kind: ResolverOutcomeReject, PolicyPrecedence: DNSPolicyPrecedence, ResolverPath: "reject_response"}) {
		t.Fatalf("compiled miss = %#v, want explicit reject without proxy linkage", compiled.Miss)
	}
	if len(compiled.ProxyDNSBindings) != 1 || compiled.ProxyDNSBindings[0].RuleID != "proxy-media" {
		t.Fatalf("proxy DNS bindings = %#v, want only explicit proxy rule binding", compiled.ProxyDNSBindings)
	}
	if compiled.Rules[0].Outcome.Kind != ResolverOutcomeDirect || compiled.Rules[1].Outcome.Kind != ResolverOutcomeProxyResolution || compiled.Rules[2].Outcome.Kind != ResolverOutcomeReject {
		t.Fatalf("rule outcomes = %#v, want direct/proxy_resolution/reject", compiled.Rules)
	}

	payload, err := json.Marshal(compiled)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	if !strings.Contains(encoded, `"miss":{"kind":"reject","policy_precedence":"dns_policy","resolver_path":"reject_response"}`) {
		t.Fatalf("compiled policy payload does not make miss reject explicit: %s", encoded)
	}
	for _, forbidden := range trafficRoutingFieldNamesForTest() {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("compiled DNS payload leaked traffic-routing field %q: %s", forbidden, encoded)
		}
	}
}

func TestDNSPolicyPrecedenceWinsAgainstConflictingRouteAssumptions(t *testing.T) {
	policy := NewPolicy(Reject(), []Rule{
		{ID: "dns-direct-conflict", Domains: []string{"direct.example"}, Outcome: Direct()},
		{ID: "dns-proxy-conflict", Domains: []string{"proxy.example"}, Outcome: Proxy("proxy-media")},
		{ID: "dns-reject-conflict", Domains: []string{"reject.example"}, Outcome: Reject()},
	})

	compiled, err := CompilePolicy(policy, []proxy.Egress{proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")})
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}

	if compiled.DecisionPrecedence != DNSPolicyPrecedence {
		t.Fatalf("compiled decision precedence = %q, want dns_policy", compiled.DecisionPrecedence)
	}
	for _, rule := range compiled.Rules {
		if rule.Outcome.PolicyPrecedence != DNSPolicyPrecedence {
			t.Fatalf("rule %q policy precedence = %q, want dns_policy", rule.ID, rule.Outcome.PolicyPrecedence)
		}
	}
	if compiled.Rules[0].Outcome.Kind != ResolverOutcomeDirect || compiled.Rules[0].Outcome.ProxyEgressID != "" || compiled.Rules[0].Outcome.DNSRequestPath != "" {
		t.Fatalf("direct DNS policy outcome = %#v, want direct without proxy linkage", compiled.Rules[0].Outcome)
	}
	if compiled.Rules[1].Outcome.Kind != ResolverOutcomeProxyResolution || compiled.Rules[1].Outcome.ProxyEgressID != "proxy-media" || compiled.Rules[1].Outcome.DNSRequestPath != "proxy_egress_dns" {
		t.Fatalf("proxy DNS policy outcome = %#v, want DNS request proxying through proxy-media", compiled.Rules[1].Outcome)
	}
	if compiled.Rules[2].Outcome.Kind != ResolverOutcomeReject || compiled.Rules[2].Outcome.ProxyEgressID != "" || compiled.Rules[2].Outcome.DNSRequestPath != "" {
		t.Fatalf("reject DNS policy outcome = %#v, want reject without proxy linkage", compiled.Rules[2].Outcome)
	}

	rendered := compiled.RenderSmartDNS()
	if rendered.DecisionPrecedence != DNSPolicyPrecedence {
		t.Fatalf("rendered decision precedence = %q, want dns_policy", rendered.DecisionPrecedence)
	}
	for _, rule := range rendered.Rules {
		if rule.PolicyPrecedence != DNSPolicyPrecedence {
			t.Fatalf("rendered rule %q policy precedence = %q, want dns_policy", rule.RuleID, rule.PolicyPrecedence)
		}
	}

	payload, err := json.Marshal(rendered)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, required := range []string{`"decision_precedence":"dns_policy"`, `"policy_precedence":"dns_policy"`, `"action":"direct_resolution"`, `"action":"proxy_dns_request"`, `"action":"empty_answer"`, `"dns_request_path":"proxy_egress_dns"`} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("rendered DNS payload missing %q: %s", required, encoded)
		}
	}
	for _, forbidden := range append(trafficRoutingFieldNamesForTest(), "route-direct", "route-proxy", "proxy_linkage_decision", `"request_path"`) {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("rendered DNS payload leaked route/proxy conflict marker %q: %s", forbidden, encoded)
		}
	}
}

func TestRenderSmartDNSProducesDeterministicDirectProxyRejectRules(t *testing.T) {
	policy := NewPolicy(Reject(), []Rule{
		{ID: "direct-local", Domains: []string{"lan.example", "corp.example"}, Outcome: Direct()},
		{ID: "proxy-media", Domains: []string{"video.example", "cdn.example"}, Outcome: Proxy("proxy-media")},
		{ID: "reject-ads", Domains: []string{"ads.example"}, Outcome: Reject()},
	})

	compiled, err := CompilePolicy(policy, []proxy.Egress{proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")})
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}

	rendered := compiled.RenderSmartDNS()
	payload, err := json.Marshal(rendered)
	if err != nil {
		t.Fatal(err)
	}
	secondPayload, err := json.Marshal(compiled.RenderSmartDNS())
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(secondPayload) {
		t.Fatalf("render is not deterministic:\nfirst:  %s\nsecond: %s", payload, secondPayload)
	}

	want := `{"engine":"smartdns","decision_precedence":"dns_policy","rules":[{"order":1,"target":"smartdns.nameserver-policy","rule_id":"direct-local","domains":["lan.example","corp.example"],"action":"direct_resolution","outcome_kind":"direct_resolution","policy_precedence":"dns_policy","resolver_path":"upstream:dns-direct-default","upstream_id":"dns-direct-default"},{"order":2,"target":"smartdns.nameserver-policy","rule_id":"proxy-media","domains":["video.example","cdn.example"],"action":"proxy_dns_request","outcome_kind":"proxy_resolution","policy_precedence":"dns_policy","resolver_path":"proxy_egress_resolver","proxy_egress_id":"proxy-media","dns_request_path":"proxy_egress_dns"},{"order":3,"target":"smartdns.address-rule","rule_id":"reject-ads","domains":["ads.example"],"action":"empty_answer","outcome_kind":"reject","policy_precedence":"dns_policy","resolver_path":"reject_response"}],"miss":{"order":0,"target":"smartdns.address-rule","rule_id":"miss","action":"empty_answer","outcome_kind":"reject","policy_precedence":"dns_policy","resolver_path":"reject_response"}}`
	if string(payload) != want {
		t.Fatalf("rendered SmartDNS payload:\n%s\nwant:\n%s", payload, want)
	}

	var roundTripped SmartDNSRender
	if err := json.Unmarshal(payload, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if roundTripped.Rules[1].DNSRequestPath != "proxy_egress_dns" {
		t.Fatalf("round-tripped proxy DNS request path = %q, want proxy_egress_dns", roundTripped.Rules[1].DNSRequestPath)
	}
	if roundTripped.Rules[0].DNSRequestPath != "" || roundTripped.Rules[2].DNSRequestPath != "" || roundTripped.Miss.DNSRequestPath != "" {
		t.Fatalf("direct/reject DNS request paths = %q/%q/%q, want empty", roundTripped.Rules[0].DNSRequestPath, roundTripped.Rules[2].DNSRequestPath, roundTripped.Miss.DNSRequestPath)
	}

	encoded := string(payload)
	for _, required := range []string{"smartdns.nameserver-policy", "smartdns.address-rule", "direct_resolution", "empty_answer", "proxy_dns_request", "proxy_egress_dns", "proxy_egress_resolver"} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("rendered SmartDNS payload missing %q: %s", required, encoded)
		}
	}
	for _, forbidden := range append(trafficRoutingFieldNamesForTest(), `"request_path"`) {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("rendered SmartDNS payload leaked traffic-routing field %q: %s", forbidden, encoded)
		}
	}
}

func trafficRoutingFieldNamesForTest() []string {
	return []string{
		`"gateway_route"`,
		`"gateway_route_policy"`,
		`"routing"`,
		`"vpp"`,
		`"fwmark"`,
		`"traffic_steering"`,
		`"route_steering"`,
		`"post_resolution"`,
	}
}

func TestRenderSmartDNSFixedWANUsesStableIPSetName(t *testing.T) {
	policy := NewPolicy(Reject(), []Rule{{ID: "fixed.wan-1", Domains: []string{"updates.example"}, Outcome: Outcome{Kind: OutcomeDirect, WANEgressID: "wan-primary"}}})
	compiled, err := CompilePolicy(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	rendered := compiled.RenderSmartDNS()
	if rendered.Rules[0].IPSetName != "lyroute_dns_fixed_wan-1" {
		t.Fatalf("IP set name = %q", rendered.Rules[0].IPSetName)
	}
}

func TestCompilePolicyRejectsMissingProxyEgressReference(t *testing.T) {
	policy := NewPolicy(Reject(), []Rule{
		{ID: "proxy-media", Domains: []string{"media.example"}, Outcome: Proxy("proxy-media")},
	})

	_, err := CompilePolicy(policy, nil)
	if !errors.Is(err, ErrInvalidPolicy) || !strings.Contains(err.Error(), "missing proxy egress") {
		t.Fatalf("compile policy error = %v, want missing proxy egress ErrInvalidPolicy", err)
	}
}

func TestCompilePolicyRejectsInvalidProxyEgressReference(t *testing.T) {
	policy := NewPolicy(Reject(), []Rule{
		{ID: "proxy-media", Domains: []string{"media.example"}, Outcome: Proxy("proxy-media")},
	})
	invalid := proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")
	invalid.SemanticType = proxy.PhysicalWAN

	_, err := CompilePolicy(policy, []proxy.Egress{invalid})
	if !errors.Is(err, ErrInvalidPolicy) || !strings.Contains(err.Error(), "failed validation") {
		t.Fatalf("compile policy error = %v, want invalid proxy egress ErrInvalidPolicy", err)
	}
}

func TestCompilePolicyRejectsProxyOutcomeWithoutEgressID(t *testing.T) {
	policy := NewPolicy(Reject(), []Rule{
		{ID: "proxy-media", Domains: []string{"media.example"}, Outcome: Proxy(" ")},
	})

	_, err := CompilePolicy(policy, []proxy.Egress{proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")})
	if !errors.Is(err, ErrInvalidPolicy) || !strings.Contains(err.Error(), "requires proxy egress id") {
		t.Fatalf("compile policy error = %v, want missing proxy egress id ErrInvalidPolicy", err)
	}
}

func TestDNSPolicyMissRejectDoesNotLeakBridgeOrConnectionLimit(t *testing.T) {
	payload, err := json.Marshal(NewPolicy(Reject(), nil))
	if err != nil {
		t.Fatal(err)
	}

	encoded := string(payload)
	if !strings.Contains(encoded, `"miss":{"kind":"reject"}`) {
		t.Fatalf("encoded policy does not include reject miss behavior: %s", encoded)
	}
	for _, forbidden := range []string{"bridge", "bridge_mode", "connection_limit", "max_connections"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("DNS policy payload leaked forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestDNSCacheReturnsEntryBeforeTTLBoundary(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cache := NewDNSCache(func() time.Time { return now })
	outcome := CompiledOutcome{Kind: ResolverOutcomeProxyResolution, ProxyEgressID: "proxy-media"}

	cache.Store("media.example", []string{"203.0.113.10"}, outcome, 30*time.Second)
	now = now.Add(29*time.Second + time.Nanosecond)

	result, ok := cache.Lookup("media.example")
	if !ok {
		t.Fatal("lookup returned miss before TTL boundary")
	}
	if result.Expired || result.Stale {
		t.Fatalf("result = %#v, want fresh cache entry", result)
	}
	if !reflect.DeepEqual(result.Outcome, outcome) || result.ProxyEgressID != "proxy-media" {
		t.Fatalf("result outcome = %#v proxy = %q, want proxy DNS linkage preserved", result.Outcome, result.ProxyEgressID)
	}
}

func TestDNSCacheExpiresEntryAtTTLBoundary(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cache := NewDNSCache(func() time.Time { return now })

	cache.Store("media.example", []string{"203.0.113.10"}, CompiledOutcome{Kind: ResolverOutcomeDirect}, 30*time.Second)
	now = now.Add(30 * time.Second)

	result, ok := cache.Lookup("media.example")
	if ok {
		t.Fatalf("lookup result = %#v, want miss at TTL boundary", result)
	}
}

func TestDNSCacheExposesExpiredEntryOnlyAsStale(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cache := NewDNSCache(func() time.Time { return now })
	outcome := CompiledOutcome{Kind: ResolverOutcomeProxyResolution, ProxyEgressID: "proxy-media"}

	cache.Store("media.example", []string{"203.0.113.10"}, outcome, 30*time.Second)
	now = now.Add(31 * time.Second)

	if result, ok := cache.Lookup("media.example"); ok {
		t.Fatalf("lookup result = %#v, want expired entry withheld from valid cache results", result)
	}
	stale, ok := cache.Stale("media.example")
	if !ok {
		t.Fatal("stale lookup returned miss for expired entry")
	}
	if !stale.Expired || !stale.Stale {
		t.Fatalf("stale result = %#v, want explicit expired/stale flags", stale)
	}
	if !reflect.DeepEqual(stale.Outcome, outcome) || stale.ProxyEgressID != "proxy-media" {
		t.Fatalf("stale outcome = %#v proxy = %q, want proxy DNS linkage preserved", stale.Outcome, stale.ProxyEgressID)
	}
}

func TestDomainIPSetMatchesFreshSmartDNSResult(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cache := NewDNSCache(func() time.Time { return now })
	set := NewDomainIPSet(func() time.Time { return now })
	outcome := CompiledOutcome{Kind: ResolverOutcomeProxyResolution, ResolverPath: "proxy_egress_resolver", ProxyEgressID: "proxy-media"}

	result := cache.Store("media.example", []string{"203.0.113.10"}, outcome, 30*time.Second)
	set.StoreResult(result)
	now = now.Add(29 * time.Second)

	entry, ok := set.Match("203.0.113.10")
	if !ok {
		t.Fatal("IP set match returned miss before TTL boundary")
	}
	if entry.Domain != "media.example" || !reflect.DeepEqual(entry.Outcome, outcome) || entry.ProxyEgressID != "proxy-media" {
		t.Fatalf("IP set entry = %#v, want SmartDNS domain-to-IP linkage preserved", entry)
	}
	if entry.Expired || entry.Stale {
		t.Fatalf("IP set entry = %#v, want fresh match", entry)
	}
}

func TestDomainIPSetExpiresAtTTLBoundary(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	set := NewDomainIPSet(func() time.Time { return now })

	set.Store("media.example", []string{"203.0.113.10"}, CompiledOutcome{Kind: ResolverOutcomeDirect}, 30*time.Second)
	now = now.Add(30 * time.Second)

	if entry, ok := set.Match("203.0.113.10"); ok {
		t.Fatalf("IP set match = %#v, want miss at TTL boundary", entry)
	}
}

func TestDomainIPSetExposesExpiredEntryOnlyAsStale(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	set := NewDomainIPSet(func() time.Time { return now })
	outcome := CompiledOutcome{Kind: ResolverOutcomeProxyResolution, ProxyEgressID: "proxy-media"}

	set.Store("media.example", []string{"203.0.113.10"}, outcome, 30*time.Second)
	now = now.Add(31 * time.Second)

	if entry, ok := set.Match("203.0.113.10"); ok {
		t.Fatalf("IP set match = %#v, want expired entry withheld from fresh matches", entry)
	}
	stale, ok := set.Stale("203.0.113.10")
	if !ok {
		t.Fatal("stale IP set lookup returned miss for expired entry")
	}
	if !stale.Expired || !stale.Stale || !reflect.DeepEqual(stale.Outcome, outcome) || stale.ProxyEgressID != "proxy-media" {
		t.Fatalf("stale IP set entry = %#v, want expired/stale SmartDNS linkage", stale)
	}
}

func TestDomainIPSetSnapshotSerializesDeterministicallyAndRoundTrips(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	set := NewDomainIPSet(func() time.Time { return now })
	outcome := CompiledOutcome{Kind: ResolverOutcomeDirect, ResolverPath: "direct_resolver"}

	set.Store("media.example", []string{"203.0.113.20", "2001:db8::1", "203.0.113.10", "203.0.113.10"}, outcome, 30*time.Second)
	payload, err := json.Marshal(set.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"entries":[{"domain":"media.example","ip":"203.0.113.10","outcome":{"kind":"direct_resolution","resolver_path":"direct_resolver"},"cached_at":"2026-06-05T12:00:00Z","ttl":30000000000,"expires_at":"2026-06-05T12:00:30Z","expired":false,"stale":false},{"domain":"media.example","ip":"203.0.113.20","outcome":{"kind":"direct_resolution","resolver_path":"direct_resolver"},"cached_at":"2026-06-05T12:00:00Z","ttl":30000000000,"expires_at":"2026-06-05T12:00:30Z","expired":false,"stale":false},{"domain":"media.example","ip":"2001:db8::1","outcome":{"kind":"direct_resolution","resolver_path":"direct_resolver"},"cached_at":"2026-06-05T12:00:00Z","ttl":30000000000,"expires_at":"2026-06-05T12:00:30Z","expired":false,"stale":false}]}`
	if string(payload) != expected {
		t.Fatalf("snapshot payload = %s, want %s", payload, expected)
	}

	var snapshot DomainIPSetSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	rehydrated := DomainIPSetFromSnapshot(snapshot, func() time.Time { return now })
	entry, ok := rehydrated.Match("2001:db8::1")
	if !ok {
		t.Fatal("rehydrated IP set returned miss for fresh entry")
	}
	if entry.Domain != "media.example" || !reflect.DeepEqual(entry.Outcome, outcome) {
		t.Fatalf("rehydrated entry = %#v, want original domain and outcome", entry)
	}
}
