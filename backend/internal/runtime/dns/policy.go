package dns

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"ly-route/backend/internal/runtime/proxy"
)

type OutcomeKind string

const (
	OutcomeDirect OutcomeKind = "direct"
	OutcomeProxy  OutcomeKind = "proxy"
	OutcomeReject OutcomeKind = "reject"
	OutcomeFixed  OutcomeKind = "fixed_answer"
)

type ResolverOutcomeKind string

const (
	ResolverOutcomeDirect          ResolverOutcomeKind = "direct_resolution"
	ResolverOutcomeProxyResolution ResolverOutcomeKind = "proxy_resolution"
	ResolverOutcomeReject          ResolverOutcomeKind = "reject"
	ResolverOutcomeFixedAnswer     ResolverOutcomeKind = "fixed_answer"
)

const DNSPolicyPrecedence = "dns_policy"

var ErrInvalidPolicy = errors.New("invalid dns policy")

type Outcome struct {
	Kind          OutcomeKind `json:"kind"`
	ProxyEgressID string      `json:"proxy_egress_id,omitempty"`
	UpstreamID    string      `json:"upstream_id,omitempty"`
	WANEgressID   string      `json:"wan_egress_id,omitempty"`
	FixedAnswers  []string    `json:"fixed_answers,omitempty"`
}

func Direct() Outcome {
	return Outcome{Kind: OutcomeDirect, UpstreamID: "dns-direct-default"}
}

func Proxy(proxyEgressID string) Outcome {
	return Outcome{Kind: OutcomeProxy, ProxyEgressID: proxyEgressID}
}

func Reject() Outcome {
	return Outcome{Kind: OutcomeReject}
}

func FixedAnswer(answers ...string) Outcome {
	return Outcome{Kind: OutcomeFixed, FixedAnswers: append([]string(nil), answers...)}
}

type Rule struct {
	ID             string   `json:"id"`
	SourcePrefixes []string `json:"source_prefixes,omitempty"`
	Domains        []string `json:"domains"`
	DomainSuffixes []string `json:"domain_suffixes,omitempty"`
	DomainSetIDs   []string `json:"domain_set_ids,omitempty"`
	Outcome        Outcome  `json:"outcome"`
}

type Policy struct {
	Engine string  `json:"engine"`
	Miss   Outcome `json:"miss"`
	Rules  []Rule  `json:"rules"`
}

type CompiledPolicy struct {
	Engine                 string            `json:"engine"`
	DecisionPrecedence     string            `json:"decision_precedence"`
	Miss                   CompiledOutcome   `json:"miss"`
	Rules                  []CompiledRule    `json:"rules"`
	ProxyDNSBindings       []ProxyDNSBinding `json:"proxy_dns_bindings"`
	ReferencedEgressIDs    []string          `json:"referenced_proxy_egress_ids"`
	ReferencedDomainSetIDs []string          `json:"referenced_domain_set_ids,omitempty"`
	ReferencedWANEgressIDs []string          `json:"referenced_wan_egress_ids,omitempty"`
	ReferencedUpstreamIDs  []string          `json:"referenced_upstream_ids,omitempty"`
}

type CompiledRule struct {
	ID             string          `json:"id"`
	SourcePrefixes []string        `json:"source_prefixes,omitempty"`
	Domains        []string        `json:"domains"`
	DomainSuffixes []string        `json:"domain_suffixes,omitempty"`
	DomainSetIDs   []string        `json:"domain_set_ids,omitempty"`
	Outcome        CompiledOutcome `json:"outcome"`
}

type CompiledOutcome struct {
	Kind             ResolverOutcomeKind `json:"kind"`
	PolicyPrecedence string              `json:"policy_precedence,omitempty"`
	ResolverPath     string              `json:"resolver_path"`
	ProxyEgressID    string              `json:"proxy_egress_id,omitempty"`
	UpstreamID       string              `json:"upstream_id,omitempty"`
	WANEgressID      string              `json:"wan_egress_id,omitempty"`
	DNSRequestPath   string              `json:"dns_request_path,omitempty"`
	FixedAnswers     []string            `json:"fixed_answers,omitempty"`
}

type ProxyDNSBinding struct {
	RuleID           string              `json:"rule_id"`
	ProxyEgressID    string              `json:"proxy_egress_id"`
	OutcomeKind      ResolverOutcomeKind `json:"outcome_kind"`
	PolicyPrecedence string              `json:"policy_precedence"`
	ResolverPath     string              `json:"resolver_path"`
	DNSRequestPath   string              `json:"dns_request_path"`
}

type Decision struct {
	Domain        string          `json:"domain"`
	SourceIP      string          `json:"source_ip,omitempty"`
	Matched       bool            `json:"matched"`
	RuleID        string          `json:"rule_id,omitempty"`
	Outcome       CompiledOutcome `json:"outcome"`
	RCode         string          `json:"rcode"`
	Answer        string          `json:"answer"`
	Answers       []string        `json:"answers,omitempty"`
	ContinueRules bool            `json:"continue_rules"`
	Reason        string          `json:"reason,omitempty"`
}

type SmartDNSRender struct {
	Engine             string         `json:"engine"`
	DecisionPrecedence string         `json:"decision_precedence"`
	Rules              []SmartDNSRule `json:"rules"`
	Miss               SmartDNSRule   `json:"miss"`
}

type SmartDNSRule struct {
	Order            int                 `json:"order"`
	Target           string              `json:"target"`
	RuleID           string              `json:"rule_id"`
	SourcePrefixes   []string            `json:"source_prefixes,omitempty"`
	Domains          []string            `json:"domains,omitempty"`
	DomainSuffixes   []string            `json:"domain_suffixes,omitempty"`
	DomainSetIDs     []string            `json:"domain_set_ids,omitempty"`
	Action           string              `json:"action"`
	OutcomeKind      ResolverOutcomeKind `json:"outcome_kind"`
	PolicyPrecedence string              `json:"policy_precedence"`
	ResolverPath     string              `json:"resolver_path"`
	ProxyEgressID    string              `json:"proxy_egress_id,omitempty"`
	UpstreamID       string              `json:"upstream_id,omitempty"`
	WANEgressID      string              `json:"wan_egress_id,omitempty"`
	DNSRequestPath   string              `json:"dns_request_path,omitempty"`
	FixedAnswers     []string            `json:"fixed_answers,omitempty"`
	IPSetName        string              `json:"ipset_name,omitempty"`
}

func NewPolicy(miss Outcome, rules []Rule) Policy {
	miss = defaultMissOutcome(miss)
	return Policy{Engine: "smartdns", Miss: miss, Rules: append([]Rule(nil), rules...)}
}

func CompilePolicy(policy Policy, proxyEgresses []proxy.Egress) (CompiledPolicy, error) {
	proxyIDs, err := validatedProxyEgressIDs(proxyEgresses)
	if err != nil {
		return CompiledPolicy{}, err
	}

	if strings.TrimSpace(policy.Engine) == "" {
		policy.Engine = "smartdns"
	}
	if policy.Engine != "smartdns" {
		return CompiledPolicy{}, fmt.Errorf("%w: engine must be smartdns", ErrInvalidPolicy)
	}

	miss, err := compileOutcome("miss", defaultMissOutcome(policy.Miss), proxyIDs)
	if err != nil {
		return CompiledPolicy{}, err
	}

	compiledRules := make([]CompiledRule, 0, len(policy.Rules))
	bindings := make([]ProxyDNSBinding, 0, len(policy.Rules))
	referenced := make([]string, 0, len(policy.Rules))
	seenReferenced := make(map[string]struct{})
	referencedDomainSets := make([]string, 0, len(policy.Rules))
	seenDomainSets := make(map[string]struct{})
	referencedWANs := make([]string, 0, len(policy.Rules))
	seenWANs := make(map[string]struct{})
	referencedUpstreams := make([]string, 0, len(policy.Rules))
	seenUpstreams := make(map[string]struct{})
	seenRules := make(map[string]struct{})
	for _, rule := range policy.Rules {
		ruleID := strings.TrimSpace(rule.ID)
		if ruleID == "" {
			return CompiledPolicy{}, fmt.Errorf("%w: rule id is required", ErrInvalidPolicy)
		}
		if _, exists := seenRules[ruleID]; exists {
			return CompiledPolicy{}, fmt.Errorf("%w: duplicate rule id %q", ErrInvalidPolicy, ruleID)
		}
		seenRules[ruleID] = struct{}{}
		sourcePrefixes, err := validatedSourcePrefixes("rule "+ruleID, rule.SourcePrefixes)
		if err != nil {
			return CompiledPolicy{}, err
		}
		domains, suffixes, err := validatedDomainSelectors("rule "+ruleID, rule.Domains, rule.DomainSuffixes)
		if err != nil {
			return CompiledPolicy{}, err
		}
		domainSetIDs := normalizedIDs(rule.DomainSetIDs)
		for _, id := range domainSetIDs {
			addUnique(&referencedDomainSets, seenDomainSets, id)
		}

		compiledOutcome, err := compileOutcome("rule "+ruleID, rule.Outcome, proxyIDs)
		if err != nil {
			return CompiledPolicy{}, err
		}
		compiledRules = append(compiledRules, CompiledRule{
			ID:             ruleID,
			SourcePrefixes: sourcePrefixes,
			Domains:        domains,
			DomainSuffixes: suffixes,
			DomainSetIDs:   domainSetIDs,
			Outcome:        compiledOutcome,
		})
		if compiledOutcome.Kind == ResolverOutcomeProxyResolution {
			bindings = append(bindings, ProxyDNSBinding{
				RuleID:           ruleID,
				ProxyEgressID:    compiledOutcome.ProxyEgressID,
				OutcomeKind:      ResolverOutcomeProxyResolution,
				PolicyPrecedence: compiledOutcome.PolicyPrecedence,
				ResolverPath:     compiledOutcome.ResolverPath,
				DNSRequestPath:   compiledOutcome.DNSRequestPath,
			})
			addUnique(&referenced, seenReferenced, compiledOutcome.ProxyEgressID)
		}
		if compiledOutcome.WANEgressID != "" {
			addUnique(&referencedWANs, seenWANs, compiledOutcome.WANEgressID)
		}
		if compiledOutcome.UpstreamID != "" {
			addUnique(&referencedUpstreams, seenUpstreams, compiledOutcome.UpstreamID)
		}
	}

	return CompiledPolicy{
		Engine:                 policy.Engine,
		DecisionPrecedence:     DNSPolicyPrecedence,
		Miss:                   miss,
		Rules:                  compiledRules,
		ProxyDNSBindings:       bindings,
		ReferencedEgressIDs:    referenced,
		ReferencedDomainSetIDs: referencedDomainSets,
		ReferencedWANEgressIDs: referencedWANs,
		ReferencedUpstreamIDs:  referencedUpstreams,
	}, nil
}

func Decide(compiled CompiledPolicy, domain string, unavailableResolverPaths map[string]bool) Decision {
	return DecideForSource(compiled, domain, "", unavailableResolverPaths)
}

func DecideForSource(compiled CompiledPolicy, domain, sourceIP string, unavailableResolverPaths map[string]bool) Decision {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	sourceIP = strings.TrimSpace(sourceIP)
	for _, rule := range compiled.Rules {
		if !ruleMatchesSource(rule.SourcePrefixes, sourceIP) || !ruleMatchesDomain(rule.Domains, rule.DomainSuffixes, domain) {
			continue
		}
		return decisionForOutcome(domain, sourceIP, true, rule.ID, rule.Outcome, unavailableResolverPaths)
	}
	return decisionForOutcome(domain, sourceIP, false, "", compiled.Miss, unavailableResolverPaths)
}

func decisionForOutcome(domain, sourceIP string, matched bool, ruleID string, outcome CompiledOutcome, unavailableResolverPaths map[string]bool) Decision {
	decision := Decision{Domain: domain, SourceIP: sourceIP, Matched: matched, RuleID: ruleID, Outcome: outcome, RCode: "NOERROR", Answer: "NODATA", ContinueRules: false}
	if unavailableResolverPaths != nil && unavailableResolverPaths[outcome.ResolverPath] {
		decision.Reason = "selected resolver is unavailable"
		return decision
	}
	switch outcome.Kind {
	case ResolverOutcomeDirect, ResolverOutcomeProxyResolution:
		decision.Answer = "RESOLVE"
	case ResolverOutcomeFixedAnswer:
		decision.Answer = "FIXED"
		decision.Answers = append([]string(nil), outcome.FixedAnswers...)
	default:
		decision.Answer = "NODATA"
	}
	return decision
}

func ruleMatchesSource(prefixes []string, sourceIP string) bool {
	if len(prefixes) == 0 {
		return true
	}
	addr, err := netip.ParseAddr(sourceIP)
	if err != nil {
		return false
	}
	for _, value := range prefixes {
		prefix, err := parseSourcePrefix(value)
		if err == nil && prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func ruleMatchesDomain(exactDomains, suffixes []string, domain string) bool {
	for _, pattern := range exactDomains {
		pattern = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pattern)), ".")
		pattern = strings.TrimPrefix(pattern, "*.")
		pattern = strings.TrimPrefix(pattern, ".")
		if pattern != "" && domain == pattern {
			return true
		}
	}
	for _, pattern := range suffixes {
		pattern = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pattern)), ".")
		pattern = strings.TrimPrefix(pattern, "*.")
		pattern = strings.TrimPrefix(pattern, ".")
		if pattern != "" && (domain == pattern || strings.HasSuffix(domain, "."+pattern)) {
			return true
		}
	}
	return false
}

func (policy CompiledPolicy) RenderSmartDNS() SmartDNSRender {
	rules := make([]SmartDNSRule, 0, len(policy.Rules))
	for index, rule := range policy.Rules {
		rules = append(rules, renderSmartDNSRule(index+1, rule.ID, rule.SourcePrefixes, rule.Domains, rule.DomainSuffixes, rule.DomainSetIDs, rule.Outcome))
	}

	return SmartDNSRender{
		Engine:             policy.Engine,
		DecisionPrecedence: DNSPolicyPrecedence,
		Rules:              rules,
		Miss:               renderSmartDNSRule(0, "miss", nil, nil, nil, nil, policy.Miss),
	}
}

func defaultMissOutcome(outcome Outcome) Outcome {
	if outcome.Kind == "" {
		outcome.Kind = OutcomeReject
	}
	return outcome
}

func renderSmartDNSRule(order int, ruleID string, sourcePrefixes, domains, suffixes, domainSetIDs []string, outcome CompiledOutcome) SmartDNSRule {
	rule := SmartDNSRule{
		Order:            order,
		RuleID:           ruleID,
		SourcePrefixes:   append([]string(nil), sourcePrefixes...),
		Domains:          append([]string(nil), domains...),
		DomainSuffixes:   append([]string(nil), suffixes...),
		DomainSetIDs:     append([]string(nil), domainSetIDs...),
		OutcomeKind:      outcome.Kind,
		PolicyPrecedence: outcome.PolicyPrecedence,
		ResolverPath:     outcome.ResolverPath,
		UpstreamID:       outcome.UpstreamID,
		WANEgressID:      outcome.WANEgressID,
		FixedAnswers:     append([]string(nil), outcome.FixedAnswers...),
	}

	switch outcome.Kind {
	case ResolverOutcomeDirect:
		rule.Target = "smartdns.nameserver-policy"
		rule.Action = "direct_resolution"
		if outcome.WANEgressID != "" {
			rule.IPSetName = SmartDNSIPSetName(ruleID)
		}
	case ResolverOutcomeProxyResolution:
		rule.Target = "smartdns.nameserver-policy"
		rule.Action = "proxy_dns_request"
		rule.ProxyEgressID = outcome.ProxyEgressID
		rule.DNSRequestPath = outcome.DNSRequestPath
	case ResolverOutcomeReject:
		rule.Target = "smartdns.address-rule"
		rule.Action = "empty_answer"
	case ResolverOutcomeFixedAnswer:
		rule.Target = "smartdns.address-rule"
		rule.Action = "fixed_answer"
	}

	return rule
}

// SmartDNSIPSetName returns the kernel set name used as the DNS-to-dataplane
// handoff for a fixed-WAN DNS rule. Rule IDs are already validated as tokens.
func SmartDNSIPSetName(ruleID string) string {
	return "lyroute_dns_" + strings.ReplaceAll(strings.TrimSpace(ruleID), ".", "_")
}

func validatedProxyEgressIDs(proxyEgresses []proxy.Egress) (map[string]struct{}, error) {
	ids := make(map[string]struct{}, len(proxyEgresses))
	for _, egress := range proxyEgresses {
		if err := proxy.ValidateEgress(egress); err != nil {
			return nil, fmt.Errorf("%w: proxy egress %q failed validation: %v", ErrInvalidPolicy, egress.ID, err)
		}
		if _, exists := ids[egress.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate proxy egress id %q", ErrInvalidPolicy, egress.ID)
		}
		ids[egress.ID] = struct{}{}
	}
	return ids, nil
}

func validatedSourcePrefixes(label string, values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		// The UI uses an explicit Any selector for readability. In the
		// canonical policy, an empty source list means any source; accepting the
		// alias here keeps persisted/UI-authored policies valid while retaining
		// one normalized representation for SmartDNS and decision evaluation.
		if strings.EqualFold(value, "any") || value == "*" {
			continue
		}
		prefix, err := parseSourcePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("%w: %s source selector %q is not a valid IP or prefix", ErrInvalidPolicy, label, value)
		}
		text := prefix.String()
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
	}
	return result, nil
}

func parseSourcePrefix(value string) (netip.Prefix, error) {
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return netip.Prefix{}, err
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func validatedDomainSelectors(label string, domains, suffixes []string) ([]string, []string, error) {
	exact := make([]string, 0, len(domains))
	seenExact := map[string]struct{}{}
	for _, domain := range domains {
		domain, err := normalizedDomainSelector(label, domain)
		if err != nil {
			return nil, nil, err
		}
		addUnique(&exact, seenExact, domain)
	}
	normalizedSuffixes := make([]string, 0, len(suffixes))
	seenSuffixes := map[string]struct{}{}
	for _, suffix := range suffixes {
		suffix, err := normalizedDomainSelector(label, suffix)
		if err != nil {
			return nil, nil, err
		}
		addUnique(&normalizedSuffixes, seenSuffixes, suffix)
	}
	return exact, normalizedSuffixes, nil
}

func normalizedDomainSelector(label, value string) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	value = strings.TrimPrefix(value, "*.")
	value = strings.TrimPrefix(value, ".")
	if value == "" {
		return "", fmt.Errorf("%w: %s domain selector is empty", ErrInvalidPolicy, label)
	}
	if value == "*" || value == "any" {
		return "", fmt.Errorf("%w: %s must not use a default any DNS rule", ErrInvalidPolicy, label)
	}
	if _, err := netip.ParseAddr(value); err == nil || strings.Contains(value, "/") {
		return "", fmt.Errorf("%w: %s domain selector %q must not be an IP rule", ErrInvalidPolicy, label, value)
	}
	return value, nil
}

func normalizedIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		addUnique(&result, seen, strings.TrimSpace(value))
	}
	return result
}

func addUnique(values *[]string, seen map[string]struct{}, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if _, exists := seen[value]; exists {
		return
	}
	seen[value] = struct{}{}
	*values = append(*values, value)
}

func compileOutcome(label string, outcome Outcome, proxyIDs map[string]struct{}) (CompiledOutcome, error) {
	switch outcome.Kind {
	case OutcomeDirect:
		if strings.TrimSpace(outcome.ProxyEgressID) != "" {
			return CompiledOutcome{}, fmt.Errorf("%w: %s direct outcome must not reference proxy egress", ErrInvalidPolicy, label)
		}
		if len(outcome.FixedAnswers) != 0 {
			return CompiledOutcome{}, fmt.Errorf("%w: %s direct outcome must not include fixed answers", ErrInvalidPolicy, label)
		}
		upstreamID := strings.TrimSpace(outcome.UpstreamID)
		wanEgressID := strings.TrimSpace(outcome.WANEgressID)
		if upstreamID == "" && wanEgressID == "" {
			return CompiledOutcome{}, fmt.Errorf("%w: %s direct outcome must select an upstream or WAN egress", ErrInvalidPolicy, label)
		}
		resolverPath := ""
		if upstreamID != "" {
			resolverPath = "upstream:" + upstreamID
		} else if wanEgressID != "" {
			resolverPath = "wan:" + wanEgressID
		}
		return CompiledOutcome{Kind: ResolverOutcomeDirect, PolicyPrecedence: DNSPolicyPrecedence, ResolverPath: resolverPath, UpstreamID: upstreamID, WANEgressID: wanEgressID}, nil
	case OutcomeProxy:
		proxyEgressID := strings.TrimSpace(outcome.ProxyEgressID)
		if proxyEgressID == "" {
			return CompiledOutcome{}, fmt.Errorf("%w: %s proxy outcome requires proxy egress id", ErrInvalidPolicy, label)
		}
		if strings.TrimSpace(outcome.UpstreamID) != "" || strings.TrimSpace(outcome.WANEgressID) != "" || len(outcome.FixedAnswers) != 0 {
			return CompiledOutcome{}, fmt.Errorf("%w: %s proxy outcome must not include upstream, WAN, or fixed-answer fields", ErrInvalidPolicy, label)
		}
		if _, exists := proxyIDs[proxyEgressID]; !exists {
			return CompiledOutcome{}, fmt.Errorf("%w: %s references missing proxy egress %q", ErrInvalidPolicy, label, proxyEgressID)
		}
		return CompiledOutcome{Kind: ResolverOutcomeProxyResolution, PolicyPrecedence: DNSPolicyPrecedence, ResolverPath: "proxy_egress_resolver", ProxyEgressID: proxyEgressID, DNSRequestPath: "proxy_egress_dns"}, nil
	case OutcomeReject:
		if strings.TrimSpace(outcome.ProxyEgressID) != "" || strings.TrimSpace(outcome.UpstreamID) != "" || strings.TrimSpace(outcome.WANEgressID) != "" || len(outcome.FixedAnswers) != 0 {
			return CompiledOutcome{}, fmt.Errorf("%w: %s reject outcome must not include egress or fixed-answer fields", ErrInvalidPolicy, label)
		}
		return CompiledOutcome{Kind: ResolverOutcomeReject, PolicyPrecedence: DNSPolicyPrecedence, ResolverPath: "reject_response"}, nil
	case OutcomeFixed:
		if strings.TrimSpace(outcome.ProxyEgressID) != "" || strings.TrimSpace(outcome.UpstreamID) != "" || strings.TrimSpace(outcome.WANEgressID) != "" {
			return CompiledOutcome{}, fmt.Errorf("%w: %s fixed-answer outcome must not reference egress fields", ErrInvalidPolicy, label)
		}
		answers := normalizedIDs(outcome.FixedAnswers)
		if len(answers) == 0 {
			return CompiledOutcome{}, fmt.Errorf("%w: %s fixed-answer outcome requires at least one answer", ErrInvalidPolicy, label)
		}
		return CompiledOutcome{Kind: ResolverOutcomeFixedAnswer, PolicyPrecedence: DNSPolicyPrecedence, ResolverPath: "fixed_answer", FixedAnswers: answers}, nil
	default:
		return CompiledOutcome{}, fmt.Errorf("%w: %s outcome kind %q is unsupported", ErrInvalidPolicy, label, outcome.Kind)
	}
}
