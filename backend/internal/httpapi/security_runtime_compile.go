package httpapi

import (
	"context"
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func (server *Server) currentSecurityGeneration(ctx context.Context, policy trafficpolicy.Config, assignments []vpp.AddressAssignment) (vpp.SecurityGeneration, error) {
	ipmacItems, err := server.desiredItems(ctx, "security_ip_mac_binding")
	if err != nil {
		return vpp.SecurityGeneration{}, err
	}
	threatItems, err := server.desiredItems(ctx, "security_threat_intel")
	if err != nil {
		return vpp.SecurityGeneration{}, err
	}
	attackItems, err := server.desiredItems(ctx, "security_attack_rule")
	if err != nil {
		return vpp.SecurityGeneration{}, err
	}
	runtimeACLs := make([]trafficpolicy.SecurityACL, 0, len(policy.SecurityACLs))
	for _, acl := range policy.SecurityACLs {
		if acl.ID != "sec-acl-default-deny-wan" {
			runtimeACLs = append(runtimeACLs, acl)
		}
	}
	if len(runtimeACLs) == 0 && !hasEnabledSecurityItems(ipmacItems) && !hasEnabledSecurityItems(threatItems) && !hasEnabledSecurityItems(attackItems) {
		return vpp.SecurityGeneration{}, nil
	}
	lanInterface := ""
	for _, assignment := range assignments {
		if strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			lanInterface = assignment.VPPInterface
			break
		}
	}
	if lanInterface == "" && len(runtimeACLs) > 0 {
		return vpp.SecurityGeneration{}, fmt.Errorf("security runtime requires one configured LAN interface")
	}
	macip := make([]vpp.SecurityMACIPACL, 0, len(ipmacItems))
	for _, item := range ipmacItems {
		if enabled, ok := item["enabled"].(bool); ok && !enabled {
			continue
		}
		bindings, ok := item["bindings"].([]any)
		if !ok {
			return vpp.SecurityGeneration{}, fmt.Errorf("IP-MAC resource %q bindings are not typed", stringField(item, "id"))
		}
		converted := make([]vpp.SecurityMACIPRule, 0, len(bindings))
		for _, raw := range bindings {
			binding, ok := raw.(map[string]any)
			if !ok {
				return vpp.SecurityGeneration{}, fmt.Errorf("IP-MAC resource %q contains an invalid binding", stringField(item, "id"))
			}
			converted = append(converted, vpp.SecurityMACIPRule{IP: stringField(binding, "ip"), MAC: stringField(binding, "mac")})
		}
		iface, err := resolveSecurityInterface(stringField(item, "interface_id"), assignments)
		if err != nil {
			return vpp.SecurityGeneration{}, err
		}
		macip = append(macip, vpp.SecurityMACIPACL{Interface: iface, Mode: stringField(item, "binding_mode"), Bindings: converted, UnboundBehavior: stringField(item, "unbound_behavior")})
	}
	threats := make([]vpp.SecurityThreatList, 0, len(threatItems))
	for _, item := range threatItems {
		if enabled, ok := item["enabled"].(bool); ok && !enabled {
			continue
		}
		entries, ok := item["entries"].([]any)
		if !ok {
			return vpp.SecurityGeneration{}, fmt.Errorf("threat resource %q entries are not typed", stringField(item, "id"))
		}
		converted := make([]string, 0, len(entries))
		for _, raw := range entries {
			value, ok := raw.(string)
			if !ok {
				return vpp.SecurityGeneration{}, fmt.Errorf("threat resource %q contains a non-string entry", stringField(item, "id"))
			}
			converted = append(converted, value)
		}
		iface, err := resolveSecurityInterface(stringField(item, "interface_id"), assignments)
		if err != nil {
			return vpp.SecurityGeneration{}, err
		}
		priority, _ := positiveJSONInteger(item["priority"])
		threats = append(threats, vpp.SecurityThreatList{ID: stringField(item, "id"), Interface: iface, Priority: priority, ListType: stringField(item, "list_type"), Direction: stringField(item, "direction"), Entries: converted})
	}
	attacks := make([]vpp.SecurityAttackRule, 0, len(attackItems))
	for _, item := range attackItems {
		if enabled, ok := item["enabled"].(bool); ok && !enabled {
			continue
		}
		iface, err := resolveSecurityInterface(stringField(item, "interface_id"), assignments)
		if err != nil {
			return vpp.SecurityGeneration{}, err
		}
		threshold, _ := positiveJSONInteger(item["threshold_pps"])
		burst, _ := positiveJSONInteger(item["burst_packets"])
		attacks = append(attacks, vpp.SecurityAttackRule{ID: stringField(item, "id"), Interface: iface, AttackType: stringField(item, "attack_type"), ThresholdPPS: threshold, BurstPackets: burst, EnforcementMode: stringField(item, "enforcement_mode"), SourcePrefix: stringField(item, "source_prefix"), DestinationPrefix: stringField(item, "destination_prefix")})
	}
	return vpp.CompileSecurityGeneration("gateway-security", lanInterface, runtimeACLs, macip, threats, attacks)
}

func hasEnabledSecurityItems(items []map[string]any) bool {
	for _, item := range items {
		if enabled, ok := item["enabled"].(bool); !ok || enabled {
			return true
		}
	}
	return false
}

func resolveSecurityInterface(id string, assignments []vpp.AddressAssignment) (string, error) {
	id = strings.TrimSpace(id)
	for _, assignment := range assignments {
		if id == assignment.ID || id == assignment.LinuxInterface || id == assignment.VPPInterface {
			return assignment.VPPInterface, nil
		}
	}
	return "", fmt.Errorf("security interface %q is not an active configured interface", id)
}
