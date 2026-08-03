package httpapi

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

var (
	securityIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	securityInterfacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,63}$`)
)

// validateSecurityDesiredPayload is deliberately narrower than a generic
// firewall schema. LY-Route's baseline security boundary is L2-L4 only: it
// must never imply DPI, application identification, URL/SNI filtering, IDS/IPS
// or user-behaviour analytics.
func validateSecurityDesiredPayload(resourceType string, payload map[string]any) error {
	if err := rejectForbiddenSecurityCapability(payload, "$"); err != nil {
		return err
	}
	if err := requireSecurityCommon(payload); err != nil {
		return err
	}
	switch resourceType {
	case "security_acl":
		return validateSecurityACLContract(payload)
	case "security_ip_mac_binding":
		return validateIPMACBindingContract(payload)
	case "security_threat_intel":
		return validateThreatIntelContract(payload)
	case "security_attack_rule":
		return validateAttackRuleContract(payload)
	default:
		return nil
	}
}

func requireSecurityCommon(payload map[string]any) error {
	id := strings.TrimSpace(stringField(payload, "id"))
	if !securityIDPattern.MatchString(id) {
		return fmt.Errorf("security resource id must be 1-64 safe identifier characters")
	}
	if value, ok := payload["enabled"]; ok {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("security resource enabled must be boolean")
		}
	} else {
		payload["enabled"] = true
	}
	priority, ok := positiveJSONInteger(payload["priority"])
	if !ok {
		return fmt.Errorf("security resource priority must be a positive integer")
	}
	payload["priority"] = priority
	for _, key := range []string{"name", "description"} {
		if value, ok := payload[key]; ok {
			text, ok := value.(string)
			if !ok || len(strings.TrimSpace(text)) > 256 {
				return fmt.Errorf("security resource %s must be a string no longer than 256 characters", key)
			}
		}
	}
	return nil
}

func validateSecurityACLContract(payload map[string]any) error {
	if err := rejectUnknownSecurityFields(payload, "security ACL", "id", "kind", "name", "description", "enabled", "priority", "action", "match"); err != nil {
		return err
	}
	action := strings.ToLower(strings.TrimSpace(stringField(payload, "action")))
	if action == "allow" {
		action = "permit"
	}
	if action != "permit" && action != "deny" {
		return fmt.Errorf("security ACL action must be permit or deny")
	}
	payload["action"] = action
	match, ok := payload["match"].(map[string]any)
	if !ok || len(match) == 0 {
		return fmt.Errorf("security ACL requires a non-empty L3/L4 match")
	}
	return nil
}

func validateIPMACBindingContract(payload map[string]any) error {
	if err := rejectUnknownSecurityFields(payload, "IP-MAC binding", "id", "kind", "name", "description", "enabled", "priority", "interface_id", "binding_mode", "unbound_behavior", "bindings"); err != nil {
		return err
	}
	if err := validateSecurityInterface(payload); err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(stringField(payload, "binding_mode")))
	behavior := strings.ToLower(strings.TrimSpace(stringField(payload, "unbound_behavior")))
	if mode != "alert" && mode != "enforce" {
		return fmt.Errorf("IP-MAC binding_mode must be alert or enforce")
	}
	if mode == "alert" && behavior != "audit_only" {
		return fmt.Errorf("IP-MAC alert mode requires unbound_behavior audit_only")
	}
	if mode == "enforce" && behavior != "block" {
		return fmt.Errorf("IP-MAC enforce mode requires unbound_behavior block")
	}
	bindings, ok := payload["bindings"].([]any)
	if !ok || len(bindings) == 0 || len(bindings) > 4096 {
		return fmt.Errorf("IP-MAC bindings must contain 1-4096 entries")
	}
	seen := map[string]struct{}{}
	for index, raw := range bindings {
		binding, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("IP-MAC bindings[%d] must be an object", index)
		}
		if err := rejectUnknownSecurityFields(binding, fmt.Sprintf("IP-MAC bindings[%d]", index), "ip", "mac"); err != nil {
			return err
		}
		ip, err := netip.ParseAddr(strings.TrimSpace(stringField(binding, "ip")))
		if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("IP-MAC bindings[%d].ip must be a usable IPv4 address", index)
		}
		mac, err := net.ParseMAC(strings.TrimSpace(stringField(binding, "mac")))
		if err != nil || len(mac) != 6 || mac[0]&1 != 0 || allZeroBytes(mac) {
			return fmt.Errorf("IP-MAC bindings[%d].mac must be a unicast 48-bit MAC address", index)
		}
		key := ip.String() + "/" + strings.ToLower(mac.String())
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("IP-MAC bindings[%d] duplicates an earlier binding", index)
		}
		seen[key] = struct{}{}
		binding["ip"] = ip.String()
		binding["mac"] = strings.ToLower(mac.String())
	}
	return nil
}

func validateThreatIntelContract(payload map[string]any) error {
	if err := rejectUnknownSecurityFields(payload, "threat list", "id", "kind", "name", "description", "enabled", "priority", "interface_id", "direction", "list_type", "entries"); err != nil {
		return err
	}
	if err := validateSecurityInterface(payload); err != nil {
		return err
	}
	direction := strings.ToLower(strings.TrimSpace(stringField(payload, "direction")))
	if direction != "input" && direction != "output" && direction != "both" {
		return fmt.Errorf("threat list direction must be input, output or both")
	}
	listType := strings.ToLower(strings.TrimSpace(stringField(payload, "list_type")))
	if listType != "blacklist" && listType != "whitelist" {
		return fmt.Errorf("threat list list_type must be blacklist or whitelist")
	}
	entries, ok := payload["entries"].([]any)
	if !ok || len(entries) == 0 || len(entries) > 65536 {
		return fmt.Errorf("threat list entries must contain 1-65536 IP/CIDR values")
	}
	seen := map[string]struct{}{}
	canonical := make([]any, 0, len(entries))
	for index, raw := range entries {
		text, ok := raw.(string)
		if !ok {
			return fmt.Errorf("threat list entries[%d] must be an IP address or CIDR string", index)
		}
		prefix, err := parseSecurityPrefix(text)
		if err != nil {
			return fmt.Errorf("threat list entries[%d]: %w", index, err)
		}
		value := prefix.String()
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("threat list entries[%d] duplicates %s", index, value)
		}
		seen[value] = struct{}{}
		canonical = append(canonical, value)
	}
	payload["entries"] = canonical
	return nil
}

func validateAttackRuleContract(payload map[string]any) error {
	if err := rejectUnknownSecurityFields(payload, "attack rule", "id", "kind", "name", "description", "enabled", "priority", "interface_id", "attack_type", "threshold_pps", "burst_packets", "enforcement_mode", "source_prefix", "destination_prefix"); err != nil {
		return err
	}
	if err := validateSecurityInterface(payload); err != nil {
		return err
	}
	attackType := strings.ToLower(strings.TrimSpace(stringField(payload, "attack_type")))
	if !oneOf(attackType, []string{"syn_flood", "udp_flood", "icmp_flood", "new_connection_rate"}) {
		return fmt.Errorf("attack rule attack_type must be syn_flood, udp_flood, icmp_flood or new_connection_rate")
	}
	mode := strings.ToLower(strings.TrimSpace(stringField(payload, "enforcement_mode")))
	if mode != "alert" && mode != "enforce" {
		return fmt.Errorf("attack rule enforcement_mode must be alert or enforce")
	}
	threshold, ok := positiveJSONInteger(payload["threshold_pps"])
	if !ok || threshold > 1000000000 {
		return fmt.Errorf("attack rule threshold_pps must be an integer between 1 and 1000000000")
	}
	burst, ok := positiveJSONInteger(payload["burst_packets"])
	if !ok || burst > 1000000000 {
		return fmt.Errorf("attack rule burst_packets must be an integer between 1 and 1000000000")
	}
	for _, key := range []string{"source_prefix", "destination_prefix"} {
		value := strings.TrimSpace(stringField(payload, key))
		if value == "" || value == "any" {
			payload[key] = "any"
			continue
		}
		prefix, err := parseSecurityPrefix(value)
		if err != nil {
			return fmt.Errorf("attack rule %s: %w", key, err)
		}
		payload[key] = prefix.String()
	}
	payload["attack_type"] = attackType
	payload["enforcement_mode"] = mode
	payload["threshold_pps"] = threshold
	payload["burst_packets"] = burst
	return nil
}

func validateSecurityInterface(payload map[string]any) error {
	value := strings.TrimSpace(stringField(payload, "interface_id"))
	if !securityInterfacePattern.MatchString(value) || strings.Contains(value, "..") {
		return fmt.Errorf("security resource interface_id must be a safe interface identifier")
	}
	payload["interface_id"] = value
	return nil
}

func rejectUnknownSecurityFields(payload map[string]any, resource string, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		set[field] = struct{}{}
	}
	unknown := make([]string, 0)
	for field := range payload {
		if _, ok := set[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("%s contains unsupported field(s): %s", resource, strings.Join(unknown, ", "))
}

func rejectForbiddenSecurityCapability(value any, path string) error {
	forbidden := map[string]struct{}{
		"dpi": {}, "l7": {}, "domain": {}, "domains": {}, "url": {}, "sni": {},
		"application": {}, "application_id": {}, "app_id": {}, "app_identification": {},
		"user_behavior": {}, "behavior_audit": {}, "ids": {}, "ips": {}, "signature": {}, "signatures": {},
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
			if _, denied := forbidden[normalized]; denied {
				return fmt.Errorf("%s.%s requests an unsupported security capability; only L2-L4 baseline controls are supported", path, key)
			}
			if err := rejectForbiddenSecurityCapability(nested, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for index, nested := range typed {
			if err := rejectForbiddenSecurityCapability(nested, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseSecurityPrefix(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if address, err := netip.ParseAddr(value); err == nil {
		bits := 128
		if address.Is4() {
			bits = 32
		}
		return netip.PrefixFrom(address, bits), nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("must be an IP address or CIDR; domains and ranges are not accepted")
	}
	return prefix.Masked(), nil
}

func positiveJSONInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, typed > 0
	case float64:
		integer := int(typed)
		return integer, typed == float64(integer) && integer > 0
	default:
		return 0, false
	}
}

func allZeroBytes(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
