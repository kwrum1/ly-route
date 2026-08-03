package trafficpolicy

import (
	"fmt"
	"math/big"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	RoutePolicies []RoutePolicy `json:"route_policies,omitempty"`
	SecurityACLs  []SecurityACL `json:"security_acls,omitempty"`
	WANGroups     []WANGroup    `json:"wan_groups,omitempty"`
}

type DomainIPSetEntry struct {
	Domain    string   `json:"domain"`
	IPs       []string `json:"ips"`
	ExpiresAt string   `json:"expires_at,omitempty"`
}

type Match struct {
	Sources      []string `json:"sources,omitempty"`
	Destinations []string `json:"destinations,omitempty"`
	Protocols    []string `json:"protocols,omitempty"`
	SourcePorts  []string `json:"source_ports,omitempty"`
	DestPorts    []string `json:"dest_ports,omitempty"`
	Direction    string   `json:"direction,omitempty"`
}

type RoutePolicy struct {
	ID       string   `json:"id"`
	Priority int      `json:"priority"`
	Action   string   `json:"action"`
	Match    Match    `json:"match"`
	Egress   string   `json:"egress,omitempty"`
	NextHop  string   `json:"next_hop,omitempty"`
	Path     *WANPath `json:"path,omitempty"`
}

type Flow struct {
	SourceIP   string `json:"source_ip"`
	DestIP     string `json:"dest_ip"`
	Protocol   string `json:"protocol,omitempty"`
	SourcePort string `json:"source_port,omitempty"`
	DestPort   string `json:"dest_port,omitempty"`
}

type DNSOverrideIntent struct {
	Source     string `json:"source"`
	ResolvedIP string `json:"resolved_ip"`
	Egress     string `json:"egress"`
	ExpiresAt  string `json:"expires_at"`
}

type RouteDecision struct {
	Matched bool   `json:"matched"`
	RuleID  string `json:"rule_id,omitempty"`
	Action  string `json:"action,omitempty"`
	Egress  string `json:"egress,omitempty"`
	NextHop string `json:"next_hop,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type SecurityACL struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
	Action   string `json:"action"`
	Match    Match  `json:"match"`
}

type WANGroup struct {
	ID      string             `json:"id"`
	Mode    WANGroupMode       `json:"mode"`
	Members []string           `json:"members"`
	Weights map[string]int     `json:"weights,omitempty"`
	Paths   map[string]WANPath `json:"paths,omitempty"`
}

type WANPath struct {
	VPPInterface string `json:"vpp_interface"`
	NextHop      string `json:"next_hop,omitempty"`
}

type WANGroupMode string

const (
	WANGroupPrimaryBackup WANGroupMode = "primary_backup"
	WANGroupWeighted      WANGroupMode = "weighted"
	WANGroupFiveTuple     WANGroupMode = "five_tuple"
)

type objectGroup struct {
	Kind       string
	Entries    []string
	References []string
}

func CompileConfig(routeItems, securityItems, objectGroupItems []map[string]any) (Config, error) {
	return CompileConfigWithDomainIPSet(routeItems, securityItems, objectGroupItems, nil)
}

func CompileConfigWithDomainIPSet(routeItems, securityItems, objectGroupItems []map[string]any, domainIPSet []DomainIPSetEntry) (Config, error) {
	return CompileConfigWithDomainIPSetAt(routeItems, securityItems, objectGroupItems, domainIPSet, time.Now().UTC())
}

func CompileConfigWithDomainIPSetAt(routeItems, securityItems, objectGroupItems []map[string]any, domainIPSet []DomainIPSetEntry, now time.Time) (Config, error) {
	groups := compileObjectGroups(objectGroupItems)
	domains := compileDomainIPSet(domainIPSet, groups, now)
	routes := make([]RoutePolicy, 0, len(routeItems))
	for _, item := range routeItems {
		if !enabled(item) {
			continue
		}
		policy, err := compileRoutePolicy(item, groups, domains)
		if err != nil {
			return Config{}, err
		}
		routes = append(routes, policy)
	}
	securityACLs := make([]SecurityACL, 0, len(securityItems))
	for _, item := range securityItems {
		if !enabled(item) {
			continue
		}
		acl, err := compileSecurityACL(item, groups)
		if err != nil {
			return Config{}, err
		}
		securityACLs = append(securityACLs, acl)
	}
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Priority < routes[j].Priority })
	sort.SliceStable(securityACLs, func(i, j int) bool { return securityACLs[i].Priority < securityACLs[j].Priority })
	return Config{RoutePolicies: routes, SecurityACLs: securityACLs}, nil
}

func CompileWANGroups(items []map[string]any) ([]WANGroup, error) {
	return CompileWANGroupsWithBindings(items, nil)
}

// ExpandAddressSelectors resolves IP object-group references using the same
// canonical address semantics as route and security policy compilation.
func ExpandAddressSelectors(selectors []string, objectItems []map[string]any) ([]string, error) {
	groups := compileObjectGroups(objectItems)
	return expandAddressList(strings.Join(selectors, ","), groups)
}

func CompileWANGroupsWithBindings(items []map[string]any, bindings map[string]WANPath) ([]WANGroup, error) {
	groups := make([]WANGroup, 0, len(items))
	for _, item := range items {
		if !enabled(item) {
			continue
		}
		id := stringValue(item, "id", "name")
		if id == "" {
			return nil, fmt.Errorf("wan_group requires id")
		}
		members := uniqueStrings(append(wanMemberIDs(item["wan_members"]), wanMemberIDs(item["members"])...))
		if len(members) < 2 {
			return nil, fmt.Errorf("wan_group %q requires at least two members", id)
		}
		for _, member := range members {
			if err := requireCommandToken(member, "wan group member"); err != nil {
				return nil, err
			}
		}
		mode, err := wanGroupMode(item)
		if err != nil {
			return nil, fmt.Errorf("wan_group %q: %w", id, err)
		}
		weights, err := wanGroupWeights(item, members)
		if err != nil {
			return nil, fmt.Errorf("wan_group %q: %w", id, err)
		}
		switch mode {
		case WANGroupPrimaryBackup:
			for member := range weights {
				weights[member] = 1
			}
		case WANGroupFiveTuple:
			for member, weight := range weights {
				if weight != 1 {
					return nil, fmt.Errorf("five_tuple member %q cannot set weight %d", member, weight)
				}
			}
		}
		paths := make(map[string]WANPath, len(members))
		for _, member := range members {
			path := WANPath{VPPInterface: member}
			if bindings != nil {
				var exists bool
				path, exists = bindings[member]
				if !exists {
					return nil, fmt.Errorf("wan_group %q member %q has no runtime WAN binding", id, member)
				}
			}
			path.VPPInterface = strings.TrimSpace(path.VPPInterface)
			path.NextHop = strings.TrimSpace(path.NextHop)
			if path.VPPInterface == "" {
				return nil, fmt.Errorf("wan_group %q member %q has no VPP interface", id, member)
			}
			if err := requireCommandToken(path.VPPInterface, "WAN group VPP interface"); err != nil {
				return nil, err
			}
			if path.NextHop != "" {
				if err := requireCommandToken(path.NextHop, "WAN group next hop"); err != nil {
					return nil, err
				}
			}
			paths[member] = path
		}
		groups = append(groups, WANGroup{ID: id, Mode: mode, Members: members, Weights: weights, Paths: paths})
	}
	return groups, nil
}

func BindRoutePolicyPaths(policies []RoutePolicy, bindings map[string]WANPath, wanGroups []WANGroup) error {
	groupIDs := make(map[string]struct{}, len(wanGroups))
	for _, group := range wanGroups {
		groupIDs[group.ID] = struct{}{}
	}
	for index := range policies {
		policy := &policies[index]
		if policy.Action == "deny" || policy.Egress == "" {
			continue
		}
		if _, grouped := groupIDs[policy.Egress]; grouped {
			continue
		}
		path, exists := bindings[policy.Egress]
		if !exists {
			return fmt.Errorf("route_policy %q egress %q has no runtime WAN binding", policy.ID, policy.Egress)
		}
		path.VPPInterface = strings.TrimSpace(path.VPPInterface)
		path.NextHop = strings.TrimSpace(path.NextHop)
		if policy.NextHop != "" {
			path.NextHop = policy.NextHop
		}
		if path.VPPInterface == "" {
			return fmt.Errorf("route_policy %q egress %q has no VPP interface", policy.ID, policy.Egress)
		}
		if err := requireCommandToken(path.VPPInterface, "route policy VPP interface"); err != nil {
			return err
		}
		if path.NextHop != "" {
			if err := requireCommandToken(path.NextHop, "route policy next hop"); err != nil {
				return err
			}
		}
		policy.Path = &path
	}
	return nil
}

func wanGroupMode(item map[string]any) (WANGroupMode, error) {
	raw := strings.ToLower(strings.TrimSpace(stringValue(item, "mode")))
	if loadBalance, ok := item["load_balance"].(map[string]any); ok {
		raw = strings.ToLower(strings.TrimSpace(stringValue(loadBalance, "mode")))
	}
	switch strings.ReplaceAll(raw, "-", "_") {
	case "", "weighted", "weighted_load", "per_connection_weighted":
		return WANGroupWeighted, nil
	case "primary_backup", "active_backup", "failover", "main_backup":
		return WANGroupPrimaryBackup, nil
	case "five_tuple", "five_tuple_load", "per_connection", "ecmp":
		return WANGroupFiveTuple, nil
	default:
		return "", fmt.Errorf("unsupported mode %q", raw)
	}
}

func wanGroupWeights(item map[string]any, members []string) (map[string]int, error) {
	weights := map[string]int{}
	memberSet := make(map[string]struct{}, len(members))
	for _, member := range members {
		weights[member] = 1
		memberSet[member] = struct{}{}
	}
	for _, value := range []any{item["weights"], item["member_weights"]} {
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		for id, raw := range object {
			if _, exists := memberSet[id]; !exists {
				return nil, fmt.Errorf("weight references non-member %q", id)
			}
			weight := numericWeight(raw)
			if weight < 1 {
				weight = 1
			}
			weights[id] = weight
		}
	}
	for _, value := range anySlice(item["member_weights"]) {
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id := stringValue(object, "id", "wan_id", "member_id", "egress_id")
		if id == "" {
			continue
		}
		if _, exists := memberSet[id]; !exists {
			return nil, fmt.Errorf("weight references non-member %q", id)
		}
		weight := intValue(object, 1, "weight")
		if weight < 1 {
			weight = 1
		}
		weights[id] = weight
	}
	return weights, nil
}

func wanMemberIDs(value any) []string {
	members := []string{}
	for _, item := range anySlice(value) {
		switch typed := item.(type) {
		case string:
			if member := strings.TrimSpace(typed); member != "" {
				members = append(members, member)
			}
		case map[string]any:
			if member := stringValue(typed, "id", "wan_id", "member_id", "egress_id"); member != "" {
				members = append(members, member)
			}
		}
	}
	return members
}

func numericWeight(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed
		}
	}
	return 1
}

func compileRoutePolicy(item map[string]any, groups map[string]objectGroup, domains map[string][]string) (RoutePolicy, error) {
	id := stringValue(item, "id", "name")
	if id == "" {
		return RoutePolicy{}, fmt.Errorf("route_policy requires id")
	}
	match, err := compileMatch(mapValue(item, "match"), groups)
	if err != nil {
		return RoutePolicy{}, fmt.Errorf("route_policy %q: %w", id, err)
	}
	if domainDestinations := expandDomainMatches(item, mapValue(item, "match"), domains); len(domainDestinations) > 0 {
		match.Destinations = domainDestinations
	}
	policy := RoutePolicy{
		ID:       id,
		Priority: intValue(item, 1000, "priority", "order"),
		Action:   normalizedAction(stringValue(item, "action", "kind")),
		Match:    match,
		Egress:   stringValue(item, "egress", "target_line", "wan_group", "wan_link"),
		NextHop:  stringValue(item, "next_hop", "gateway"),
	}
	if policy.Action == "" {
		policy.Action = "route"
	}
	if policy.Action != "route" && policy.Action != "nat" && policy.Action != "deny" {
		return RoutePolicy{}, fmt.Errorf("unsupported action %q", policy.Action)
	}
	if policy.Action != "deny" && policy.Egress == "" && policy.NextHop == "" {
		return RoutePolicy{}, fmt.Errorf("route action requires egress or next_hop")
	}
	if policy.Egress != "" {
		if err := requireCommandToken(policy.Egress, "route policy egress"); err != nil {
			return RoutePolicy{}, err
		}
	}
	if policy.NextHop != "" {
		if err := requireCommandToken(policy.NextHop, "route policy next_hop"); err != nil {
			return RoutePolicy{}, err
		}
	}
	return policy, nil
}

func compileSecurityACL(item map[string]any, groups map[string]objectGroup) (SecurityACL, error) {
	id := stringValue(item, "id", "name")
	if id == "" {
		return SecurityACL{}, fmt.Errorf("security_acl requires id")
	}
	match, err := compileMatch(mapValue(item, "match"), groups)
	if err != nil {
		return SecurityACL{}, fmt.Errorf("security_acl %q: %w", id, err)
	}
	action := normalizedAction(stringValue(item, "action", "kind"))
	if action == "" {
		action = "deny"
	}
	if action != "permit" && action != "deny" {
		return SecurityACL{}, fmt.Errorf("security_acl %q unsupported action %q", id, action)
	}
	return SecurityACL{ID: id, Priority: intValue(item, 1000, "priority", "order"), Action: action, Match: match}, nil
}

func compileMatch(match map[string]any, groups map[string]objectGroup) (Match, error) {
	if err := rejectUnsupportedMatchFields(match); err != nil {
		return Match{}, err
	}
	sources, err := expandAddressList(selectorValue(match, "src_ip", "source", "source_ip", "sources"), groups)
	if err != nil {
		return Match{}, err
	}
	destinations, err := expandAddressList(selectorValue(match, "dst_ip", "destination", "destination_ip", "destinations"), groups)
	if err != nil {
		return Match{}, err
	}
	protocols := splitTokens(selectorValue(match, "protocol", "protocols"))
	sourcePorts, err := expandPortList(selectorValue(match, "src_port", "source_port", "source_ports"), groups, &protocols)
	if err != nil {
		return Match{}, err
	}
	destPorts, err := expandPortList(selectorValue(match, "dst_port", "destination_port", "dest_ports", "destination_ports"), groups, &protocols)
	if err != nil {
		return Match{}, err
	}
	// VPP cannot retain L4 port ranges on protocol 0 (any); it normalizes
	// those ranges back to 0-65535. Router policy UIs conventionally interpret
	// a port condition without an explicit protocol as both TCP and UDP.
	if hasRestrictivePortMatch(sourcePorts) || hasRestrictivePortMatch(destPorts) {
		if len(protocols) == 0 || len(protocols) == 1 && protocols[0] == "any" {
			protocols = []string{"tcp", "udp"}
		}
	}
	if len(protocols) == 0 {
		protocols = []string{"any"}
	}
	return Match{Sources: sources, Destinations: destinations, Protocols: protocols, SourcePorts: sourcePorts, DestPorts: destPorts, Direction: normalizedDirection(stringValue(match, "direction"))}, nil
}

func hasRestrictivePortMatch(ports []string) bool {
	for _, port := range ports {
		port = strings.TrimSpace(strings.ToLower(port))
		if port != "" && port != "any" && port != "0-65535" {
			return true
		}
	}
	return false
}

func compileObjectGroups(items []map[string]any) map[string]objectGroup {
	groups := map[string]objectGroup{}
	for _, item := range items {
		id := stringValue(item, "id", "name")
		if id == "" {
			continue
		}
		entries := append(entryValues(item["entries"]), entryValues(item["members"])...)
		groups[id] = objectGroup{Kind: stringValue(item, "group_type", "type", "kind"), Entries: entries, References: stringSlice(item["references"])}
	}
	return groups
}

func compileDomainIPSet(entries []DomainIPSetEntry, groups map[string]objectGroup, now time.Time) map[string][]string {
	resolved := map[string][]string{}
	for _, entry := range entries {
		if domainEntryExpired(entry.ExpiresAt, now) {
			continue
		}
		domain := strings.TrimSpace(entry.Domain)
		if domain == "" {
			continue
		}
		for _, ip := range entry.IPs {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				resolved[domain] = appendUnique(resolved[domain], ip)
			}
		}
	}
	for id, group := range groups {
		if group.Kind != "domain" {
			continue
		}
		for _, domain := range group.Entries {
			resolved[id] = append(resolved[id], resolved[domain]...)
		}
		resolved[id] = uniqueStrings(resolved[id])
	}
	return resolved
}

func domainEntryExpired(expiresAt string, now time.Time) bool {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" || now.IsZero() {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return !deadline.After(now)
}

func DecideRoute(policies []RoutePolicy, flow Flow, overrides []DNSOverrideIntent, now time.Time) RouteDecision {
	if override, ok := activeDNSOverride(flow.SourceIP, flow.DestIP, overrides, now); ok {
		return RouteDecision{Matched: true, Action: "route", Egress: override.Egress, Reason: "dns_intent_override"}
	}
	for _, policy := range policies {
		if routePolicyMatches(policy.Match, flow) {
			return RouteDecision{Matched: true, RuleID: policy.ID, Action: policy.Action, Egress: policy.Egress, NextHop: policy.NextHop}
		}
	}
	return RouteDecision{Matched: false}
}

func activeDNSOverride(sourceIP, resolvedIP string, overrides []DNSOverrideIntent, now time.Time) (DNSOverrideIntent, bool) {
	for _, override := range overrides {
		if strings.TrimSpace(override.Egress) == "" || domainEntryExpired(override.ExpiresAt, now) {
			continue
		}
		if !ipSelectorMatches(override.Source, sourceIP) {
			continue
		}
		if strings.TrimSpace(override.ResolvedIP) == strings.TrimSpace(resolvedIP) {
			return override, true
		}
	}
	return DNSOverrideIntent{}, false
}

func routePolicyMatches(match Match, flow Flow) bool {
	return ipSelectorsMatch(match.Sources, flow.SourceIP) && ipSelectorsMatch(match.Destinations, flow.DestIP) && valueSelectorsMatch(match.Protocols, flow.Protocol) && valueSelectorsMatch(match.SourcePorts, flow.SourcePort) && valueSelectorsMatch(match.DestPorts, flow.DestPort)
}

func ipSelectorsMatch(selectors []string, value string) bool {
	if len(selectors) == 0 {
		return true
	}
	for _, selector := range selectors {
		if ipSelectorMatches(selector, value) {
			return true
		}
	}
	return false
}

func ipSelectorMatches(selector, value string) bool {
	selector = strings.TrimSpace(selector)
	value = strings.TrimSpace(value)
	if selector == "" || selector == "any" || selector == "0.0.0.0/0" {
		return true
	}
	if selector == value {
		return true
	}
	if start, end, ok := parseIPRange(selector); ok {
		addr, err := netip.ParseAddr(value)
		return err == nil && start.Compare(addr) <= 0 && addr.Compare(end) <= 0
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}
	if strings.Contains(selector, "/") {
		prefix, err := netip.ParsePrefix(selector)
		return err == nil && prefix.Contains(addr)
	}
	selectorAddr, err := netip.ParseAddr(selector)
	return err == nil && selectorAddr == addr
}

func valueSelectorsMatch(selectors []string, value string) bool {
	if len(selectors) == 0 {
		return true
	}
	value = strings.ToLower(strings.TrimSpace(value))
	for _, selector := range selectors {
		selector = strings.ToLower(strings.TrimSpace(selector))
		if selector == "" || selector == "any" || selector == value {
			return true
		}
	}
	return false
}

func rejectUnsupportedMatchFields(match map[string]any) error {
	allowed := map[string]struct{}{
		"src_ip": {}, "source": {}, "source_ip": {}, "sources": {},
		"dst_ip": {}, "destination": {}, "destination_ip": {}, "destinations": {},
		"protocol": {}, "protocols": {}, "src_port": {}, "source_port": {}, "source_ports": {}, "dst_port": {}, "destination_port": {}, "dest_ports": {}, "destination_ports": {},
		"direction": {}, "domain": {}, "domain_group": {}, "dst_domain": {}, "destination_domain": {},
		"enabled": {}, "schedule": {}, "description": {}, "name": {},
	}
	for key := range match {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unsupported policy condition %q", key)
		}
	}
	return nil
}

func expandDomainMatches(item, match map[string]any, domains map[string][]string) []string {
	if len(domains) == 0 {
		return nil
	}
	tokens := append(splitTokens(stringValue(item, "domain", "domain_group", "dst_domain", "destination_domain")), splitTokens(stringValue(match, "domain", "domain_group", "dst_domain", "destination_domain"))...)
	var ips []string
	for _, token := range tokens {
		ips = append(ips, domains[token]...)
	}
	return uniqueStrings(ips)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func expandAddressList(value string, groups map[string]objectGroup) ([]string, error) {
	expanded, err := expandAddressTokens(splitTokens(value), groups, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if len(expanded) == 0 {
		return []string{"0.0.0.0/0"}, nil
	}
	return expanded, nil
}

func expandAddressTokens(tokens []string, groups map[string]objectGroup, visiting map[string]bool) ([]string, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	expanded := []string{}
	for _, token := range tokens {
		if token == "any" || token == "wan" {
			expanded = append(expanded, "0.0.0.0/0")
			continue
		}
		if err := requireCommandToken(token, "address match"); err != nil {
			return nil, err
		}
		if group, ok := groups[token]; ok {
			if visiting[token] {
				return nil, fmt.Errorf("object group %q has cyclic reference", token)
			}
			if group.Kind != "" && group.Kind != "ip" {
				return nil, fmt.Errorf("object group %q kind %q cannot compile to VPP IP ACL", token, group.Kind)
			}
			visiting[token] = true
			for _, entry := range group.Entries {
				if err := requireCommandToken(entry, "object group entry"); err != nil {
					return nil, err
				}
				entries, err := normalizeAddressEntry(entry)
				if err != nil {
					return nil, err
				}
				expanded = append(expanded, entries...)
			}
			if len(group.References) > 0 {
				referenced, err := expandAddressTokens(group.References, groups, visiting)
				if err != nil {
					return nil, err
				}
				expanded = append(expanded, referenced...)
			}
			delete(visiting, token)
			continue
		}
		entries, err := normalizeAddressEntry(token)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, entries...)
	}
	return uniqueStrings(expanded), nil
}

func parseIPRange(value string) (netip.Addr, netip.Addr, bool) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return netip.Addr{}, netip.Addr{}, false
	}
	start, startErr := netip.ParseAddr(strings.TrimSpace(parts[0]))
	end, endErr := netip.ParseAddr(strings.TrimSpace(parts[1]))
	if startErr != nil || endErr != nil || start.BitLen() != end.BitLen() || start.Compare(end) > 0 {
		return netip.Addr{}, netip.Addr{}, false
	}
	return start, end, true
}

func normalizeAddressEntry(entry string) ([]string, error) {
	if strings.Contains(entry, "-") {
		start, end, ok := parseIPRange(entry)
		if !ok {
			return nil, fmt.Errorf("invalid IP range %q", entry)
		}
		return addressRangePrefixes(start, end), nil
	}
	if address, err := netip.ParseAddr(entry); err == nil {
		bits := 128
		if address.Is4() {
			bits = 32
		}
		return []string{netip.PrefixFrom(address, bits).String()}, nil
	} else {
		if _, prefixErr := netip.ParsePrefix(entry); prefixErr != nil {
			return nil, fmt.Errorf("invalid IP address or prefix %q", entry)
		}
	}
	return []string{entry}, nil
}

func addressRangePrefixes(start, end netip.Addr) []string {
	bitLen := start.BitLen()
	current := addressInteger(start)
	last := addressInteger(end)
	one := big.NewInt(1)
	result := []string{}
	for current.Cmp(last) <= 0 {
		alignmentBits := 0
		for alignmentBits < bitLen && current.Bit(alignmentBits) == 0 {
			alignmentBits++
		}
		remaining := new(big.Int).Sub(last, current)
		remaining.Add(remaining, one)
		sizeBits := remaining.BitLen() - 1
		blockBits := alignmentBits
		if sizeBits < blockBits {
			blockBits = sizeBits
		}
		result = append(result, netip.PrefixFrom(integerAddress(current, bitLen), bitLen-blockBits).String())
		step := new(big.Int).Lsh(new(big.Int).Set(one), uint(blockBits))
		current.Add(current, step)
	}
	return result
}

func addressInteger(addr netip.Addr) *big.Int {
	if addr.Is4() {
		raw := addr.As4()
		return new(big.Int).SetBytes(raw[:])
	}
	raw := addr.As16()
	return new(big.Int).SetBytes(raw[:])
}

func integerAddress(value *big.Int, bitLen int) netip.Addr {
	if bitLen == 32 {
		var raw [4]byte
		value.FillBytes(raw[:])
		return netip.AddrFrom4(raw)
	}
	var raw [16]byte
	value.FillBytes(raw[:])
	return netip.AddrFrom16(raw)
}

func expandPortList(value string, groups map[string]objectGroup, protocols *[]string) ([]string, error) {
	return expandPortTokens(splitTokens(value), groups, protocols, map[string]bool{})
}

func expandPortTokens(tokens []string, groups map[string]objectGroup, protocols *[]string, visiting map[string]bool) ([]string, error) {
	ports := []string{}
	for _, token := range tokens {
		if err := requireCommandToken(token, "port match"); err != nil {
			return nil, err
		}
		if group, ok := groups[token]; ok {
			if visiting[token] {
				return nil, fmt.Errorf("object group %q has cyclic reference", token)
			}
			if group.Kind != "port" && group.Kind != "service" {
				return nil, fmt.Errorf("object group %q kind %q cannot compile to VPP port ACL", token, group.Kind)
			}
			visiting[token] = true
			entries, err := expandPortTokens(group.Entries, groups, protocols, visiting)
			if err != nil {
				return nil, err
			}
			ports = append(ports, entries...)
			if len(group.References) > 0 {
				referenced, err := expandPortTokens(group.References, groups, protocols, visiting)
				if err != nil {
					return nil, err
				}
				ports = append(ports, referenced...)
			}
			delete(visiting, token)
			continue
		}
		parts := strings.SplitN(token, "/", 2)
		if len(parts) == 2 {
			proto := strings.ToLower(strings.TrimSpace(parts[0]))
			port := strings.TrimSpace(parts[1])
			if proto != "" && !contains(*protocols, proto) {
				*protocols = append(*protocols, proto)
			}
			if port != "" {
				ports = append(ports, port)
			}
			continue
		}
		ports = append(ports, token)
	}
	return ports, nil
}

func splitTokens(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		if token := strings.ToLower(strings.TrimSpace(part)); token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func requireCommandToken(value, field string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if strings.ContainsAny(trimmed, " \t\r\n|&;$`()<>\\\"") {
		return fmt.Errorf("%s %q contains unsupported command characters", field, value)
	}
	return nil
}

func normalizedAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "route", "指定线路", "v6出口":
		return "route"
	case "nat", "snat", "dnat", "nat线路":
		return "nat"
	case "deny", "drop", "block", "阻断":
		return "deny"
	case "allow", "permit", "accept":
		return "permit"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizedDirection(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "egress", "output", "out", "lan_to_wan":
		return "output"
	default:
		return "input"
	}
}

func enabled(item map[string]any) bool {
	value, ok := item["enabled"]
	if !ok {
		return true
	}
	boolValue, ok := value.(bool)
	return !ok || boolValue
}

func stringValue(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func selectorValue(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		if values := stringSlice(item[key]); len(values) > 0 {
			return strings.Join(values, ",")
		}
	}
	return ""
}

func intValue(item map[string]any, fallback int, keys ...string) int {
	for _, key := range keys {
		switch value := item[key].(type) {
		case int:
			return value
		case float64:
			return int(value)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				return parsed
			}
		}
	}
	return fallback
}

func mapValue(item map[string]any, key string) map[string]any {
	if value, ok := item[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func entryValues(value any) []string {
	entries := []string{}
	for _, item := range anySlice(value) {
		switch typed := item.(type) {
		case string:
			if entry := strings.TrimSpace(typed); entry != "" {
				entries = append(entries, entry)
			}
		case map[string]any:
			if entry := stringValue(typed, "value", "id"); entry != "" {
				entries = append(entries, entry)
			}
		}
	}
	return entries
}

func stringSlice(value any) []string {
	entries := []string{}
	for _, item := range anySlice(value) {
		if entry := strings.TrimSpace(fmt.Sprint(item)); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func anySlice(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	return nil
}
