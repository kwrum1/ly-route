package vpp

import (
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

func proxySteeringDeleteCommands(steering proxy.VPPSteeringInstruction) []string {
	resource := steering.EgressID
	if strings.TrimSpace(resource) == "" {
		resource = string(steering.Handoff)
	}
	policyID := stableID("abf:"+resource, 1000, 8999)
	aclID := stableID("acl:"+resource, 1000, 8999)
	tableID := stableID("pbr:"+resource, 10000, 49999)
	interfaceName := "lyroute-$LY_ROUTE_LAN_INTERFACE"
	switch steering.TargetKind {
	case "vpp.abf.policy":
		return []string{fmt.Sprintf("?abf attach ip4 del policy %d %s", policyID, interfaceName), fmt.Sprintf("?abf policy del id %d", policyID), fmt.Sprintf("?delete acl-plugin acl index %d", aclID), fmt.Sprintf("show abf policy %d", policyID), fmt.Sprintf("show acl-plugin acl index %d", aclID), fmt.Sprintf("show abf attach %s", interfaceName)}
	case "vpp.pbr.policy":
		return []string{fmt.Sprintf("?ip route del table %d 0.0.0.0/0 via local", tableID), fmt.Sprintf("?ip table del %d", tableID), fmt.Sprintf("show ip table %d", tableID), fmt.Sprintf("show ip fib table %d", tableID)}
	case "vpp.service-chain.egress-binding":
		return []string{fmt.Sprintf("show interface %s", interfaceName), fmt.Sprintf("show abf attach %s", interfaceName), fmt.Sprintf("show ip table %d", tableID)}
	case "vpp.proxy-service.network":
		network := steering.ServiceNetwork
		if strings.TrimSpace(network.EgressID) == "" {
			network = proxy.ServiceNetworkForEgressID(resource)
		}
		return []string{
			fmt.Sprintf("?set interface nat44 in %s del", network.EgressVPPInterface),
			fmt.Sprintf("?ip route del table %d 0.0.0.0/0", network.OutboundTableID),
			fmt.Sprintf("?ip table del %d", network.OutboundTableID),
			fmt.Sprintf("?delete tap %s", network.IngressVPPInterface),
			fmt.Sprintf("?delete tap %s", network.EgressVPPInterface),
			"show tap",
		}
	default:
		return nil
	}
}

func dnsServiceNetworkDeleteCommands(network DNSServiceNetwork) []string {
	return []string{
		fmt.Sprintf("?set interface nat44 in %s del", network.VPPInterface),
		fmt.Sprintf("?ip route del table %d 0.0.0.0/0", network.TableID),
		fmt.Sprintf("?ip table del %d", network.TableID),
		fmt.Sprintf("?delete tap %s", network.VPPInterface),
		"show tap",
	}
}

func flowTargetDeleteCommands(target flow.Target) []string {
	interfaceName := "lyroute-$LY_ROUTE_LAN_INTERFACE"
	resource := target.RuleID
	if strings.TrimSpace(resource) == "" {
		resource = target.Kind
	}
	mapID := stableID("qos-map:"+resource, 1, 999)
	policerName := "ly_route_" + safeTag(resource)
	switch target.Kind {
	case "vpp.acl.drop":
		aclID := stableID("flow-acl-drop:"+resource, 10000, 49999)
		commands := flowDetachACLCommands(target, aclID)
		return append(commands, fmt.Sprintf("?delete acl-plugin acl index %d", aclID), "show acl-plugin acl")
	case "vpp.behavior.rate":
		aclID := stableID("flow-acl-rate:"+resource, 10000, 49999)
		commands := flowDetachPolicerCommands(target, policerName)
		commands = append(commands, flowDetachACLCommands(target, aclID)...)
		return append(commands, fmt.Sprintf("?policer del name %s", policerName), fmt.Sprintf("?delete acl-plugin acl index %d", aclID), fmt.Sprintf("?show policer name %s", policerName), "show acl-plugin acl")
	case "vpp.qos.classify":
		return []string{fmt.Sprintf("?qos record ip %s disable", interfaceName), fmt.Sprintf("?qos store ip %s disable", interfaceName), fmt.Sprintf("show qos record %s", interfaceName), fmt.Sprintf("show qos store %s", interfaceName)}
	case "vpp.qos.record":
		return []string{fmt.Sprintf("?qos record ip %s disable", interfaceName), fmt.Sprintf("show qos record %s", interfaceName)}
	case "vpp.qos.store":
		return []string{fmt.Sprintf("?qos store ip %s disable", interfaceName), fmt.Sprintf("show qos store %s", interfaceName)}
	case "vpp.qos.egress-map":
		return []string{fmt.Sprintf("?qos egress map delete id %d", mapID), fmt.Sprintf("show qos egress map id %d", mapID)}
	case "vpp.qos.mark":
		return []string{fmt.Sprintf("?qos mark ip %s disable", interfaceName), fmt.Sprintf("?qos egress map delete id %d", mapID), fmt.Sprintf("show qos mark %s", interfaceName), fmt.Sprintf("show qos egress map id %d", mapID)}
	case "vpp.policer":
		return []string{fmt.Sprintf("?policer del name %s", policerName), fmt.Sprintf("show policer name %s", policerName)}
	default:
		return nil
	}
}

func managementLCPCleanupCommands() []string {
	return []string{"show lcp"}
}

func dnsTransparentCleanupCommands(interception DNSTransparentInterception) []string {
	return []string{
		"show abf attach",
		fmt.Sprintf("show abf attach %s", interception.LANInterface),
		fmt.Sprintf("show abf policy %d", stableID("dns-transparent-v4", 9000, 999)),
		fmt.Sprintf("show abf policy %d", stableID("dns-transparent-v6", 9000, 999)),
		"show acl-plugin acl",
	}
}
