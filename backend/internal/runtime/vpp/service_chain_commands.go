package vpp

import (
	"fmt"
	"net/netip"
	"strconv"

	"ly-route/backend/internal/orchestrator"
)

func serviceChainPolicyCommands(policy ServiceChainPolicy) []string {
	return []string{
		fmt.Sprintf("?set acl-plugin acl index %d permit src %s dst %s proto %d sport %s dport %s tag %s", policy.ACLID, hostPrefix(policy.Match.SourceIP), hostPrefix(policy.Match.DestinationIP), serviceChainProtocol(policy.Match.Protocol), serviceChainPort(policy.Match.SourcePort), serviceChainPort(policy.Match.DestinationPort), serviceChainACLTag(policy)),
		fmt.Sprintf("?abf policy add id %d acl %d via %s %s", policy.PolicyID, policy.ACLID, policy.NextHop, policy.ServiceInterface),
		fmt.Sprintf("?abf attach %s policy %d priority %d %s", policy.AddressFamily, policy.PolicyID, policy.Priority, policy.IngressInterface),
		fmt.Sprintf("show acl-plugin acl index %d", policy.ACLID),
		policyShowCommand(policy),
		attachmentShowCommand(policy),
		"show interface " + policy.IngressInterface,
	}
}

func serviceChainACLCommand(policy ServiceChainPolicy, replace bool) string {
	index := ""
	if replace {
		index = fmt.Sprintf(" index %d", policy.ACLID)
	}
	return fmt.Sprintf("set acl-plugin acl%s permit src %s dst %s proto %d sport %s dport %s tag %s", index, hostPrefix(policy.Match.SourceIP), hostPrefix(policy.Match.DestinationIP), serviceChainProtocol(policy.Match.Protocol), serviceChainPort(policy.Match.SourcePort), serviceChainPort(policy.Match.DestinationPort), serviceChainACLTag(policy))
}

func serviceChainACLTag(policy ServiceChainPolicy) string {
	return "ly-route-" + safeTag(fmt.Sprintf("%s-%s-%d", policy.ChainID, policy.Direction, policy.Position))
}

func policyShowCommand(policy ServiceChainPolicy) string {
	return fmt.Sprintf("show abf policy %d", policy.PolicyID)
}

func attachmentShowCommand(policy ServiceChainPolicy) string {
	return "show abf attach " + policy.IngressInterface
}

func hostPrefix(raw string) string {
	address, err := netip.ParseAddr(raw)
	if err != nil {
		return raw
	}
	return netip.PrefixFrom(address, address.BitLen()).String()
}

func serviceChainProtocol(protocol orchestrator.Protocol) int {
	switch protocol {
	case orchestrator.ProtocolTCP:
		return 6
	case orchestrator.ProtocolUDP:
		return 17
	case orchestrator.ProtocolICMP:
		return 1
	case orchestrator.ProtocolICMPv6:
		return 58
	case orchestrator.ProtocolAny:
		return 0
	default:
		return 0
	}
}

func serviceChainPort(port uint16) string {
	if port == 0 {
		return "0-65535"
	}
	value := strconv.Itoa(int(port))
	return value + "-" + value
}
