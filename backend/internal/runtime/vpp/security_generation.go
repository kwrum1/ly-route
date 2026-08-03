package vpp

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

// SecurityGeneration is the VPP-owned representation of all baseline
// security controls. Standard ACLs are grouped by interface and direction so
// one apply cannot accidentally replace another ACL's interface attachment.
type SecurityGeneration struct {
	ID          string                 `json:"id"`
	ACLs        []SecurityInterfaceACL `json:"acls,omitempty"`
	MACIP       []SecurityMACIPACL     `json:"macip,omitempty"`
	AttackRules []SecurityAttackRule   `json:"attack_rules,omitempty"`
}

type SecurityInterfaceACL struct {
	Interface string                      `json:"interface"`
	Direction string                      `json:"direction"`
	Rules     []trafficpolicy.SecurityACL `json:"rules"`
}

type SecurityMACIPACL struct {
	Interface       string              `json:"interface"`
	Mode            string              `json:"mode"`
	Bindings        []SecurityMACIPRule `json:"bindings"`
	UnboundBehavior string              `json:"unbound_behavior"`
}

type SecurityMACIPRule struct {
	IP  string `json:"ip"`
	MAC string `json:"mac"`
}

type SecurityAttackRule struct {
	ID                string `json:"id"`
	Interface         string `json:"interface"`
	AttackType        string `json:"attack_type"`
	ThresholdPPS      int    `json:"threshold_pps"`
	BurstPackets      int    `json:"burst_packets"`
	EnforcementMode   string `json:"enforcement_mode"`
	SourcePrefix      string `json:"source_prefix"`
	DestinationPrefix string `json:"destination_prefix"`
}

// CompileSecurityGeneration combines the typed API resources before any VPP
// command is emitted. It preserves priority ordering and makes the implicit
// terminal behavior explicit, which is required for a safe ACL attachment.
func CompileSecurityGeneration(id, defaultInterface string, acls []trafficpolicy.SecurityACL, bindings []SecurityMACIPACL, threats []SecurityThreatList, attacks []SecurityAttackRule) (SecurityGeneration, error) {
	if strings.TrimSpace(id) == "" {
		return SecurityGeneration{}, fmt.Errorf("security generation id is required")
	}
	if strings.TrimSpace(defaultInterface) == "" && len(acls) > 0 {
		return SecurityGeneration{}, fmt.Errorf("security generation LAN interface is required")
	}
	generation := SecurityGeneration{ID: id}
	groups := map[string][]trafficpolicy.SecurityACL{}
	for _, acl := range acls {
		direction := strings.ToLower(strings.TrimSpace(acl.Match.Direction))
		if direction == "" {
			direction = "input"
		}
		if direction != "input" && direction != "output" {
			return SecurityGeneration{}, fmt.Errorf("security ACL %q has unsupported direction %q", acl.ID, direction)
		}
		groups[defaultInterface+"\x00"+direction] = append(groups[defaultInterface+"\x00"+direction], acl)
	}
	for _, threat := range threats {
		iface := strings.TrimSpace(threat.Interface)
		if iface == "" {
			return SecurityGeneration{}, fmt.Errorf("threat list %q has no interface", threat.ID)
		}
		for _, direction := range threat.Directions() {
			key := iface + "\x00" + direction
			if threat.ListType == "whitelist" {
				for index, entry := range threat.Entries {
					match, err := threatSecurityMatch(entry, direction)
					if err != nil {
						return SecurityGeneration{}, fmt.Errorf("threat list %q entry %q: %w", threat.ID, entry, err)
					}
					groups[key] = append(groups[key], trafficpolicy.SecurityACL{ID: fmt.Sprintf("%s-%d", threat.ID, index), Priority: threat.Priority, Action: "permit", Match: match})
				}
				groups[key] = append(groups[key], terminalSecurityRules(threat.ID, direction, threat.Priority+1, "deny")...)
			} else {
				for index, entry := range threat.Entries {
					match, err := threatSecurityMatch(entry, direction)
					if err != nil {
						return SecurityGeneration{}, fmt.Errorf("threat list %q entry %q: %w", threat.ID, entry, err)
					}
					groups[key] = append(groups[key], trafficpolicy.SecurityACL{ID: fmt.Sprintf("%s-%d", threat.ID, index), Priority: threat.Priority, Action: "deny", Match: match})
				}
				groups[key] = append(groups[key], terminalSecurityRules(threat.ID, direction, threat.Priority+1, "permit")...)
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		rules := groups[key]
		sort.SliceStable(rules, func(left, right int) bool { return rules[left].Priority < rules[right].Priority })
		generation.ACLs = append(generation.ACLs, SecurityInterfaceACL{Interface: parts[0], Direction: parts[1], Rules: rules})
	}
	for _, macip := range bindings {
		if strings.TrimSpace(macip.Interface) == "" || len(macip.Bindings) == 0 {
			return SecurityGeneration{}, fmt.Errorf("IP-MAC generation requires interface and bindings")
		}
		generation.MACIP = append(generation.MACIP, macip)
	}
	for _, attack := range attacks {
		if attack.ThresholdPPS < 1 || attack.BurstPackets < 1 {
			return SecurityGeneration{}, fmt.Errorf("attack rule %q thresholds must be positive", attack.ID)
		}
		if attack.EnforcementMode != "alert" && attack.EnforcementMode != "enforce" {
			return SecurityGeneration{}, fmt.Errorf("attack rule %q has invalid enforcement mode", attack.ID)
		}
		generation.AttackRules = append(generation.AttackRules, attack)
	}
	return generation, nil
}

type SecurityThreatList struct {
	ID        string
	Interface string
	Priority  int
	ListType  string
	Direction string
	Entries   []string
}

func (threat SecurityThreatList) Directions() []string {
	if threat.Direction == "both" {
		return []string{"input", "output"}
	}
	return []string{threat.Direction}
}

func threatSecurityMatch(entry, direction string) (trafficpolicy.Match, error) {
	prefix, family, err := securityAddressSelector(entry)
	if err != nil || family == 0 {
		return trafficpolicy.Match{}, fmt.Errorf("must be an IP address or CIDR")
	}
	any := "0.0.0.0/0"
	if family == 6 {
		any = "::/0"
	}
	match := trafficpolicy.Match{Sources: []string{prefix}, Destinations: []string{any}, Protocols: []string{"any"}, Direction: direction}
	if direction == "output" {
		match.Sources, match.Destinations = []string{any}, []string{prefix}
	}
	return match, nil
}

func terminalSecurityRules(id, direction string, priority int, action string) []trafficpolicy.SecurityACL {
	result := make([]trafficpolicy.SecurityACL, 0, 2)
	for _, family := range []struct{ suffix, prefix string }{{"ipv4", "0.0.0.0/0"}, {"ipv6", "::/0"}} {
		result = append(result, trafficpolicy.SecurityACL{ID: id + "-terminal-" + action + "-" + family.suffix, Priority: priority, Action: action, Match: trafficpolicy.Match{Sources: []string{family.prefix}, Destinations: []string{family.prefix}, Protocols: []string{"any"}, Direction: direction}})
	}
	return result
}

func securityGenerationCommands(generation SecurityGeneration) ([]string, error) {
	commands := []string{"show acl-plugin acl", "show acl-plugin interface", "show acl-plugin macip acl", "show acl-plugin macip interface", "show policer", "show ly-route security-guard"}
	for _, group := range generation.ACLs {
		if len(group.Rules) == 0 {
			continue
		}
		rules, err := securityInterfaceACLRules(group.Rules)
		if err != nil {
			return nil, err
		}
		tag := "ly-route-security-gen-" + safeTag(group.Interface+"-"+group.Direction)
		commands = append(commands, fmt.Sprintf("set acl-plugin acl %s tag %s", strings.Join(rules, ", "), tag), fmt.Sprintf("set acl-plugin interface %s %s acl 0", group.Interface, group.Direction))
	}
	for _, group := range generation.MACIP {
		rules, err := securityMACIPRules(group)
		if err != nil {
			return nil, err
		}
		tag := "ly-route-security-macip-" + safeTag(group.Interface)
		commands = append(commands, fmt.Sprintf("set acl-plugin macip acl %s tag %s", strings.Join(rules, ", "), tag), fmt.Sprintf("set acl-plugin macip interface %s acl 0", group.Interface))
	}
	attackCommands, err := securityAttackCommands(generation.AttackRules)
	if err != nil {
		return nil, err
	}
	commands = append(commands, attackCommands...)
	return commands, nil
}

type securityAttackFamilyRule struct {
	Rule   SecurityAttackRule
	Family int
	Source string
	Dest   string
}

func securityAttackFamilies(attacks []SecurityAttackRule) ([]securityAttackFamilyRule, error) {
	result := make([]securityAttackFamilyRule, 0, len(attacks)*2)
	for _, attack := range attacks {
		if strings.TrimSpace(attack.ID) == "" || strings.TrimSpace(attack.Interface) == "" {
			return nil, fmt.Errorf("attack rule ID and interface are required")
		}
		if attack.ThresholdPPS < 1 || attack.BurstPackets < 1 {
			return nil, fmt.Errorf("attack rule %q thresholds must be positive", attack.ID)
		}
		if attack.EnforcementMode != "alert" && attack.EnforcementMode != "enforce" {
			return nil, fmt.Errorf("attack rule %q has invalid enforcement mode", attack.ID)
		}
		source, sourceFamily, err := securityAddressSelector(attack.SourcePrefix)
		if err != nil {
			return nil, fmt.Errorf("attack rule %q has invalid source prefix: %w", attack.ID, err)
		}
		dest, destFamily, err := securityAddressSelector(attack.DestinationPrefix)
		if err != nil {
			return nil, fmt.Errorf("attack rule %q has invalid destination prefix: %w", attack.ID, err)
		}
		if sourceFamily != 0 && destFamily != 0 && sourceFamily != destFamily {
			return nil, fmt.Errorf("attack rule %q source and destination use different address families", attack.ID)
		}
		families := []int{4, 6}
		if sourceFamily != 0 {
			families = []int{sourceFamily}
		} else if destFamily != 0 {
			families = []int{destFamily}
		}
		for _, family := range families {
			resolvedSource, resolvedDest := source, dest
			if sourceFamily == 0 {
				resolvedSource = securityAnyPrefix(family)
			}
			if destFamily == 0 {
				resolvedDest = securityAnyPrefix(family)
			}
			result = append(result, securityAttackFamilyRule{Rule: attack, Family: family, Source: resolvedSource, Dest: resolvedDest})
		}
	}
	return result, nil
}

func securityAttackRuleID(item securityAttackFamilyRule) string {
	return "ly-route-security-attack-" + safeTag(item.Rule.ID) + fmt.Sprintf("-ip%d", item.Family)
}

func securityAttackCommands(attacks []SecurityAttackRule) ([]string, error) {
	items, err := securityAttackFamilies(attacks)
	if err != nil {
		return nil, err
	}
	commands := make([]string, 0, len(items))
	for _, item := range items {
		family := "ip4"
		if item.Family == 6 {
			family = "ip6"
		}
		commands = append(commands, fmt.Sprintf("set ly-route security-guard rule %s interface %s family %s attack-type %s threshold-pps %d burst-packets %d mode %s source %s destination %s", securityAttackRuleID(item), item.Rule.Interface, family, item.Rule.AttackType, item.Rule.ThresholdPPS, item.Rule.BurstPackets, item.Rule.EnforcementMode, item.Source, item.Dest))
	}
	return commands, nil
}

func securityMACIPRules(group SecurityMACIPACL) ([]string, error) {
	rules := make([]string, 0, len(group.Bindings)+1)
	for _, binding := range group.Bindings {
		ip, err := netip.ParseAddr(binding.IP)
		if err != nil || !ip.Is4() {
			return nil, fmt.Errorf("IP-MAC rule %q is not IPv4", binding.IP)
		}
		mac, err := net.ParseMAC(binding.MAC)
		if err != nil || len(mac) != 6 {
			return nil, fmt.Errorf("IP-MAC rule %q has invalid MAC", binding.MAC)
		}
		rules = append(rules, fmt.Sprintf("permit ip %s/32 mac %s mask ff:ff:ff:ff:ff:ff", ip, mac))
	}
	if group.Mode == "enforce" {
		rules = append(rules, "deny ip 0.0.0.0/0 mac 00:00:00:00:00:00 mask 00:00:00:00:00:00")
	} else {
		rules = append(rules, "permit ip 0.0.0.0/0 mac 00:00:00:00:00:00 mask 00:00:00:00:00:00")
	}
	return rules, nil
}

func securityInterfaceACLRules(rules []trafficpolicy.SecurityACL) ([]string, error) {
	commands := make([]string, 0)
	for _, acl := range rules {
		pairs, err := securityACLAddressPairs(acl.Match)
		if err != nil {
			return nil, fmt.Errorf("security ACL %q: %w", acl.ID, err)
		}
		for _, pair := range pairs {
			for _, protocol := range nonEmptyList(acl.Match.Protocols, "any") {
				proto, err := securityVPPProtocol(protocol, pair.Family)
				if err != nil {
					return nil, err
				}
				for _, sourcePort := range nonEmptyList(acl.Match.SourcePorts, "any") {
					for _, destinationPort := range nonEmptyList(acl.Match.DestPorts, "any") {
						commands = append(commands, fmt.Sprintf("%s src %s dst %s proto %s sport %s dport %s", acl.Action, pair.Source, pair.Destination, proto, portRange(sourcePort), portRange(destinationPort)))
					}
				}
			}
		}
	}
	return commands, nil
}

type securityACLAddressPair struct {
	Source      string
	Destination string
	Family      int
}

func securityACLAddressPairs(match trafficpolicy.Match) ([]securityACLAddressPair, error) {
	sources := nonEmptyList(match.Sources, "any")
	destinations := nonEmptyList(match.Destinations, "any")
	result := make([]securityACLAddressPair, 0, len(sources)*len(destinations))
	for _, source := range sources {
		source, sourceFamily, err := securityAddressSelector(source)
		if err != nil {
			return nil, fmt.Errorf("invalid source %q", source)
		}
		for _, destination := range destinations {
			destination, destinationFamily, err := securityAddressSelector(destination)
			if err != nil {
				return nil, fmt.Errorf("invalid destination %q", destination)
			}
			families := []int{sourceFamily}
			if sourceFamily == 0 {
				families = []int{destinationFamily}
			}
			if sourceFamily == 0 && destinationFamily == 0 {
				families = []int{4, 6}
			}
			if sourceFamily != 0 && destinationFamily != 0 && sourceFamily != destinationFamily {
				return nil, fmt.Errorf("source %q and destination %q use different address families", source, destination)
			}
			for _, family := range families {
				resolvedSource, resolvedDestination := source, destination
				if sourceFamily == 0 {
					resolvedSource = securityAnyPrefix(family)
				}
				if destinationFamily == 0 {
					resolvedDestination = securityAnyPrefix(family)
				}
				result = append(result, securityACLAddressPair{Source: resolvedSource, Destination: resolvedDestination, Family: family})
			}
		}
	}
	return result, nil
}

func securityAddressSelector(value string) (string, int, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "any") {
		return "any", 0, nil
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		prefix = prefix.Masked()
		if prefix.Addr().Is4() {
			return prefix.String(), 4, nil
		}
		return prefix.String(), 6, nil
	}
	if address, err := netip.ParseAddr(value); err == nil {
		if address.Is4() {
			return address.String() + "/32", 4, nil
		}
		return address.String() + "/128", 6, nil
	}
	return "", 0, fmt.Errorf("invalid IP selector")
}

func securityAnyPrefix(family int) string {
	if family == 6 {
		return "::/0"
	}
	return "0.0.0.0/0"
}

func securityVPPProtocol(protocol string, family int) (string, error) {
	if family == 6 && strings.EqualFold(strings.TrimSpace(protocol), "icmp") {
		return "58", nil
	}
	return vppProtocol(protocol)
}
