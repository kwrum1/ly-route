package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"ly-route/backend/internal/runtime/dns"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/vpp"
)

type ServiceName string

const (
	SmartDNS     ServiceName = "smartdns"
	Kea          ServiceName = "kea"
	Xray         ServiceName = "xray"
	PPPd         ServiceName = "pppd"
	VPP          ServiceName = "vpp"
	Nftables     ServiceName = "nftables"
	LinuxRouting ServiceName = "linux-routing"
	IPv6RA       ServiceName = "ipv6-ra"
)

type RenderedArtifact struct {
	Service      ServiceName `json:"service"`
	Path         string      `json:"path"`
	Content      string      `json:"content"`
	ContentHash  string      `json:"content_hash"`
	AuditSummary string      `json:"audit_summary"`
	ReloadMode   string      `json:"reload_mode"`
}

type Health struct {
	Service   ServiceName `json:"service"`
	Available bool        `json:"available"`
	Reason    string      `json:"reason,omitempty"`
}

type ProcessController interface {
	ReloadOrRestart(context.Context, ServiceName, []RenderedArtifact) error
	Status(context.Context, ServiceName) (Health, error)
	Rollback(context.Context, ServiceName, []RenderedArtifact) error
}

type StopController interface {
	Stop(context.Context, ServiceName, []RenderedArtifact) error
}

type LogController interface {
	Logs(context.Context, ServiceName, int) (string, error)
}

type CommandRunner interface {
	Run(context.Context, string, ...string) error
	Output(context.Context, string, ...string) (string, error)
	Status(context.Context, ServiceName) (Health, error)
}

type SystemctlRunner struct{}

func (SystemctlRunner) Run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		if name == "systemctl" && len(args) >= 2 {
			statusCommand := exec.CommandContext(ctx, "systemctl", "status", "--no-pager", "-l", args[len(args)-1])
			if statusOutput, statusErr := statusCommand.CombinedOutput(); statusErr == nil || len(statusOutput) > 0 {
				statusMessage := strings.TrimSpace(string(statusOutput))
				if statusMessage != "" {
					message += "\n" + statusMessage
				}
			}
		}
		return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), message)
	}
	return nil
}

func (SystemctlRunner) Status(ctx context.Context, service ServiceName) (Health, error) {
	unit := serviceUnit(service)
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run(); err != nil {
		reason := unit + " is not active"
		statusCommand := exec.CommandContext(ctx, "systemctl", "status", "--no-pager", "-l", unit)
		if output, statusErr := statusCommand.CombinedOutput(); statusErr == nil || len(output) > 0 {
			if status := strings.TrimSpace(string(output)); status != "" {
				reason += ": " + status
			}
		}
		journalCommand := exec.CommandContext(ctx, "journalctl", "-u", unit, "--no-pager", "-n", "20")
		if output, journalErr := journalCommand.CombinedOutput(); journalErr == nil || len(output) > 0 {
			if journal := strings.TrimSpace(string(output)); journal != "" {
				reason += "\n" + journal
			}
		}
		return Health{Service: service, Available: false, Reason: reason}, nil
	}
	return Health{Service: service, Available: true}, nil
}

type Runtime struct {
	Controller ProcessController
}

func NewArtifact(service ServiceName, path, content, reloadMode string) RenderedArtifact {
	digest := sha256.Sum256([]byte(content))
	return RenderedArtifact{
		Service:      service,
		Path:         strings.TrimSpace(path),
		Content:      content,
		ContentHash:  hex.EncodeToString(digest[:]),
		AuditSummary: Redact(content),
		ReloadMode:   strings.TrimSpace(reloadMode),
	}
}

type SmartDNSPlan struct {
	ID         string
	Render     dns.SmartDNSRender
	Upstreams  []SmartDNSUpstream
	Cache      SmartDNSCache
	DomainSets map[string][]string
}

// SmartDNSUpstream is a validated resolver group. Interface is the Linux WAN
// device used for egress pinning when a DNS policy selects this group.
type SmartDNSUpstream struct {
	ID          string
	Servers     []string
	Interface   string
	WANEgressID string
}

type SmartDNSCache struct {
	Size     int
	TTLMin   int
	TTLMax   int
	Prefetch bool
}

type KeaDHCP4Plan struct {
	ID           string              `json:"id"`
	InterfaceID  string              `json:"interface_id"`
	Subnet       string              `json:"subnet"`
	Pools        []string            `json:"pools"`
	Routers      []string            `json:"routers,omitempty"`
	NameServers  []string            `json:"name_servers,omitempty"`
	LeaseTime    int                 `json:"lease_time_seconds,omitempty"`
	Reservations []KeaReservation    `json:"reservations,omitempty"`
	Logging      map[string]string   `json:"logging,omitempty"`
	Options      map[string][]string `json:"options,omitempty"`
}

type KeaReservation struct {
	HWAddress string `json:"hw_address"`
	IPAddress string `json:"ip_address"`
	Hostname  string `json:"hostname,omitempty"`
}

type PPPoEPeer struct {
	ID                string   `json:"id"`
	Interface         string   `json:"interface"`
	Username          string   `json:"username"`
	Password          string   `json:"password"`
	MTU               int      `json:"mtu,omitempty"`
	MRU               int      `json:"mru,omitempty"`
	IPv6PrefixGroup   string   `json:"ipv6_prefix_group,omitempty"`
	IPv6LANInterfaces []string `json:"ipv6_lan_interfaces,omitempty"`
}

type PPPoEState string

const (
	PPPoEDisconnected PPPoEState = "disconnected"
	PPPoEConnecting   PPPoEState = "connecting"
	PPPoEConnected    PPPoEState = "connected"
	PPPoEFailed       PPPoEState = "failed"
)

type PPPoEStatus struct {
	PeerID          string     `json:"peer_id"`
	Interface       string     `json:"interface"`
	State           PPPoEState `json:"state"`
	AssignedIPv4    string     `json:"assigned_ipv4,omitempty"`
	AssignedIPv6    string     `json:"assigned_ipv6,omitempty"`
	RouteReady      bool       `json:"route_ready"`
	VPPTableID      int        `json:"vpp_table_id,omitempty"`
	VPPRouteHandoff string     `json:"vpp_route_handoff,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
}

type PPPoERouteHandoff struct {
	PeerID      string `json:"peer_id"`
	Interface   string `json:"interface"`
	VPPTableID  int    `json:"vpp_table_id"`
	Destination string `json:"destination"`
	NextHop     string `json:"next_hop,omitempty"`
	Ready       bool   `json:"ready"`
}

func RenderVPPOperations(operations []vpp.Operation) ([]RenderedArtifact, error) {
	if len(operations) == 0 {
		return nil, fmt.Errorf("vpp operation render requires at least one operation")
	}
	content, err := marshalIndented(map[string]any{"operations": operations})
	if err != nil {
		return nil, err
	}
	return []RenderedArtifact{
		NewArtifact(VPP, "/var/lib/ly-route/vpp/operations.json", content, "restart"),
	}, nil
}

func RenderSmartDNS(plan SmartDNSPlan) ([]RenderedArtifact, error) {
	return RenderSmartDNSBundle([]SmartDNSPlan{plan})
}

// RenderSmartDNSBundle writes one deterministic active configuration. SmartDNS
// evaluates rules globally, so individual policy fragments are not isolated
// safely from each other.
func RenderSmartDNSBundle(plans []SmartDNSPlan) ([]RenderedArtifact, error) {
	if len(plans) == 0 {
		return nil, fmt.Errorf("smartdns requires at least one policy plan")
	}
	content, sourceRoutes, err := renderSmartDNSBundle(plans)
	if err != nil {
		return nil, err
	}
	return []RenderedArtifact{
		NewArtifact(SmartDNS, "/etc/smartdns/conf.d/ly-route-active.conf", content, "reload"),
		NewArtifact(SmartDNS, "/etc/ly-route/dns-source-routes.conf", sourceRoutes, "reload"),
	}, nil
}

func renderSmartDNSBundle(plans []SmartDNSPlan) (string, string, error) {
	seenPlans := map[string]struct{}{}
	upstreams := make(map[string]SmartDNSUpstream)
	wanUpstreams := make(map[string]string)
	for _, plan := range plans {
		planID := strings.TrimSpace(plan.ID)
		if planID == "" {
			return "", "", fmt.Errorf("smartdns plan id is required")
		}
		if _, exists := seenPlans[planID]; exists {
			return "", "", fmt.Errorf("duplicate smartdns policy plan %q", planID)
		}
		seenPlans[planID] = struct{}{}
		for _, upstream := range plan.Upstreams {
			if err := validateSmartDNSUpstream(upstream); err != nil {
				return "", "", err
			}
			if existing, exists := upstreams[upstream.ID]; exists && !sameSmartDNSUpstream(existing, upstream) {
				return "", "", fmt.Errorf("smartdns upstream %q is defined inconsistently", upstream.ID)
			}
			upstreams[upstream.ID] = upstream
			if upstream.WANEgressID != "" {
				wanUpstreams[upstream.WANEgressID] = upstream.ID
			}
		}
	}

	var builder strings.Builder
	builder.WriteString("# Generated by Ly Route. Do not edit.\n")
	cache := plans[0].Cache
	if err := writeSmartDNSCache(&builder, cache); err != nil {
		return "", "", err
	}
	ids := make([]string, 0, len(upstreams))
	for id := range upstreams {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := writeSmartDNSUpstream(&builder, upstreams[id]); err != nil {
			return "", "", err
		}
	}
	var sourceRoutes strings.Builder
	sourceRoutes.WriteString("# source-prefix match-kind domain smartdns-port\n")
	nextSourcePort := 12000
	for _, plan := range plans {
		if err := writeSmartDNSRender(&builder, &sourceRoutes, &nextSourcePort, plan.ID, plan.Render, upstreams, wanUpstreams, plan.DomainSets); err != nil {
			return "", "", err
		}
	}
	return builder.String(), sourceRoutes.String(), nil
}

func writeSmartDNSRender(builder, sourceRoutes *strings.Builder, nextSourcePort *int, planID string, render dns.SmartDNSRender, upstreams map[string]SmartDNSUpstream, wanUpstreams map[string]string, domainSets map[string][]string) error {
	if strings.TrimSpace(render.Engine) != "smartdns" {
		return fmt.Errorf("smartdns render requires smartdns engine")
	}
	builder.WriteString("# Generated by Ly Route.\n")
	builder.WriteString("# Decision precedence: ")
	builder.WriteString(strings.TrimSpace(render.DecisionPrecedence))
	builder.WriteString("\n\n")

	for _, rule := range render.Rules {
		if err := writeSmartDNSRule(builder, sourceRoutes, nextSourcePort, planID, rule, upstreams, wanUpstreams, domainSets); err != nil {
			return err
		}
	}
	if err := writeSmartDNSMissRule(builder, render.Miss, upstreams, wanUpstreams); err != nil {
		return err
	}
	return nil
}

type smartDNSDomainSelector struct {
	matchKind string
	domain    string
}

func writeSmartDNSRule(builder, sourceRoutes *strings.Builder, nextSourcePort *int, planID string, rule dns.SmartDNSRule, upstreams map[string]SmartDNSUpstream, wanUpstreams map[string]string, domainSets map[string][]string) error {
	ruleID := strings.TrimSpace(rule.RuleID)
	if ruleID == "" {
		return fmt.Errorf("smartdns rule id is required")
	}
	selectors, err := smartDNSDomainSelectors(rule, domainSets)
	if err != nil {
		return fmt.Errorf("smartdns rule %q: %w", ruleID, err)
	}
	if len(selectors) == 0 {
		return nil
	}
	if len(rule.SourcePrefixes) != 0 {
		if nextSourcePort == nil || *nextSourcePort > 19999 {
			return fmt.Errorf("smartdns source-rule listener range is exhausted")
		}
		port := *nextSourcePort
		*nextSourcePort = port + 1
		group := smartDNSClientGroupName(strings.TrimSpace(planID) + "/" + ruleID)
		fmt.Fprintf(builder, "bind 127.0.0.1:%d -group %s\n", port, group)
		fmt.Fprintf(builder, "bind-tcp 127.0.0.1:%d -group %s\n", port, group)
		builder.WriteString("group-begin ")
		builder.WriteString(group)
		builder.WriteString(" -inherit none\n")
		for _, prefix := range rule.SourcePrefixes {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" || strings.ContainsAny(prefix, " \t\n\r") {
				return fmt.Errorf("smartdns rule %q has invalid source prefix %q", ruleID, prefix)
			}
			for _, selector := range selectors {
				fmt.Fprintf(sourceRoutes, "%s %s %s %d\n", prefix, selector.matchKind, selector.domain, port)
			}
		}
		if err := writeSmartDNSRuleContent(builder, rule, selectors, upstreams, wanUpstreams); err != nil {
			return err
		}
		builder.WriteString("group-end\n\n")
		return nil
	}
	return writeSmartDNSRuleContent(builder, rule, selectors, upstreams, wanUpstreams)
}

func smartDNSDomainSelectors(rule dns.SmartDNSRule, domainSets map[string][]string) ([]smartDNSDomainSelector, error) {
	selectors := make([]smartDNSDomainSelector, 0, len(rule.Domains)+len(rule.DomainSuffixes))
	seen := map[string]struct{}{}
	add := func(value, matchKind string) error {
		value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		value = strings.TrimPrefix(value, "*.")
		if matchKind == "suffix" {
			value = strings.TrimPrefix(value, ".")
		}
		if value == "" || strings.ContainsAny(value, "/ \t\n\r") {
			return fmt.Errorf("invalid %s domain %q", matchKind, value)
		}
		key := matchKind + "\x00" + value
		if _, exists := seen[key]; exists {
			return nil
		}
		seen[key] = struct{}{}
		selectors = append(selectors, smartDNSDomainSelector{matchKind: matchKind, domain: value})
		return nil
	}
	for _, domain := range rule.Domains {
		if err := add(domain, "exact"); err != nil {
			return nil, err
		}
	}
	for _, domain := range rule.DomainSuffixes {
		if err := add(domain, "suffix"); err != nil {
			return nil, err
		}
	}
	for _, setID := range rule.DomainSetIDs {
		setID = strings.TrimSpace(setID)
		entries, exists := domainSets[setID]
		if !exists {
			return nil, fmt.Errorf("domain set %q is unavailable", setID)
		}
		for _, entry := range entries {
			kind := "exact"
			if strings.HasPrefix(strings.TrimSpace(entry), ".") || strings.HasPrefix(strings.TrimSpace(entry), "*.") {
				kind = "suffix"
			}
			if err := add(entry, kind); err != nil {
				return nil, fmt.Errorf("domain set %q: %w", setID, err)
			}
		}
	}
	return selectors, nil
}

func smartDNSClientGroupName(ruleID string) string {
	digest := sha256.Sum256([]byte(ruleID))
	return fmt.Sprintf("lyroute-client-%x", digest[:6])
}

func writeSmartDNSRuleContent(builder *strings.Builder, rule dns.SmartDNSRule, selectors []smartDNSDomainSelector, upstreams map[string]SmartDNSUpstream, wanUpstreams map[string]string) error {
	ruleID := strings.TrimSpace(rule.RuleID)
	builder.WriteString("# rule ")
	builder.WriteString(ruleID)
	builder.WriteString("\n")
	for _, selector := range selectors {
		domain := selector.domain
		if selector.matchKind == "exact" {
			domain = "-." + domain
		}
		switch rule.OutcomeKind {
		case dns.ResolverOutcomeDirect:
			// A default `address #` miss policy otherwise also matches this
			// domain. The specific ignore rule lets the selected nameserver
			// group resolve it while unmatched names remain fail-closed.
			builder.WriteString(fmt.Sprintf("address /%s/-\n", domain))
			group, err := smartDNSGroupForRule(rule, upstreams, wanUpstreams)
			if err != nil {
				return fmt.Errorf("smartdns rule %q: %w", ruleID, err)
			}
			if group != "" {
				builder.WriteString(fmt.Sprintf("nameserver /%s/%s\n", domain, group))
			}
			if rule.IPSetName != "" {
				if !smartDNSToken(rule.IPSetName) {
					return fmt.Errorf("smartdns rule %q has invalid IP set name", ruleID)
				}
				builder.WriteString(fmt.Sprintf("ipset /%s/%s\n", domain, rule.IPSetName))
			}
		case dns.ResolverOutcomeReject:
			builder.WriteString(fmt.Sprintf("address /%s/#\n", domain))
		case dns.ResolverOutcomeFixedAnswer:
			for _, answer := range rule.FixedAnswers {
				answer = strings.TrimSpace(answer)
				if answer == "" || strings.ContainsAny(answer, "/ \t\n\r") {
					return fmt.Errorf("smartdns rule %q has invalid fixed answer %q", ruleID, answer)
				}
				builder.WriteString(fmt.Sprintf("address /%s/%s\n", domain, answer))
			}
		case dns.ResolverOutcomeProxyResolution:
			return fmt.Errorf("smartdns rule %q requires proxy DNS endpoint for egress %q", ruleID, rule.ProxyEgressID)
		default:
			return fmt.Errorf("smartdns rule %q has unsupported outcome %q", ruleID, rule.OutcomeKind)
		}
	}
	builder.WriteString("\n")
	return nil
}

func writeSmartDNSMissRule(builder *strings.Builder, rule dns.SmartDNSRule, upstreams map[string]SmartDNSUpstream, wanUpstreams map[string]string) error {
	switch rule.OutcomeKind {
	case dns.ResolverOutcomeDirect:
		if _, err := smartDNSGroupForRule(rule, upstreams, wanUpstreams); err != nil {
			return fmt.Errorf("smartdns miss policy: %w", err)
		}
		return nil
	case dns.ResolverOutcomeReject:
		builder.WriteString("# default miss policy\n")
		builder.WriteString("address #\n")
		return nil
	case dns.ResolverOutcomeProxyResolution:
		return fmt.Errorf("smartdns miss policy requires proxy DNS endpoint for egress %q", rule.ProxyEgressID)
	default:
		return fmt.Errorf("smartdns miss policy has unsupported outcome %q", rule.OutcomeKind)
	}
}

func writeSmartDNSCache(builder *strings.Builder, cache SmartDNSCache) error {
	if cache == (SmartDNSCache{}) {
		return nil
	}
	if cache.Size < 128 || cache.Size > 1048576 {
		return fmt.Errorf("smartdns cache size must be between 128 and 1048576")
	}
	if cache.TTLMin < 1 || cache.TTLMax < cache.TTLMin || cache.TTLMax > 86400 {
		return fmt.Errorf("smartdns TTL range must satisfy 1 <= min <= max <= 86400")
	}
	builder.WriteString(fmt.Sprintf("cache-size %d\nrr-ttl-min %d\nrr-ttl-max %d\n", cache.Size, cache.TTLMin, cache.TTLMax))
	if cache.Prefetch {
		builder.WriteString("prefetch-domain yes\n")
	}
	return nil
}

func validateSmartDNSUpstream(upstream SmartDNSUpstream) error {
	if !smartDNSToken(upstream.ID) {
		return fmt.Errorf("smartdns upstream id is invalid")
	}
	if len(upstream.Servers) == 0 {
		return fmt.Errorf("smartdns upstream %q requires at least one server", upstream.ID)
	}
	if upstream.Interface != "" && !smartDNSToken(upstream.Interface) {
		return fmt.Errorf("smartdns upstream %q has invalid interface", upstream.ID)
	}
	if upstream.WANEgressID != "" && !smartDNSToken(upstream.WANEgressID) {
		return fmt.Errorf("smartdns upstream %q has invalid WAN egress id", upstream.ID)
	}
	if upstream.WANEgressID != "" && upstream.Interface == "" {
		return fmt.Errorf("smartdns upstream %q pins a WAN egress but has no interface", upstream.ID)
	}
	for _, server := range upstream.Servers {
		if strings.TrimSpace(server) == "" || strings.ContainsAny(server, " \t\r\n") {
			return fmt.Errorf("smartdns upstream %q has invalid server", upstream.ID)
		}
	}
	return nil
}

func writeSmartDNSUpstream(builder *strings.Builder, upstream SmartDNSUpstream) error {
	for _, server := range upstream.Servers {
		builder.WriteString("server ")
		builder.WriteString(strings.TrimSpace(server))
		builder.WriteString(" -group ")
		builder.WriteString(upstream.ID)
		builder.WriteString(" -exclude-default-group")
		if upstream.Interface != "" {
			builder.WriteString(" -interface ")
			builder.WriteString(upstream.Interface)
		}
		builder.WriteByte('\n')
	}
	return nil
}

func smartDNSGroupForRule(rule dns.SmartDNSRule, upstreams map[string]SmartDNSUpstream, wanUpstreams map[string]string) (string, error) {
	if rule.UpstreamID != "" {
		if _, ok := upstreams[rule.UpstreamID]; !ok {
			return "", fmt.Errorf("references missing upstream %q", rule.UpstreamID)
		}
		return rule.UpstreamID, nil
	}
	if rule.WANEgressID != "" {
		group, ok := wanUpstreams[rule.WANEgressID]
		if !ok {
			return "", fmt.Errorf("references WAN %q without a pinned DNS upstream", rule.WANEgressID)
		}
		return group, nil
	}
	return "", nil
}

func smartDNSToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func sameSmartDNSUpstream(left, right SmartDNSUpstream) bool {
	return left.ID == right.ID && left.Interface == right.Interface && left.WANEgressID == right.WANEgressID && slices.Equal(left.Servers, right.Servers)
}

func RenderKeaDHCP4(plan KeaDHCP4Plan) ([]RenderedArtifact, error) {
	return RenderKeaDHCP4Config([]KeaDHCP4Plan{plan})
}

func RenderKeaDHCP4Config(plans []KeaDHCP4Plan) ([]RenderedArtifact, error) {
	if len(plans) == 0 {
		return nil, fmt.Errorf("kea dhcp4 config requires at least one plan")
	}
	interfaces := make([]string, 0, len(plans))
	seenInterfaces := map[string]bool{}
	subnets := make([]map[string]any, 0, len(plans))
	for index, plan := range plans {
		if !seenInterfaces[strings.TrimSpace(plan.InterfaceID)] {
			interfaces = append(interfaces, strings.TrimSpace(plan.InterfaceID))
			seenInterfaces[strings.TrimSpace(plan.InterfaceID)] = true
		}
		subnet, err := keaDHCP4Subnet(index+1, plan)
		if err != nil {
			return nil, err
		}
		subnets = append(subnets, subnet)
	}
	content, err := marshalIndented(map[string]any{"Dhcp4": map[string]any{"interfaces-config": map[string]any{"interfaces": interfaces}, "subnet4": subnets}})
	if err != nil {
		return nil, err
	}
	return []RenderedArtifact{NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", content, "restart")}, nil
}

func keaDHCP4Subnet(id int, plan KeaDHCP4Plan) (map[string]any, error) {
	if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.InterfaceID) == "" || strings.TrimSpace(plan.Subnet) == "" {
		return nil, fmt.Errorf("kea dhcp4 plan requires id, interface_id, and subnet")
	}
	if len(plan.Pools) == 0 {
		return nil, fmt.Errorf("kea dhcp4 plan requires at least one pool")
	}
	poolEntries := make([]map[string]string, 0, len(plan.Pools))
	for _, pool := range plan.Pools {
		poolEntries = append(poolEntries, map[string]string{"pool": strings.ReplaceAll(strings.TrimSpace(pool), "-", " - ")})
	}
	optionData := make([]map[string]string, 0, len(plan.Routers)+len(plan.NameServers))
	if len(plan.Routers) > 0 {
		optionData = append(optionData, map[string]string{"name": "routers", "data": strings.Join(plan.Routers, ", ")})
	}
	if len(plan.NameServers) > 0 {
		optionData = append(optionData, map[string]string{"name": "domain-name-servers", "data": strings.Join(plan.NameServers, ", ")})
	}
	subnet := map[string]any{"id": id, "subnet": plan.Subnet, "pools": poolEntries}
	if plan.LeaseTime > 0 {
		subnet["valid-lifetime"] = plan.LeaseTime
	}
	if len(optionData) > 0 {
		subnet["option-data"] = optionData
	}
	if len(plan.Reservations) > 0 {
		reservations := make([]map[string]string, 0, len(plan.Reservations))
		for _, reservation := range plan.Reservations {
			entry := map[string]string{"hw-address": strings.TrimSpace(reservation.HWAddress), "ip-address": strings.TrimSpace(reservation.IPAddress)}
			if strings.TrimSpace(reservation.Hostname) != "" {
				entry["hostname"] = strings.TrimSpace(reservation.Hostname)
			}
			reservations = append(reservations, entry)
		}
		subnet["reservations"] = reservations
	}
	return subnet, nil
}

func RenderXray(compiled proxy.CompiledEgress) ([]RenderedArtifact, error) {
	if strings.TrimSpace(compiled.ID) == "" || compiled.XrayRuntime.Engine != proxy.Xray {
		return nil, fmt.Errorf("compiled proxy egress with xray runtime is required")
	}
	content, err := marshalIndented(compiled.XrayRuntime.ConfigPayload)
	if err != nil {
		return nil, err
	}
	return []RenderedArtifact{NewArtifact(Xray, compiled.XrayRuntime.ConfigPath, content, "restart")}, nil
}

func RenderNftablesCapture(plan proxy.NftablesCapturePlan) ([]RenderedArtifact, error) {
	return RenderGatewayNftablesCapture(plan, DNSInterceptionPlan{})
}

type DNSInterceptionPlan struct {
	LANInterfaces []string `json:"lan_interfaces"`
	ListenPort    int      `json:"listen_port"`
}

func RenderGatewayNftablesCapture(plan proxy.NftablesCapturePlan, dns DNSInterceptionPlan) ([]RenderedArtifact, error) {
	hasProxy := strings.TrimSpace(plan.Family) != "" || strings.TrimSpace(plan.Table) != ""
	hasDNS := len(dns.LANInterfaces) > 0
	if !hasProxy && !hasDNS {
		return nil, nil
	}
	if hasProxy && (strings.TrimSpace(plan.Family) == "" || strings.TrimSpace(plan.Table) == "") {
		return nil, fmt.Errorf("nftables capture plan requires family and table")
	}
	port := dns.ListenPort
	if port == 0 {
		port = 53
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("DNS interception listen port is invalid")
	}
	family, table := plan.Family, plan.Table
	if !hasProxy {
		family, table = "inet", "ly_route_dns_capture"
	}
	seen := map[string]bool{}
	lanInterfaces := make([]string, 0, len(dns.LANInterfaces))
	for _, name := range dns.LANInterfaces {
		name = strings.TrimSpace(name)
		if name == "" || !serviceTokenSafe(name) {
			return nil, fmt.Errorf("DNS interception LAN interface %q is unsafe", name)
		}
		if !seen[name] {
			seen[name] = true
			lanInterfaces = append(lanInterfaces, name)
		}
	}
	slices.Sort(lanInterfaces)
	var builder strings.Builder
	builder.WriteString("#!/usr/sbin/nft -f\n\n")
	builder.WriteString("flush ruleset\n\n")
	builder.WriteString(fmt.Sprintf("table %s %s {\n", family, table))
	for _, chain := range plan.Chains {
		builder.WriteString(fmt.Sprintf("  chain %s {\n", chain.Name))
		builder.WriteString(fmt.Sprintf("    type %s hook %s priority %d; policy %s;\n", chain.Type, chain.Hook, chain.Priority, chain.Policy))
		for _, rule := range plan.Rules {
			if rule.Chain == chain.Name {
				builder.WriteString(fmt.Sprintf("    %s %s\n", rule.Expression, rule.Action))
			}
		}
		builder.WriteString("  }\n")
	}
	if len(lanInterfaces) > 0 {
		builder.WriteString("  chain dns_prerouting {\n")
		builder.WriteString("    type nat hook prerouting priority -100; policy accept;\n")
		for _, name := range lanInterfaces {
			builder.WriteString(fmt.Sprintf("    iifname %q udp dport 53 counter redirect to :%d\n", name, port))
			builder.WriteString(fmt.Sprintf("    iifname %q tcp dport 53 counter redirect to :%d\n", name, port))
		}
		builder.WriteString("  }\n")
	}
	builder.WriteString("}\n")
	return []RenderedArtifact{NewArtifact(Nftables, "/etc/nftables.conf", builder.String(), "reload")}, nil
}

func serviceTokenSafe(value string) bool {
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return value != ""
}

func RenderLinuxPolicyRouting(plan proxy.LinuxPolicyRoutingPlan) ([]RenderedArtifact, error) {
	if strings.TrimSpace(plan.Mark) == "" || plan.TableID <= 0 || plan.RulePriority <= 0 || strings.TrimSpace(plan.DefaultRoute.Device) == "" {
		return nil, fmt.Errorf("linux policy routing plan requires mark, table, priority, and default route device")
	}
	var builder strings.Builder
	builder.WriteString("#!/bin/sh\nset -eu\n\n")
	builder.WriteString(fmt.Sprintf("ip rule del fwmark %s/%s table %d priority %d 2>/dev/null || true\n", plan.Mark, plan.MarkMask, plan.TableID, plan.RulePriority))
	builder.WriteString(fmt.Sprintf("ip rule add fwmark %s/%s table %d priority %d\n", plan.Mark, plan.MarkMask, plan.TableID, plan.RulePriority))
	if strings.TrimSpace(plan.DefaultRoute.Via) != "" {
		builder.WriteString(fmt.Sprintf("ip route replace %s via %s dev %s table %d\n", plan.DefaultRoute.Destination, plan.DefaultRoute.Via, plan.DefaultRoute.Device, plan.TableID))
	} else {
		builder.WriteString(fmt.Sprintf("ip route replace %s dev %s scope %s table %d\n", plan.DefaultRoute.Destination, plan.DefaultRoute.Device, plan.DefaultRoute.Scope, plan.TableID))
	}
	return []RenderedArtifact{NewArtifact(LinuxRouting, "/var/lib/ly-route/policy-routing/apply.sh", builder.String(), "restart")}, nil
}

func RenderPPPoE(peer PPPoEPeer) ([]RenderedArtifact, error) {
	return RenderPPPoEConfig([]PPPoEPeer{peer})
}

func RenderPPPoEConfig(peers []PPPoEPeer) ([]RenderedArtifact, error) {
	if len(peers) == 0 {
		return nil, fmt.Errorf("pppoe config requires at least one peer")
	}
	artifacts := make([]RenderedArtifact, 0, len(peers))
	seenIDs := map[string]bool{}
	seenUsers := map[string]bool{}
	for _, peer := range peers {
		if strings.TrimSpace(peer.ID) == "" || strings.TrimSpace(peer.Interface) == "" || strings.TrimSpace(peer.Username) == "" {
			return nil, fmt.Errorf("pppoe peer requires id, interface, and username")
		}
		if err := requirePPPoEToken(peer.ID, "id"); err != nil {
			return nil, err
		}
		if err := requirePPPoEToken(peer.Interface, "interface"); err != nil {
			return nil, err
		}
		if err := requirePPPoEToken(peer.Username, "username"); err != nil {
			return nil, err
		}
		if strings.ContainsAny(peer.Password, "\x00\n\r") {
			return nil, fmt.Errorf("pppoe password contains unsupported control characters")
		}
		if seenIDs[peer.ID] {
			return nil, fmt.Errorf("duplicate pppoe peer id %q", peer.ID)
		}
		if seenUsers[peer.Username] {
			return nil, fmt.Errorf("duplicate pppoe username %q", peer.Username)
		}
		seenIDs[peer.ID], seenUsers[peer.Username] = true, true
		mtu := peer.MTU
		if mtu == 0 {
			mtu = 1492
		}
		mru := peer.MRU
		if mru == 0 {
			mru = 1492
		}
		if mtu < 1280 || mtu > 1492 || mru < 1280 || mru > 1492 {
			return nil, fmt.Errorf("pppoe mtu and mru must be between 1280 and 1492")
		}
		digest := sha256.Sum256([]byte(peer.ID))
		tapID := 500 + int(binary.BigEndian.Uint16(digest[:2]))%3000
		controlInterface := "lyppp-" + hex.EncodeToString(digest[:4])
		wanInterface := peer.Interface
		if !strings.HasPrefix(wanInterface, "lyroute-") {
			wanInterface = "lyroute-" + wanInterface
		}
		content, err := marshalIndented(map[string]any{
			"id":                  peer.ID,
			"control_interface":   controlInterface,
			"wan_interface":       wanInterface,
			"username":            peer.Username,
			"password":            peer.Password,
			"mru":                 mru,
			"tap_id":              tapID,
			"vppctl":              "/usr/bin/vppctl",
			"status_file":         "/run/ly-route/pppoe/" + peer.ID + ".json",
			"default_route":       true,
			"nat":                 true,
			"ipv6_prefix_group":   peer.IPv6PrefixGroup,
			"ipv6_lan_interfaces": peer.IPv6LANInterfaces,
		})
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, NewArtifact(PPPd, "/etc/ly-route/pppoe/ly-route-"+peer.ID+".json", content, "restart"))
	}
	return artifacts, nil
}

func PPPoEInterfaceName(id string) string {
	id = strings.TrimSpace(id)
	if len("ppp-"+id) <= 15 {
		return "ppp-" + id
	}
	digest := sha256.Sum256([]byte(id))
	return "ppp-" + id[:6] + "-" + hex.EncodeToString(digest[:2])
}

func NewPPPoEStatus(peer PPPoEPeer, state PPPoEState, assignedIPv4, assignedIPv6 string, tableID int, lastError string) (PPPoEStatus, error) {
	if strings.TrimSpace(peer.ID) == "" || strings.TrimSpace(peer.Interface) == "" {
		return PPPoEStatus{}, fmt.Errorf("pppoe status requires peer id and interface")
	}
	if err := requirePPPoEToken(peer.ID, "id"); err != nil {
		return PPPoEStatus{}, err
	}
	if err := requirePPPoEToken(peer.Interface, "interface"); err != nil {
		return PPPoEStatus{}, err
	}
	switch state {
	case PPPoEDisconnected, PPPoEConnecting, PPPoEConnected, PPPoEFailed:
	default:
		return PPPoEStatus{}, fmt.Errorf("unsupported pppoe state %q", state)
	}
	status := PPPoEStatus{PeerID: peer.ID, Interface: peer.Interface, State: state, AssignedIPv4: strings.TrimSpace(assignedIPv4), AssignedIPv6: strings.TrimSpace(assignedIPv6), VPPTableID: tableID, LastError: strings.TrimSpace(lastError)}
	status.RouteReady = state == PPPoEConnected && status.AssignedIPv4 != "" && tableID > 0
	if status.RouteReady {
		status.VPPRouteHandoff = "vpp.fib.route"
	}
	return status, nil
}

func PPPoEVPPRouteHandoff(status PPPoEStatus, requestID string) ([]vpp.Operation, error) {
	if !status.RouteReady {
		return nil, fmt.Errorf("pppoe route handoff is not ready")
	}
	handoff := PPPoERouteHandoff{PeerID: status.PeerID, Interface: status.Interface, VPPTableID: status.VPPTableID, Destination: "0.0.0.0/0", Ready: true}
	return []vpp.Operation{{Name: "vpp.fib.route", RequestID: requestID, Resource: status.PeerID, Payload: handoff}}, nil
}

func requirePPPoEToken(value, field string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("pppoe %s is required", field)
	}
	if trimmed != value || strings.ContainsAny(trimmed, "\x00\n\r\t ;|&$()<>\\\"") {
		return fmt.Errorf("pppoe %s contains unsupported characters", field)
	}
	return nil
}

func marshalIndented(value any) (string, error) {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}

type FilesystemController struct {
	RootDir        string
	Runner         CommandRunner
	Now            func() time.Time
	XrayAPIAddress string
}

func (controller FilesystemController) ReloadOrRestart(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	return controller.applyWithRecovery(ctx, service, artifacts)
}

func (controller FilesystemController) Status(ctx context.Context, service ServiceName) (Health, error) {
	if controller.Runner == nil {
		return Health{Service: service, Available: false, Reason: "service command runner is not configured"}, nil
	}
	return controller.Runner.Status(ctx, service)
}

func (controller FilesystemController) Rollback(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	return controller.rollbackWithSnapshot(ctx, service, artifacts)
}

func (controller FilesystemController) Logs(ctx context.Context, service ServiceName, lines int) (string, error) {
	if lines <= 0 {
		lines = 100
	}
	output, err := exec.CommandContext(ctx, "journalctl", "-u", serviceUnit(service), "--no-pager", "-n", strconv.Itoa(lines)).CombinedOutput()
	text := Redact(strings.TrimSpace(string(output)))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, err
	}
	return text, nil
}

func (controller FilesystemController) writeArtifacts(service ServiceName, artifacts []RenderedArtifact) error {
	if service == SmartDNS {
		if err := controller.removeStaleSmartDNSArtifacts(artifacts); err != nil {
			return err
		}
	}
	for _, artifact := range artifacts {
		path, err := controller.resolvePath(artifact.Path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0640)
		if strings.HasSuffix(path, ".sh") {
			mode = 0750
		}
		if service == PPPd && strings.HasPrefix(artifact.Path, "/etc/ly-route/pppoe/") {
			mode = 0600
		}
		if err := writeFileAtomically(path, []byte(artifact.Content), mode); err != nil {
			return err
		}
	}
	return nil
}

func (controller FilesystemController) removeStaleSmartDNSArtifacts(artifacts []RenderedArtifact) error {
	keep := map[string]bool{}
	for _, artifact := range artifacts {
		if artifact.Service == SmartDNS {
			keep[filepath.Base(artifact.Path)] = true
		}
	}
	dir, err := controller.resolvePath("/etc/smartdns/conf.d")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "ly-route-") || !strings.HasSuffix(name, ".conf") {
			continue
		}
		if keep[name] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (controller FilesystemController) resolvePath(path string) (string, error) {
	clean := filepath.Clean("/" + strings.TrimSpace(path))
	if clean == "/" || strings.Contains(clean, "..") {
		return "", fmt.Errorf("invalid artifact path %q", path)
	}
	if controller.RootDir == "" {
		return clean, nil
	}
	return filepath.Join(controller.RootDir, strings.TrimPrefix(clean, "/")), nil
}

func serviceUnit(service ServiceName) string {
	return healthUnit(service)
}

func applyUnit(service ServiceName) string {
	switch service {
	case SmartDNS:
		return "smartdns.service"
	case Kea:
		return "kea-dhcp4-server.service"
	case Xray:
		return "xray.service"
	case PPPd:
		return "ly-route-pppoe.target"
	case VPP:
		return "ly-route-vpp-apply.service"
	case Nftables:
		return "nftables.service"
	case LinuxRouting:
		return "ly-route-policy-routing.service"
	case IPv6RA:
		return "radvd.service"
	default:
		return string(service) + ".service"
	}
}

func applyCommand(service ServiceName) string {
	switch service {
	case VPP, LinuxRouting:
		return "restart"
	default:
		return "reload-or-restart"
	}
}

func directApplyHelper(service ServiceName) string {
	switch service {
	case VPP:
		return "/usr/lib/ly-route/vpp-apply-default"
	case LinuxRouting:
		return "/usr/lib/ly-route/policy-routing-apply-default"
	default:
		return ""
	}
}

func healthUnit(service ServiceName) string {
	switch service {
	case VPP:
		return "vpp.service"
	default:
		return applyUnit(service)
	}
}

func (runtime Runtime) Apply(ctx context.Context, artifacts []RenderedArtifact) error {
	return runtime.ApplyCapabilities(ctx, artifacts).Err()
}

func (runtime Runtime) Stop(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	if runtime.Controller == nil {
		return fmt.Errorf("%s stop requires a service controller", service)
	}
	controller, ok := runtime.Controller.(StopController)
	if !ok {
		return fmt.Errorf("%s service controller does not support verified stop", service)
	}
	return controller.Stop(ctx, service, artifacts)
}

func (runtime Runtime) HealthCheck(ctx context.Context, services ...ServiceName) ([]Health, error) {
	results := make([]Health, 0, len(services))
	for _, service := range services {
		health, err := runtime.Controller.Status(ctx, service)
		if err != nil {
			return nil, err
		}
		results = append(results, health)
	}
	return results, nil
}

func (runtime Runtime) Rollback(ctx context.Context, artifacts []RenderedArtifact) error {
	byService := groupByService(artifacts)
	order := serviceOrder()
	rollbackErrors := make([]error, 0)
	for i := len(order) - 1; i >= 0; i-- {
		service := order[i]
		items := byService[service]
		if len(items) == 0 {
			continue
		}
		if err := runtime.Controller.Rollback(ctx, service, items); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback %s: %w", service, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func Redact(value string) string {
	redacted := value
	for _, marker := range []string{"xray://", "vmess://", "vless://", "trojan://", "ss://", "subscription", "credential", "password", "token", "secret", "private_key"} {
		for {
			index := strings.Index(strings.ToLower(redacted), marker)
			if index < 0 {
				break
			}
			end := index + len(marker)
			for end < len(redacted) && !strings.ContainsRune(" \n\t\r\"'", rune(redacted[end])) {
				end++
			}
			redacted = redacted[:index] + "[redacted]" + redacted[end:]
		}
	}
	return redacted
}

func groupByService(artifacts []RenderedArtifact) map[ServiceName][]RenderedArtifact {
	grouped := map[ServiceName][]RenderedArtifact{}
	for _, artifact := range artifacts {
		grouped[artifact.Service] = append(grouped[artifact.Service], artifact)
	}
	return grouped
}

func serviceOrder() []ServiceName {
	return []ServiceName{SmartDNS, Kea, Xray, Nftables, LinuxRouting, VPP, PPPd, IPv6RA}
}
