package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"ly-route/backend/internal/runtime/dns"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/vpp"
)

type ServiceName string

const (
	SmartDNS ServiceName = "smartdns"
	Kea      ServiceName = "kea"
	Xray     ServiceName = "xray"
	// PPPoE is the product runtime. The dialer is ly-route-pppoe-client;
	// pppd is not part of the appliance runtime.
	PPPoE ServiceName = "pppoe"
	// PPPd is a deprecated source-compatibility alias for old test helpers.
	// It resolves to the native PPPoE service name and is not used for
	// package, unit, or process discovery.
	PPPd         ServiceName = PPPoE
	VPP          ServiceName = "vpp"
	Nftables     ServiceName = "nftables"
	LinuxRouting ServiceName = "linux-routing"
	IPv6RA       ServiceName = "ipv6-ra"
)

const linuxRoutingResetMarker = "# ly-route-linux-routing-reset"

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

func LinuxRoutingResetArtifact() RenderedArtifact {
	return NewArtifact(
		LinuxRouting,
		"/var/lib/ly-route/policy-routing/apply.sh",
		"#!/bin/sh\nset -eu\n"+linuxRoutingResetMarker+"\n",
		"restart",
	)
}

type SmartDNSPlan struct {
	ID         string
	Render     dns.SmartDNSRender
	Upstreams  []SmartDNSUpstream
	Cache      SmartDNSCache
	DomainSets map[string][]string
}

type smartDNSDomainSetArtifact struct {
	Path    string
	Content string
}

type smartDNSDomainSelector struct {
	matchKind string
	domain    string
	domainSet string
}

// SmartDNSUpstream is a validated resolver group. A WAN-pinned upstream uses
// SocketMark to select a dedicated Linux peer of a VPP DNS service network,
// never the physical WAN device that VPP owns.
type SmartDNSUpstream struct {
	ID               string
	Servers          []string
	BootstrapServers []string
	ResolvedHostIPs  map[string][]string
	Interface        string
	WANEgressID      string
	SocketMark       uint32
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
	ID                  string   `json:"id"`
	Interface           string   `json:"interface"`
	Username            string   `json:"username"`
	Password            string   `json:"password"`
	MTU                 int      `json:"mtu,omitempty"`
	MRU                 int      `json:"mru,omitempty"`
	NATInsideInterfaces []string `json:"nat_inside_interfaces,omitempty"`
	NATBehavior         string   `json:"nat_behavior,omitempty"`
	IPv6PrefixGroup     string   `json:"ipv6_prefix_group,omitempty"`
	IPv6LANInterfaces   []string `json:"ipv6_lan_interfaces,omitempty"`
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
	ACMAC           string     `json:"ac_mac,omitempty"`
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
	content, sourceRoutes, domainSetArtifacts, err := renderSmartDNSBundle(plans)
	if err != nil {
		return nil, err
	}
	artifacts := []RenderedArtifact{
		NewArtifact(SmartDNS, "/etc/smartdns/conf.d/ly-route-active.conf", content, "reload"),
		NewArtifact(SmartDNS, "/etc/ly-route/dns-source-routes.conf", sourceRoutes, "reload"),
	}
	for _, artifact := range domainSetArtifacts {
		artifacts = append(artifacts, NewArtifact(SmartDNS, artifact.Path, artifact.Content, "reload"))
	}
	return artifacts, nil
}

func renderSmartDNSBundle(plans []SmartDNSPlan) (string, string, []smartDNSDomainSetArtifact, error) {
	seenPlans := map[string]struct{}{}
	upstreams := make(map[string]SmartDNSUpstream)
	wanUpstreams := make(map[string]string)
	for _, plan := range plans {
		planID := strings.TrimSpace(plan.ID)
		if planID == "" {
			return "", "", nil, fmt.Errorf("smartdns plan id is required")
		}
		if _, exists := seenPlans[planID]; exists {
			return "", "", nil, fmt.Errorf("duplicate smartdns policy plan %q", planID)
		}
		seenPlans[planID] = struct{}{}
		for _, upstream := range plan.Upstreams {
			if err := validateSmartDNSUpstream(upstream); err != nil {
				return "", "", nil, err
			}
			if existing, exists := upstreams[upstream.ID]; exists && !sameSmartDNSUpstream(existing, upstream) {
				return "", "", nil, fmt.Errorf("smartdns upstream %q is defined inconsistently", upstream.ID)
			}
			upstreams[upstream.ID] = upstream
			if upstream.WANEgressID != "" {
				wanUpstreams[upstream.WANEgressID] = upstream.ID
			}
		}
	}
	domainSets, domainSetNames, domainSetArtifacts, err := collectSmartDNSDomainSets(plans)
	if err != nil {
		return "", "", nil, err
	}

	var builder strings.Builder
	builder.WriteString("# Generated by Ly Route. Do not edit.\n")
	cache := plans[0].Cache
	if err := writeSmartDNSCache(&builder, cache); err != nil {
		return "", "", nil, err
	}
	ids := make([]string, 0, len(upstreams))
	for id := range upstreams {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var bootstrapRules strings.Builder
	for _, id := range ids {
		if err := writeSmartDNSUpstream(&builder, &bootstrapRules, upstreams[id]); err != nil {
			return "", "", nil, err
		}
	}
	writeSmartDNSDomainSetDeclarations(&builder, domainSets, domainSetNames)
	var sourceRoutes strings.Builder
	sourceRoutes.WriteString("# source-prefix match-kind domain smartdns-port\n")
	nextSourcePort := 12000
	for _, plan := range plans {
		if err := writeSmartDNSRender(&builder, &sourceRoutes, &nextSourcePort, plan.ID, plan.Render, upstreams, wanUpstreams, domainSets, domainSetNames); err != nil {
			return "", "", nil, err
		}
	}
	if bootstrapRules.Len() > 0 {
		builder.WriteString("# Bootstrap hostname resolution takes precedence over policy miss rules.\n")
		builder.WriteString(bootstrapRules.String())
	}
	return builder.String(), sourceRoutes.String(), domainSetArtifacts, nil
}

func writeSmartDNSRender(builder, sourceRoutes *strings.Builder, nextSourcePort *int, planID string, render dns.SmartDNSRender, upstreams map[string]SmartDNSUpstream, wanUpstreams map[string]string, domainSets map[string][]string, domainSetNames map[string]string) error {
	if strings.TrimSpace(render.Engine) != "smartdns" {
		return fmt.Errorf("smartdns render requires smartdns engine")
	}
	builder.WriteString("# Generated by Ly Route.\n")
	builder.WriteString("# Decision precedence: ")
	builder.WriteString(strings.TrimSpace(render.DecisionPrecedence))
	builder.WriteString("\n\n")

	for _, rule := range render.Rules {
		if err := writeSmartDNSRule(builder, sourceRoutes, nextSourcePort, planID, rule, upstreams, wanUpstreams, domainSets, domainSetNames); err != nil {
			return err
		}
	}
	if err := writeSmartDNSMissRule(builder, render.Miss, upstreams, wanUpstreams); err != nil {
		return err
	}
	return nil
}

func writeSmartDNSRule(builder, sourceRoutes *strings.Builder, nextSourcePort *int, planID string, rule dns.SmartDNSRule, upstreams map[string]SmartDNSUpstream, wanUpstreams map[string]string, domainSets map[string][]string, domainSetNames map[string]string) error {
	ruleID := strings.TrimSpace(rule.RuleID)
	if ruleID == "" {
		return fmt.Errorf("smartdns rule id is required")
	}
	selectors, err := smartDNSDomainSelectors(rule, domainSets, domainSetNames)
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
		builder.WriteString("group-begin ")
		builder.WriteString(group)
		builder.WriteString(" -inherit none\n")
		sourceSelectors, err := smartDNSSourceSelectors(selectors, domainSets)
		if err != nil {
			return fmt.Errorf("smartdns rule %q source selectors: %w", ruleID, err)
		}
		for _, prefix := range rule.SourcePrefixes {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" || strings.ContainsAny(prefix, " \t\n\r") {
				return fmt.Errorf("smartdns rule %q has invalid source prefix %q", ruleID, prefix)
			}
			for _, selector := range sourceSelectors {
				fmt.Fprintf(sourceRoutes, "%s %s %s %d\n", prefix, selector.matchKind, selector.domain, port)
			}
		}
		if err := writeSmartDNSRuleContent(builder, rule, selectors, upstreams, wanUpstreams); err != nil {
			return err
		}
		builder.WriteString("group-end\n")
		fmt.Fprintf(builder, "bind 127.0.0.1:%d -group %s -no-cache -no-speed-check\n", port, group)
		fmt.Fprintf(builder, "bind-tcp 127.0.0.1:%d -group %s -no-cache -no-speed-check\n\n", port, group)
		return nil
	}
	return writeSmartDNSRuleContent(builder, rule, selectors, upstreams, wanUpstreams)
}

func smartDNSDomainSelectors(rule dns.SmartDNSRule, domainSets map[string][]string, domainSetNames map[string]string) ([]smartDNSDomainSelector, error) {
	selectors := make([]smartDNSDomainSelector, 0, len(rule.Domains)+len(rule.DomainSuffixes))
	seen := map[string]struct{}{}
	add := func(value, matchKind string) error {
		var err error
		value, err = normalizeSmartDNSDomain(value, matchKind)
		if err != nil {
			return err
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
		_, exists := domainSets[setID]
		if !exists {
			return nil, fmt.Errorf("domain set %q is unavailable", setID)
		}
		setName, exists := domainSetNames[setID]
		if !exists || !smartDNSToken(setName) {
			return nil, fmt.Errorf("domain set %q has no valid SmartDNS name", setID)
		}
		key := "domain-set\x00" + setID
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			selectors = append(selectors, smartDNSDomainSelector{
				matchKind: "domain-set",
				domain:    "domain-set:" + setName,
				domainSet: setID,
			})
		}
	}
	return selectors, nil
}

func collectSmartDNSDomainSets(plans []SmartDNSPlan) (map[string][]string, map[string]string, []smartDNSDomainSetArtifact, error) {
	sets := map[string][]string{}
	for _, plan := range plans {
		for rawID, rawEntries := range plan.DomainSets {
			setID := strings.TrimSpace(rawID)
			if setID == "" {
				return nil, nil, nil, fmt.Errorf("smartdns domain set id is required")
			}
			entries, err := normalizeSmartDNSDomainSetEntries(rawEntries)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("domain set %q: %w", setID, err)
			}
			if existing, exists := sets[setID]; exists && !slices.Equal(existing, entries) {
				return nil, nil, nil, fmt.Errorf("smartdns domain set %q is defined inconsistently", setID)
			}
			sets[setID] = entries
		}
	}

	ids := make([]string, 0, len(sets))
	for setID := range sets {
		ids = append(ids, setID)
	}
	slices.Sort(ids)
	names := make(map[string]string, len(ids))
	artifacts := make([]smartDNSDomainSetArtifact, 0, len(ids))
	for _, setID := range ids {
		name := smartDNSDomainSetName(setID)
		names[setID] = name
		artifacts = append(artifacts, smartDNSDomainSetArtifact{
			Path:    "/etc/smartdns/domain-sets/" + name + ".list",
			Content: renderSmartDNSDomainSetFile(sets[setID]),
		})
	}
	return sets, names, artifacts, nil
}

func normalizeSmartDNSDomainSetEntries(entries []string) ([]string, error) {
	seen := make(map[string]struct{}, len(entries))
	result := make([]string, 0, len(entries))
	for _, rawEntry := range entries {
		entry := strings.TrimSpace(rawEntry)
		if entry == "" {
			continue
		}
		kind := "exact"
		if strings.HasPrefix(entry, ".") || strings.HasPrefix(entry, "*.") {
			kind = "suffix"
		}
		domain, err := normalizeSmartDNSDomain(entry, kind)
		if err != nil {
			return nil, err
		}
		// Keep the match kind in the in-memory representation. The list file
		// is rendered separately, so source-prefix routing can still expand
		// the set without guessing whether an entry was exact or a suffix.
		encoded := kind + "\x00" + domain
		if _, exists := seen[encoded]; exists {
			continue
		}
		seen[encoded] = struct{}{}
		result = append(result, encoded)
	}
	slices.Sort(result)
	return result, nil
}

func renderSmartDNSDomainSetFile(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	lines := make([]string, 0, len(entries))
	for _, encoded := range entries {
		kind, domain, ok := strings.Cut(encoded, "\x00")
		if !ok {
			continue
		}
		if kind == "exact" {
			domain = "-." + domain
		}
		lines = append(lines, domain)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func smartDNSDomainSetName(setID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(setID)))
	return fmt.Sprintf("lyroute-set-%x", digest[:8])
}

func writeSmartDNSDomainSetDeclarations(builder *strings.Builder, domainSets map[string][]string, domainSetNames map[string]string) {
	if len(domainSets) == 0 {
		return
	}
	ids := make([]string, 0, len(domainSets))
	for setID := range domainSets {
		ids = append(ids, setID)
	}
	slices.Sort(ids)
	builder.WriteString("# Domain sets are kept in separate list files to avoid per-domain rule expansion.\n")
	for _, setID := range ids {
		name := domainSetNames[setID]
		fmt.Fprintf(builder, "domain-set -name %s -type list -file /etc/smartdns/domain-sets/%s.list\n", name, name)
	}
	builder.WriteByte('\n')
}

func normalizeSmartDNSDomain(value, matchKind string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	value = strings.TrimPrefix(value, "*.")
	if matchKind == "suffix" {
		value = strings.TrimPrefix(value, ".")
	}
	if value == "" || strings.ContainsAny(value, "/ \t\n\r") || !smartDNSDomainName(value) {
		return "", fmt.Errorf("invalid %s domain %q", matchKind, value)
	}
	return value, nil
}

func smartDNSSourceSelectors(selectors []smartDNSDomainSelector, domainSets map[string][]string) ([]smartDNSDomainSelector, error) {
	result := make([]smartDNSDomainSelector, 0, len(selectors))
	seen := map[string]struct{}{}
	add := func(value, kind string) error {
		normalized, err := normalizeSmartDNSDomain(value, kind)
		if err != nil {
			return err
		}
		key := kind + "\x00" + normalized
		if _, exists := seen[key]; exists {
			return nil
		}
		seen[key] = struct{}{}
		result = append(result, smartDNSDomainSelector{matchKind: kind, domain: normalized})
		return nil
	}
	for _, selector := range selectors {
		if selector.matchKind != "domain-set" {
			if err := add(selector.domain, selector.matchKind); err != nil {
				return nil, err
			}
			continue
		}
		entries, exists := domainSets[selector.domainSet]
		if !exists {
			return nil, fmt.Errorf("domain set %q is unavailable", selector.domainSet)
		}
		for _, entry := range entries {
			kind, domain, ok := strings.Cut(entry, "\x00")
			if !ok || (kind != "exact" && kind != "suffix") {
				return nil, fmt.Errorf("domain set %q contains malformed entry", selector.domainSet)
			}
			if err := add(domain, kind); err != nil {
				return nil, fmt.Errorf("domain set %q: %w", selector.domainSet, err)
			}
		}
	}
	return result, nil
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
			// SmartDNS applies a domain-specific nameserver before a global
			// `address #` miss rule. Rendering `address /domain/-` here would
			// instead turn a successful upstream reply into an empty answer.
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
		group, err := smartDNSGroupForRule(rule, upstreams, wanUpstreams)
		if err != nil {
			return fmt.Errorf("smartdns miss policy: %w", err)
		}
		if group != "" {
			builder.WriteString("nameserver /./")
			builder.WriteString(group)
			builder.WriteByte('\n')
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
	// Policy rules can change while cache entries retain their previous group.
	// Keep the runtime cache, but never restore entries from an older policy.
	builder.WriteString("cache-persist no\n")
	// DNS-to-VPP handoff reads the kernel timeout so expired answers can be
	// removed without retaining stale policy routes.
	builder.WriteString("ipset-timeout yes\n")
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
	for _, server := range upstream.BootstrapServers {
		address, err := netip.ParseAddr(strings.TrimSpace(server))
		if err != nil || !address.IsValid() {
			return fmt.Errorf("smartdns upstream %q has invalid bootstrap DNS server %q", upstream.ID, server)
		}
	}
	for host, addresses := range upstream.ResolvedHostIPs {
		if !smartDNSDomainName(host) || len(addresses) == 0 {
			return fmt.Errorf("smartdns upstream %q has invalid resolved DoH host %q", upstream.ID, host)
		}
		for _, address := range addresses {
			parsed, err := netip.ParseAddr(strings.TrimSpace(address))
			if err != nil || !parsed.Is4() {
				return fmt.Errorf("smartdns upstream %q has invalid resolved DoH address %q", upstream.ID, address)
			}
		}
	}
	return nil
}

func writeSmartDNSUpstream(builder, bootstrapRules *strings.Builder, upstream SmartDNSUpstream) error {
	bootstrapGroup := upstream.ID + "-bootstrap"
	if len(upstream.BootstrapServers) > 0 {
		builder.WriteString("# Bootstrap DNS for ")
		builder.WriteString(upstream.ID)
		builder.WriteByte('\n')
		for _, server := range upstream.BootstrapServers {
			builder.WriteString("server ")
			builder.WriteString(strings.TrimSpace(server))
			builder.WriteString(" -group ")
			builder.WriteString(bootstrapGroup)
			builder.WriteString(" -exclude-default-group")
			writeSmartDNSRoutingSelector(builder, upstream)
			builder.WriteByte('\n')
		}
		for _, server := range upstream.Servers {
			host := smartDNSUpstreamHostname(server)
			if host != "" {
				fmt.Fprintf(bootstrapRules, "nameserver /%s/%s\n", host, bootstrapGroup)
			}
		}
	}
	for _, server := range upstream.Servers {
		directive := "server"
		lower := strings.ToLower(strings.TrimSpace(server))
		if strings.HasPrefix(lower, "https://") {
			directive = "server-https"
		} else if strings.HasPrefix(lower, "h3://") {
			directive = "server-h3"
		}
		host := smartDNSUpstreamHostname(server)
		endpoints := []string{strings.TrimSpace(server)}
		if directive != "server" && host != "" && len(upstream.ResolvedHostIPs[host]) > 0 {
			endpoints = make([]string, 0, len(upstream.ResolvedHostIPs[host]))
			for _, address := range upstream.ResolvedHostIPs[host] {
				endpoint, err := smartDNSDoHEndpointWithHostIP(server, address)
				if err != nil {
					return fmt.Errorf("smartdns upstream %q: %w", upstream.ID, err)
				}
				endpoints = append(endpoints, endpoint)
			}
		}
		for _, endpoint := range endpoints {
			builder.WriteString(directive)
			builder.WriteByte(' ')
			builder.WriteString(endpoint)
			builder.WriteString(" -group ")
			builder.WriteString(upstream.ID)
			builder.WriteString(" -exclude-default-group")
			if endpoint != strings.TrimSpace(server) {
				fmt.Fprintf(builder, " -host-name %s -http-host %s -tls-host-verify %s", host, host, host)
			}
			writeSmartDNSRoutingSelector(builder, upstream)
			builder.WriteByte('\n')
		}
	}
	return nil
}

func writeSmartDNSRoutingSelector(builder *strings.Builder, upstream SmartDNSUpstream) {
	if upstream.SocketMark != 0 {
		// SmartDNS parses -set-mark as decimal. A hexadecimal literal is
		// accepted syntactically but resolves to mark zero at runtime.
		fmt.Fprintf(builder, " -set-mark %d", upstream.SocketMark)
	}
	if upstream.Interface != "" {
		builder.WriteString(" -interface ")
		builder.WriteString(upstream.Interface)
	}
}

func smartDNSUpstreamHostname(server string) string {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if _, err := netip.ParseAddr(host); err == nil {
		return ""
	}
	if !smartDNSDomainName(host) {
		return ""
	}
	return host
}

func smartDNSDoHEndpointWithHostIP(server, address string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid DoH server %q", server)
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(address))
	if err != nil || !ip.Is4() {
		return "", fmt.Errorf("invalid DoH host address %q", address)
	}
	host := ip.String()
	if port := parsed.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	parsed.Host = host
	return parsed.String(), nil
}

func smartDNSDomainName(value string) bool {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, " /\t\r\n") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
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
	if left.ID != right.ID || left.Interface != right.Interface || left.WANEgressID != right.WANEgressID || !slices.Equal(left.Servers, right.Servers) || !slices.Equal(left.BootstrapServers, right.BootstrapServers) || len(left.ResolvedHostIPs) != len(right.ResolvedHostIPs) {
		return false
	}
	for host, addresses := range left.ResolvedHostIPs {
		if !slices.Equal(addresses, right.ResolvedHostIPs[host]) {
			return false
		}
	}
	return true
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
	// This artifact owns only its dedicated table. `add table` is idempotent on
	// Debian 12's nftables, then `flush table` clears only Ly Route's prior
	// generation. Host firewall, container, and management rules remain intact.
	builder.WriteString(fmt.Sprintf("add table %s %s\n", family, table))
	builder.WriteString(fmt.Sprintf("flush table %s %s\n\n", family, table))
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

func RenderLinuxPolicyRouting(plan proxy.LinuxPolicyRoutingPlan, natBehavior nat.Behavior, dnsNetworks ...vpp.DNSServiceNetwork) ([]RenderedArtifact, error) {
	if strings.TrimSpace(plan.Mark) == "" || plan.TableID <= 0 || plan.RulePriority <= 0 || strings.TrimSpace(plan.DefaultRoute.Device) == "" {
		return nil, fmt.Errorf("linux policy routing plan requires mark, table, priority, and default route device")
	}
	if strings.TrimSpace(plan.Network.EgressID) != "" {
		artifacts, err := renderProxyServiceNetwork(plan, natBehavior)
		if err != nil {
			return nil, err
		}
		return appendDNSServiceRouting(artifacts, dnsNetworks, plan.Network.EgressID, natBehavior)
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
	return appendDNSServiceRouting([]RenderedArtifact{NewArtifact(LinuxRouting, "/var/lib/ly-route/policy-routing/apply.sh", builder.String(), "restart")}, dnsNetworks, "", natBehavior)
}

// RenderDNSServiceRouting creates the Linux half of VPP-owned DNS egress when
// no proxy policy-routing handoff is present. It has no default route and no
// client traffic rule: only the configured resolver addresses are reachable
// through the service TAPs.
func RenderDNSServiceRouting(natBehavior nat.Behavior, networks []vpp.DNSServiceNetwork) ([]RenderedArtifact, error) {
	if len(networks) == 0 {
		return nil, fmt.Errorf("DNS service routing requires at least one network")
	}
	var builder strings.Builder
	builder.WriteString("#!/bin/sh\nset -eu\n")
	return appendDNSServiceRouting([]RenderedArtifact{NewArtifact(LinuxRouting, "/var/lib/ly-route/policy-routing/apply.sh", builder.String(), "restart")}, networks, "", natBehavior)
}

func appendDNSServiceRouting(artifacts []RenderedArtifact, networks []vpp.DNSServiceNetwork, proxyEgressID string, natBehavior nat.Behavior) ([]RenderedArtifact, error) {
	if len(artifacts) != 1 || artifacts[0].Service != LinuxRouting || artifacts[0].Path != "/var/lib/ly-route/policy-routing/apply.sh" {
		return nil, fmt.Errorf("DNS service routing requires the primary Linux routing artifact")
	}
	section, err := renderDNSServiceRoutingSection(networks, proxyEgressID, natBehavior)
	if err != nil {
		return nil, err
	}
	artifacts[0] = NewArtifact(LinuxRouting, artifacts[0].Path, artifacts[0].Content+section, artifacts[0].ReloadMode)
	return artifacts, nil
}

func renderDNSServiceRoutingSection(networks []vpp.DNSServiceNetwork, proxyEgressID string, natBehavior nat.Behavior) (string, error) {
	ordered := append([]vpp.DNSServiceNetwork(nil), networks...)
	slices.SortFunc(ordered, func(left, right vpp.DNSServiceNetwork) int {
		return strings.Compare(left.UpstreamID, right.UpstreamID)
	})
	seenUpstreams := map[string]bool{}
	seenInterfaces := map[string]bool{}
	for _, network := range ordered {
		if !serviceTokenSafe(network.UpstreamID) || !serviceTokenSafe(network.VPPInterface) || !serviceTokenSafe(network.HostInterface) {
			return "", fmt.Errorf("DNS service network contains an unsafe identifier")
		}
		if seenUpstreams[network.UpstreamID] || seenInterfaces[network.HostInterface] {
			return "", fmt.Errorf("duplicate DNS service network %q", network.UpstreamID)
		}
		seenUpstreams[network.UpstreamID] = true
		seenInterfaces[network.HostInterface] = true
		for label, value := range map[string]string{"VPP address": network.VPPAddress, "host address": network.HostAddress} {
			address, err := netip.ParseAddr(strings.TrimSpace(value))
			if err != nil || !address.Is4() {
				return "", fmt.Errorf("DNS service network %q %s is invalid", network.UpstreamID, label)
			}
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(network.CIDR))
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 30 {
			return "", fmt.Errorf("DNS service network %q CIDR is invalid", network.UpstreamID)
		}
		if len(network.ResolverServers) == 0 {
			return "", fmt.Errorf("DNS service network %q has no resolver servers", network.UpstreamID)
		}
		if network.MTU < 576 || network.MTU > 9000 {
			return "", fmt.Errorf("DNS service network %q has invalid MTU %d", network.UpstreamID, network.MTU)
		}
		if network.SocketMark == 0 {
			return "", fmt.Errorf("DNS service network %q has no socket mark", network.UpstreamID)
		}
		if strings.TrimSpace(network.UnderlayRoute) == "" || !serviceCommandTokensSafe(network.UnderlayRoute) {
			return "", fmt.Errorf("DNS service network %q has an unsafe underlay route", network.UpstreamID)
		}
		for _, resolver := range network.ResolverServers {
			address, err := netip.ParseAddr(strings.TrimSpace(resolver))
			if err != nil || !address.Is4() {
				return "", fmt.Errorf("DNS service network %q resolver %q is invalid", network.UpstreamID, resolver)
			}
		}
	}

	var builder strings.Builder
	builder.WriteString("\n# VPP-owned DNS resolver service routes.\n")
	underlayInterfaces := make([]string, 0, len(ordered))
	for _, network := range ordered {
		if name := serviceUnderlayInterface(network.UnderlayRoute); name != "" {
			underlayInterfaces = append(underlayInterfaces, name)
		}
	}
	readiness, err := renderVPPUnderlayReadiness(underlayInterfaces)
	if err != nil {
		return "", err
	}
	builder.WriteString(readiness)
	builder.WriteString("DNS_STATE_DIR=/var/lib/ly-route/dns-service-routing\n")
	builder.WriteString("DNS_STATE_FILE=$DNS_STATE_DIR/active\n")
	builder.WriteString("mkdir -p $DNS_STATE_DIR\n")
	builder.WriteString("if [ -f $DNS_STATE_FILE ]; then\n")
	builder.WriteString("  while IFS=' ' read -r kind one two three four five; do\n")
	builder.WriteString("    [ -n \"$kind\" ] || continue\n")
	builder.WriteString("    case \"$kind\" in\n")
	builder.WriteString("      route) if [ -n \"${four:-}\" ]; then ip route del \"$one/32\" via \"$three\" dev \"$two\" table \"$four\" 2>/dev/null || true; else ip route del \"$one/32\" via \"$three\" dev \"$two\" 2>/dev/null || true; fi ;;\n")
	builder.WriteString("      policy) ip rule del priority \"$three\" from \"$one/32\" lookup \"$two\" 2>/dev/null || true; ip route flush table \"$two\" 2>/dev/null || true ;;\n")
	builder.WriteString("      mark) ip rule del priority \"$three\" fwmark \"$one\"/0xffffffff lookup \"$two\" 2>/dev/null || true ;;\n")
	builder.WriteString("      vpp-return) \"$VPPCTL\" ip route del \"$one/32\" via \"$one\" \"$two\" 2>/dev/null || true ;;\n")
	builder.WriteString("      *) ip route del \"$kind/32\" via \"$two\" dev \"$one\" 2>/dev/null || true ;;\n")
	builder.WriteString("    esac\n")
	builder.WriteString("  done < $DNS_STATE_FILE\n")
	builder.WriteString("fi\n")
	builder.WriteString("DNS_STATE_TMP=$DNS_STATE_FILE.tmp.$$\n")
	builder.WriteString(": > $DNS_STATE_TMP\n")
	builder.WriteString("cleanup_dns_state_tmp() {\n")
	builder.WriteString("  if [ -n \"${DNS_STATE_TMP:-}\" ]; then rm -f \"$DNS_STATE_TMP\"; fi\n")
	builder.WriteString("}\n")
	builder.WriteString("trap cleanup_dns_state_tmp 0 1 2 15\n")
	builder.WriteString("warm_vpp_resolver() {\n")
	builder.WriteString("  host_address=$1\n  resolver=$2\n")
	builder.WriteString("  command -v ping >/dev/null 2>&1 || return 0\n")
	builder.WriteString("  attempt=1\n")
	builder.WriteString("  while [ \"$attempt\" -le 5 ]; do\n")
	builder.WriteString("    ping -I \"$host_address\" -c 1 -W 1 \"$resolver\" >/dev/null 2>&1 && return 0\n")
	builder.WriteString("    attempt=$((attempt + 1))\n")
	builder.WriteString("  done\n  return 0\n}\n")
	builder.WriteString("ensure_dns_tap() {\n")
	builder.WriteString("  tap_id=$1\n  host_if=$2\n  vpp_if=$3\n")
	builder.WriteString("  if ! ip link show dev \"$host_if\" >/dev/null 2>&1; then\n")
	builder.WriteString("    if \"$VPPCTL\" show tap 2>/dev/null | grep -Fq \"name \\\"$host_if\\\"\"; then\n")
	builder.WriteString("      \"$VPPCTL\" delete tap \"$vpp_if\" 2>/dev/null || true\n")
	builder.WriteString("    fi\n")
	builder.WriteString("  fi\n")
	builder.WriteString("  if ! \"$VPPCTL\" show interface | awk 'NR > 1 {print $1}' | grep -Fxq \"$vpp_if\"; then\n")
	builder.WriteString("    case \"$host_if\" in lydnsh*) ;; *) echo \"refusing to replace unmanaged DNS TAP $host_if\" >&2; return 1 ;; esac\n")
	builder.WriteString("    # VPP restart removes its TAP object but not always the Linux peer.\n")
	builder.WriteString("    # Remove only the generated service peer before recreating the TAP.\n")
	builder.WriteString("    ip link delete dev \"$host_if\" 2>/dev/null || true\n")
	builder.WriteString("    \"$VPPCTL\" create tap id \"$tap_id\" host-if-name \"$host_if\" no-gso\n")
	builder.WriteString("    \"$VPPCTL\" set interface name \"tap${tap_id}\" \"$vpp_if\"\n")
	builder.WriteString("  fi\n")
	builder.WriteString("  \"$VPPCTL\" set interface state \"$vpp_if\" up\n")
	builder.WriteString("}\n\n")
	for _, network := range ordered {
		underlayPath := serviceVPPRoutePath(network.UnderlayRoute)
		linuxTableID := network.TableID
		rulePriority := 10000 + network.TapID
		markPriority := 20000 + network.TapID
		builder.WriteString(fmt.Sprintf("ensure_dns_tap %d %s %s\n", network.TapID, network.HostInterface, network.VPPInterface))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface mtu packet %d %s\n", network.MTU, network.VPPInterface))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface ip address del %s %s 2>/dev/null || true\n", network.VPPInterface, network.CIDR))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip table add %d 2>/dev/null || true\n", network.TableID))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface ip table %s %d\n", network.VPPInterface, network.TableID))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface ip address %s %s 2>/dev/null || true\n", network.VPPInterface, network.CIDR))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip route del table %d 0.0.0.0/0 2>/dev/null || true\n", network.TableID))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip route add table %d 0.0.0.0/0 via %s\n", network.TableID, underlayPath))
		if name := serviceUnderlayInterface(network.UnderlayRoute); name != "" {
			if natBehavior == nat.BehaviorFullCone {
				builder.WriteString("\"$VPPCTL\" nat44 plugin disable >/dev/null 2>&1 || true\n")
				builder.WriteString("\"$VPPCTL\" nat44 ei plugin enable >/dev/null 2>&1 || true\n")
				builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface nat44 ei in %s out %s\n", network.VPPInterface, name))
				builder.WriteString(fmt.Sprintf("\"$VPPCTL\" nat44 ei add interface address %s >/dev/null 2>&1 || true\n", name))
				builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip route add %s/32 via %s %s\n", network.HostAddress, network.HostAddress, network.VPPInterface))
				builder.WriteString(fmt.Sprintf("\"$VPPCTL\" show nat44 ei interfaces | grep -Fq %s\n", network.VPPInterface))
			} else {
				builder.WriteString("\"$VPPCTL\" nat44 ei plugin disable 2>/dev/null || true\n")
				builder.WriteString("\"$VPPCTL\" nat44 plugin enable 2>/dev/null || true\n")
				builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface nat44 in %s out %s output-feature del 2>/dev/null || true\n", network.VPPInterface, name))
				builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface nat44 in %s out %s\n", network.VPPInterface, name))
				builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip route add %s/32 via %s %s\n", network.HostAddress, network.HostAddress, network.VPPInterface))
				builder.WriteString(fmt.Sprintf("\"$VPPCTL\" show nat44 interfaces | grep -Fq %s\n", network.VPPInterface))
			}
			builder.WriteString(fmt.Sprintf("\"$VPPCTL\" show ip fib table %d | grep -F %s >/dev/null\n", network.TableID, name))
		}
		if strings.TrimSpace(proxyEgressID) != "" && strings.TrimSpace(network.WANEgressID) == strings.TrimSpace(proxyEgressID) {
			builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip route del %s/32 via %s %s 2>/dev/null || true\n", network.HostAddress, network.HostAddress, network.VPPInterface))
			builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip route add %s/32 via %s %s\n", network.HostAddress, network.HostAddress, network.VPPInterface))
			builder.WriteString(fmt.Sprintf("printf '%%s %%s %%s\\n' vpp-return %s %s >> $DNS_STATE_TMP\n", network.HostAddress, network.VPPInterface))
		}
		builder.WriteString(fmt.Sprintf("ip link set dev %s mtu %d up\n", network.HostInterface, network.MTU))
		builder.WriteString(fmt.Sprintf("ip address replace %s/30 dev %s\n", network.HostAddress, network.HostInterface))
		builder.WriteString(fmt.Sprintf("sysctl -q -w net.ipv4.conf.%s.rp_filter=0\n", network.HostInterface))
		builder.WriteString(fmt.Sprintf("ip rule del priority %d from %s/32 lookup %d 2>/dev/null || true\n", rulePriority, network.HostAddress, linuxTableID))
		builder.WriteString(fmt.Sprintf("ip route flush table %d 2>/dev/null || true\n", linuxTableID))
		builder.WriteString(fmt.Sprintf("ip route replace default via %s dev %s table %d\n", network.VPPAddress, network.HostInterface, linuxTableID))
		builder.WriteString(fmt.Sprintf("ip rule add from %s/32 lookup %d priority %d\n", network.HostAddress, linuxTableID, rulePriority))
		builder.WriteString(fmt.Sprintf("printf '%%s %%s %%s %%s\\n' policy %s %d %d >> $DNS_STATE_TMP\n", network.HostAddress, linuxTableID, rulePriority))
		builder.WriteString(fmt.Sprintf("ip rule del priority %d fwmark 0x%x/0xffffffff lookup %d 2>/dev/null || true\n", markPriority, network.SocketMark, linuxTableID))
		builder.WriteString(fmt.Sprintf("ip rule add fwmark 0x%x/0xffffffff lookup %d priority %d\n", network.SocketMark, linuxTableID, markPriority))
		builder.WriteString(fmt.Sprintf("printf '%%s %%s %%s %%s\\n' mark 0x%x %d %d >> $DNS_STATE_TMP\n", network.SocketMark, linuxTableID, markPriority))
		for _, resolver := range network.ResolverServers {
			// DNS resolver traffic originates from the service TAP address. Keep
			// its route in that TAP's source-policy table: a resolver may be
			// intentionally reused by several policies or WANs, where a main-table
			// /32 route would be overwritten by the last rendered service network.
			builder.WriteString(fmt.Sprintf("ip route replace %s/32 via %s dev %s table %d\n", resolver, network.VPPAddress, network.HostInterface, linuxTableID))
			builder.WriteString(fmt.Sprintf("printf '%%s %%s %%s %%s\\n' route %s %s %s %d >> $DNS_STATE_TMP\n", resolver, network.HostInterface, network.VPPAddress, linuxTableID))
		}
		builder.WriteString(fmt.Sprintf("warm_vpp_resolver %s %s\n", network.HostAddress, network.ResolverServers[0]))
	}
	builder.WriteString("mv $DNS_STATE_TMP $DNS_STATE_FILE\n")
	builder.WriteString("DNS_STATE_TMP=\n")
	builder.WriteString("ip route flush cache\n")
	return builder.String(), nil
}

func renderProxyServiceNetwork(plan proxy.LinuxPolicyRoutingPlan, natBehavior nat.Behavior) ([]RenderedArtifact, error) {
	network := plan.Network
	for label, value := range map[string]string{
		"egress id":              network.EgressID,
		"ingress VPP interface":  network.IngressVPPInterface,
		"ingress host interface": network.IngressHostInterface,
		"egress VPP interface":   network.EgressVPPInterface,
		"egress host interface":  network.EgressHostInterface,
	} {
		if !serviceTokenSafe(value) {
			return nil, fmt.Errorf("proxy service network %s %q is unsafe", label, value)
		}
	}
	for label, value := range map[string]string{
		"ingress VPP address":  network.IngressVPPAddress,
		"ingress host address": network.IngressHostAddress,
		"egress VPP address":   network.EgressVPPAddress,
		"egress host address":  network.EgressHostAddress,
	} {
		address, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || !address.Is4() {
			return nil, fmt.Errorf("proxy service network %s %q is invalid", label, value)
		}
	}
	for label, value := range map[string]string{"ingress CIDR": network.IngressCIDR, "egress CIDR": network.EgressCIDR} {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 30 {
			return nil, fmt.Errorf("proxy service network %s %q is invalid", label, value)
		}
	}
	if network.IngressTapID <= 0 || network.EgressTapID <= 0 || network.IngressTableID <= 0 || network.OutboundTableID <= 0 || network.IngressRulePriority <= 0 || network.OutboundRulePriority <= 0 || network.IngressMark == 0 || network.OutboundMark == 0 {
		return nil, fmt.Errorf("proxy service network numeric identities are incomplete")
	}
	if network.MTU < 576 || network.MTU > 9000 {
		return nil, fmt.Errorf("proxy service network MTU %d is outside 576-9000", network.MTU)
	}
	underlay := strings.TrimSpace(plan.UnderlayRoute)
	if underlay == "" {
		underlay = strings.TrimSpace(network.UnderlayRoute)
	}
	if underlay == "" || !serviceCommandTokensSafe(underlay) {
		return nil, fmt.Errorf("proxy service network underlay route %q is unsafe", underlay)
	}
	underlayPath := serviceVPPRoutePath(underlay)
	lanRoutes := make([]string, 0, len(plan.LANRoutes))
	for _, route := range plan.LANRoutes {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(route))
		if err != nil || !prefix.Addr().Is4() {
			return nil, fmt.Errorf("proxy service network LAN route %q is invalid", route)
		}
		lanRoutes = append(lanRoutes, prefix.Masked().String())
	}

	var builder strings.Builder
	builder.WriteString("#!/bin/sh\nset -eu\n\n")
	builder.WriteString("VPPCTL=${LY_ROUTE_VPPCTL:-vppctl}\n")
	readiness, err := renderVPPUnderlayReadiness([]string{serviceUnderlayInterface(underlay)})
	if err != nil {
		return nil, err
	}
	builder.WriteString(readiness)
	builder.WriteString("ensure_tap() {\n")
	builder.WriteString("  tap_id=$1\n  host_if=$2\n  vpp_if=$3\n")
	// A VPP TAP can survive while its Linux peer disappeared (for example
	// after a failed service restart).  Treat that as stale state and recreate
	// the managed TAP instead of continuing with an interface index that can
	// resolve to the wrong proxy side.
	builder.WriteString("  if ! ip link show dev \"$host_if\" >/dev/null 2>&1; then\n")
	builder.WriteString("    if \"$VPPCTL\" show tap 2>/dev/null | grep -Fq \"name \\\"$host_if\\\"\"; then\n")
	builder.WriteString("      \"$VPPCTL\" delete tap \"$vpp_if\" 2>/dev/null || true\n")
	builder.WriteString("    fi\n")
	builder.WriteString("  fi\n")
	builder.WriteString("  if ! \"$VPPCTL\" show interface | awk 'NR > 1 {print $1}' | grep -Fxq \"$vpp_if\"; then\n")
	builder.WriteString("    case \"$host_if\" in lypxhin*|lypxhout*) ;; *) echo \"refusing to replace unmanaged proxy TAP $host_if\" >&2; return 1 ;; esac\n")
	builder.WriteString("    # VPP restart removes its TAP object but not always the Linux peer.\n")
	builder.WriteString("    # Remove only the generated service peer before recreating the TAP.\n")
	builder.WriteString("    ip link delete dev \"$host_if\" 2>/dev/null || true\n")
	builder.WriteString("    \"$VPPCTL\" create tap id \"$tap_id\" host-if-name \"$host_if\" no-gso\n")
	builder.WriteString("    \"$VPPCTL\" set interface name \"tap${tap_id}\" \"$vpp_if\"\n")
	builder.WriteString("  fi\n")
	builder.WriteString("  \"$VPPCTL\" set interface state \"$vpp_if\" up\n")
	builder.WriteString("}\n\n")
	builder.WriteString(fmt.Sprintf("ensure_tap %d %s %s\n", network.IngressTapID, network.IngressHostInterface, network.IngressVPPInterface))
	builder.WriteString(fmt.Sprintf("ensure_tap %d %s %s\n", network.EgressTapID, network.EgressHostInterface, network.EgressVPPInterface))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface mtu packet %d %s\n", network.MTU, network.IngressVPPInterface))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface mtu packet %d %s\n", network.MTU, network.EgressVPPInterface))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface ip address del %s %s 2>/dev/null || true\n", network.IngressVPPInterface, network.IngressCIDR))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface ip address %s %s 2>/dev/null || true\n", network.IngressVPPInterface, network.IngressCIDR))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip table add %d 2>/dev/null || true\n", network.OutboundTableID))
	// VPP's delete form places `del` before the interface. Keeping address
	// removal idempotent is essential while this script is retried during a
	// runtime transaction.
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface ip address del %s %s 2>/dev/null || true\n", network.EgressVPPInterface, network.EgressCIDR))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface ip table %s %d\n", network.EgressVPPInterface, network.OutboundTableID))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface ip address %s %s 2>/dev/null || true\n", network.EgressVPPInterface, network.EgressCIDR))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip route del table %d 0.0.0.0/0 2>/dev/null || true\n", network.OutboundTableID))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" ip route add table %d 0.0.0.0/0 via %s\n", network.OutboundTableID, underlayPath))
	if name := serviceUnderlayInterface(underlay); name != "" {
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" show ip fib table %d | grep -F %s >/dev/null\n", network.OutboundTableID, name))
	}
	// Proxy traffic enters VPP again from the Linux/Xray egress TAP. It must be
	// the NAT inside interface for the selected WAN; otherwise the proxy node
	// sees the private 198.18/30 source and cannot return the connection.
	if natBehavior == nat.BehaviorFullCone {
		builder.WriteString("\"$VPPCTL\" nat44 plugin disable >/dev/null 2>&1 || true\n")
		builder.WriteString("\"$VPPCTL\" nat44 ei plugin enable >/dev/null 2>&1 || true\n")
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface nat44 ei in %s out %s\n", network.EgressVPPInterface, serviceUnderlayInterface(underlay)))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" nat44 ei add interface address %s >/dev/null 2>&1 || true\n\n", serviceUnderlayInterface(underlay)))
	} else {
		builder.WriteString("\"$VPPCTL\" nat44 plugin enable 2>/dev/null || true\n")
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface nat44 in %s out %s output-feature del 2>/dev/null || true\n", network.EgressVPPInterface, serviceUnderlayInterface(underlay)))
		builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set interface nat44 in %s out %s\n\n", network.EgressVPPInterface, serviceUnderlayInterface(underlay)))
	}

	builder.WriteString(fmt.Sprintf("ip link set dev %s mtu %d up\n", network.IngressHostInterface, network.MTU))
	builder.WriteString(fmt.Sprintf("ip link set dev %s mtu %d up\n", network.EgressHostInterface, network.MTU))
	// TAP peers use deterministic point-to-point L3 addresses. Install both
	// neighbor entries explicitly so proxy handoff does not depend on ARP
	// learning across a VPP/Linux TAP boundary.
	builder.WriteString(fmt.Sprintf("IN_TAP_MAC=\"$($VPPCTL show hardware %s | awk '/Ethernet address/{print $3; exit}')\"\n", network.IngressVPPInterface))
	builder.WriteString(fmt.Sprintf("OUT_TAP_MAC=\"$($VPPCTL show hardware %s | awk '/Ethernet address/{print $3; exit}')\"\n", network.EgressVPPInterface))
	builder.WriteString(fmt.Sprintf("[ -n \"$IN_TAP_MAC\" ] && ip neigh replace %s lladdr \"$IN_TAP_MAC\" nud permanent dev %s\n", network.IngressVPPAddress, network.IngressHostInterface))
	builder.WriteString(fmt.Sprintf("[ -n \"$OUT_TAP_MAC\" ] && ip neigh replace %s lladdr \"$OUT_TAP_MAC\" nud permanent dev %s\n", network.EgressVPPAddress, network.EgressHostInterface))
	builder.WriteString(fmt.Sprintf("IN_HOST_MAC=\"$(cat /sys/class/net/%s/address)\"\n", network.IngressHostInterface))
	builder.WriteString(fmt.Sprintf("OUT_HOST_MAC=\"$(cat /sys/class/net/%s/address)\"\n", network.EgressHostInterface))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set ip neighbor %s %s \"$IN_HOST_MAC\" static\n", network.IngressVPPInterface, network.IngressHostAddress))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" set ip neighbor %s %s \"$OUT_HOST_MAC\" static\n", network.EgressVPPInterface, network.EgressHostAddress))
	builder.WriteString(fmt.Sprintf("ip address replace %s/30 dev %s\n", network.IngressHostAddress, network.IngressHostInterface))
	builder.WriteString(fmt.Sprintf("ip address replace %s/30 dev %s\n", network.EgressHostAddress, network.EgressHostInterface))
	builder.WriteString(fmt.Sprintf("sysctl -q -w net.ipv4.conf.%s.rp_filter=0\n", network.IngressHostInterface))
	builder.WriteString(fmt.Sprintf("sysctl -q -w net.ipv4.conf.%s.rp_filter=0\n", network.EgressHostInterface))
	// TPROXY listeners must be allowed to bind to original client addresses.
	builder.WriteString("sysctl -q -w net.ipv4.ip_nonlocal_bind=1\n")
	// DNS service sockets use host-owned source addresses, cross VPP, and
	// re-enter Linux on this interface for transparent proxying.
	builder.WriteString(fmt.Sprintf("sysctl -q -w net.ipv4.conf.%s.accept_local=1\n", network.IngressHostInterface))
	builder.WriteString(fmt.Sprintf("while ip rule del fwmark 0x%x/0xffffffff table %d priority %d 2>/dev/null; do :; done\n", network.IngressMark, network.IngressTableID, network.IngressRulePriority))
	builder.WriteString(fmt.Sprintf("ip rule add fwmark 0x%x/0xffffffff table %d priority %d\n", network.IngressMark, network.IngressTableID, network.IngressRulePriority))
	builder.WriteString(fmt.Sprintf("ip route replace local 0.0.0.0/0 dev lo table %d\n", network.IngressTableID))
	builder.WriteString(fmt.Sprintf("while ip rule del fwmark 0x%x/0xffffffff table %d priority %d 2>/dev/null; do :; done\n", network.OutboundMark, network.OutboundTableID, network.OutboundRulePriority))
	builder.WriteString(fmt.Sprintf("ip rule add fwmark 0x%x/0xffffffff table %d priority %d\n", network.OutboundMark, network.OutboundTableID, network.OutboundRulePriority))
	builder.WriteString(fmt.Sprintf("ip route replace default via %s dev %s table %d\n", network.EgressVPPAddress, network.EgressHostInterface, network.OutboundTableID))
	for _, route := range lanRoutes {
		builder.WriteString(fmt.Sprintf("ip route replace %s via %s dev %s\n", route, network.IngressVPPAddress, network.IngressHostInterface))
	}
	builder.WriteString("ip route flush cache\n\n")
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" show interface address %s | grep -F %s >/dev/null\n", network.IngressVPPInterface, network.IngressVPPAddress))
	builder.WriteString(fmt.Sprintf("\"$VPPCTL\" show interface address %s | grep -F %s >/dev/null\n", network.EgressVPPInterface, network.EgressVPPAddress))
	builder.WriteString(fmt.Sprintf("ip route show table %d | grep -F %s >/dev/null\n", network.OutboundTableID, network.EgressVPPAddress))

	return []RenderedArtifact{NewArtifact(LinuxRouting, "/var/lib/ly-route/policy-routing/apply.sh", builder.String(), "restart")}, nil
}

func serviceCommandTokensSafe(value string) bool {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return false
	}
	for _, field := range fields {
		if !serviceTokenSafe(field) {
			return false
		}
	}
	return true
}

func serviceUnderlayInterface(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	switch len(fields) {
	case 1:
		return fields[0]
	case 2:
		if fields[0] == "ip4-lookup-in-table" {
			return ""
		}
		return fields[1]
	default:
		return ""
	}
}

func serviceVPPRoutePath(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 1 && strings.HasPrefix(strings.ToLower(fields[0]), "pppoe_session") {
		// PPPoE is a point-to-point VPP interface. VPP resolves the peer
		// through the session; an artificial 0.0.0.0 next hop is invalid.
		return fields[0]
	}
	return strings.TrimSpace(value)
}

func renderVPPUnderlayReadiness(interfaces []string) (string, error) {
	unique := map[string]struct{}{}
	for _, name := range interfaces {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !serviceTokenSafe(name) {
			return "", fmt.Errorf("VPP underlay interface %q is unsafe", name)
		}
		unique[name] = struct{}{}
	}
	if len(unique) == 0 {
		return "", nil
	}
	ordered := make([]string, 0, len(unique))
	for name := range unique {
		ordered = append(ordered, name)
	}
	slices.Sort(ordered)
	var builder strings.Builder
	builder.WriteString("VPPCTL=${LY_ROUTE_VPPCTL:-vppctl}\n")
	builder.WriteString("wait_vpp_underlay() {\n")
	builder.WriteString("  underlay_if=$1\n")
	builder.WriteString("  attempts=${LY_ROUTE_VPP_UNDERLAY_READY_ATTEMPTS:-10}\n")
	builder.WriteString("  interval=${LY_ROUTE_VPP_UNDERLAY_READY_INTERVAL:-1}\n")
	builder.WriteString("  case \"$attempts:$interval\" in *[!0-9:]*|0:*|*:0) echo \"VPP underlay readiness settings must be positive integers\" >&2; return 1 ;; esac\n")
	builder.WriteString("  attempt=1\n")
	builder.WriteString("  while ! \"$VPPCTL\" show interface address \"$underlay_if\" 2>/dev/null | grep -Eq 'L3 [0-9]'; do\n")
	builder.WriteString("    [ \"$attempt\" -lt \"$attempts\" ] || { echo \"VPP underlay $underlay_if did not become ready\" >&2; return 1; }\n")
	builder.WriteString("    sleep \"$interval\"\n")
	builder.WriteString("    attempt=$((attempt + 1))\n")
	builder.WriteString("  done\n")
	builder.WriteString("}\n")
	// A missing PPPoE/proxy underlay is normal during boot or link
	// renegotiation. Return failure so systemd retries after the native
	// session has obtained an address.
	for _, name := range ordered {
		builder.WriteString(fmt.Sprintf("if ! wait_vpp_underlay %s; then\n", name))
		builder.WriteString(fmt.Sprintf("  echo \"VPP underlay %s is unavailable; retrying dependent policy routing\" >&2\n", name))
		builder.WriteString("  exit 1\n")
		builder.WriteString("fi\n")
	}
	return builder.String(), nil
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
		seenIDs[peer.ID] = true
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
			"id":                    peer.ID,
			"control_interface":     controlInterface,
			"wan_interface":         wanInterface,
			"username":              peer.Username,
			"password":              peer.Password,
			"mru":                   mru,
			"tap_id":                tapID,
			"vppctl":                "vppctl",
			"status_file":           "/run/ly-route/pppoe/" + peer.ID + ".json",
			"default_route":         true,
			"nat":                   true,
			"nat_inside_interfaces": peer.NATInsideInterfaces,
			"nat_behavior":          peer.NATBehavior,
			"ipv6_prefix_group":     peer.IPv6PrefixGroup,
			"ipv6_lan_interfaces":   peer.IPv6LANInterfaces,
		})
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, NewArtifact(PPPoE, "/etc/ly-route/pppoe/ly-route-"+peer.ID+".json", content, "restart"))
	}
	return artifacts, nil
}

func PPPoEInterfaceName(id string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return "pppoe_session_ly" + hex.EncodeToString(digest[:4])
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
	// VPP's default FIB is table 0.  A connected PPPoE peer using the
	// default table is fully routable and must not be reported as pending.
	status.RouteReady = state == PPPoEConnected && status.AssignedIPv4 != "" && tableID >= 0
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
		if service == Kea {
			// Debian runs Kea as the unprivileged _kea user. DHCP configuration
			// carries no credentials and must remain readable after atomic replace.
			mode = 0644
		}
		if strings.HasSuffix(path, ".sh") {
			mode = 0750
		}
		if service == PPPoE && strings.HasPrefix(artifact.Path, "/etc/ly-route/pppoe/") {
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
	removeFromDir := func(path, prefix, suffix string) error {
		dir, err := controller.resolvePath(path)
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
			if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
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
	if err := removeFromDir("/etc/smartdns/conf.d", "ly-route-", ".conf"); err != nil {
		return err
	}
	return removeFromDir("/etc/smartdns/domain-sets", "lyroute-", ".list")
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
	case PPPoE:
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
	// PPPoE establishes the VPP WAN session and its notifier refreshes policy
	// routing. Start it before routing and dependent daemons so that refresh
	// cannot stop an already-validated SmartDNS or proxy service mid-apply.
	// Install transparent capture only after Xray accepts full-proxy ingress.
	return []ServiceName{VPP, PPPoE, LinuxRouting, SmartDNS, Kea, Xray, Nftables, IPv6RA}
}
