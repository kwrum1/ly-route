package vpp

import (
	"bufio"
	"context"
	"fmt"
	"strings"
)

const dnsIPv6TableID = 101

func (channel vppctlChannel) doDNSTransparentLifecycle(ctx context.Context, operation Operation, interception DNSTransparentInterception) (Reply, error) {
	v4Policy := stableID("dns-transparent-v4", 9000, 999)
	v6Policy := stableID("dns-transparent-v6", 9000, 999)
	initial, err := channel.runServiceChainCommands(ctx, operation, "show abf attach", fmt.Sprintf("show abf policy %d", v4Policy), fmt.Sprintf("show abf policy %d", v6Policy), "show acl-plugin acl")
	if err != nil {
		return Reply{}, err
	}
	results := append([]VPPCTLCommandResult(nil), initial...)
	attachOutput := resultStdout(initial, "show abf attach")
	v4PolicyOutput := resultStdout(initial, fmt.Sprintf("show abf policy %d", v4Policy))
	v6PolicyOutput := resultStdout(initial, fmt.Sprintf("show abf policy %d", v6Policy))
	v4ACLID, v4Present := observedABFACLID(v4PolicyOutput)
	v6ACLID, v6Present := observedABFACLID(v6PolicyOutput)
	v4Attachments := dnsPolicyAttachments(attachOutput, v4Policy, "ip4")
	v6Attachments := dnsPolicyAttachments(attachOutput, v6Policy, "ip6")
	if v4Present {
		v4Attachments = appendUniqueAttachment(v4Attachments, interception.LANInterface)
	}
	if v6Present {
		v6Attachments = appendUniqueAttachment(v6Attachments, interception.LANInterface)
	}
	for _, attachment := range v4Attachments {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("abf attach ip4 del policy %d %s", v4Policy, attachment))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	for _, attachment := range v6Attachments {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("abf attach ip6 del policy %d %s", v6Policy, attachment))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	if v4Present {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("abf policy del id %d acl %d via local", v4Policy, v4ACLID))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	if v6Present {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("abf policy del id %d acl %d via ip6-lookup-in-table %d", v6Policy, v6ACLID, dnsIPv6TableID))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	for _, tag := range []string{"ly-route-dns-transparent-v4", "ly-route-dns-transparent-v6"} {
		for _, id := range taggedServiceChainACLIDs(resultStdout(initial, "show acl-plugin acl"), tag) {
			removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("delete acl-plugin acl index %d", id))
			if removeErr != nil {
				return Reply{}, removeErr
			}
			results = append(results, removed...)
		}
	}

	v4Created, err := channel.runServiceChainCommands(ctx, operation, "set acl-plugin acl permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 17 sport 0-65535 dport 53-53, permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 53-53 tag ly-route-dns-transparent-v4")
	if err != nil {
		return Reply{}, err
	}
	v4ACL, err := allocatedServiceChainACLID(v4Created)
	if err != nil {
		return Reply{}, err
	}
	results = append(results, v4Created...)
	baseCommands := []string{
		fmt.Sprintf("abf policy add id %d acl %d via local", v4Policy, v4ACL),
		fmt.Sprintf("abf attach ip4 policy %d priority 0 %s", v4Policy, interception.LANInterface),
		fmt.Sprintf("ip6 table add %d", dnsIPv6TableID),
		fmt.Sprintf("ip route add table %d ::/0 via local", dnsIPv6TableID),
	}
	for _, prefix := range interception.IPv6Prefixes {
		baseCommands = append(baseCommands, fmt.Sprintf("ip route add table %d %s via %s", dnsIPv6TableID, prefix, interception.LANInterface))
	}
	applied, err := channel.runServiceChainCommands(ctx, operation, baseCommands...)
	if err != nil {
		return Reply{}, err
	}
	results = append(results, applied...)

	v6Created, err := channel.runServiceChainCommands(ctx, operation, "set acl-plugin acl permit src ::/0 dst ::/0 proto 17 sport 0-65535 dport 53-53, permit src ::/0 dst ::/0 proto 6 sport 0-65535 dport 53-53 tag ly-route-dns-transparent-v6")
	if err != nil {
		return Reply{}, err
	}
	v6ACL, err := allocatedServiceChainACLID(v6Created)
	if err != nil {
		return Reply{}, err
	}
	results = append(results, v6Created...)
	v6Applied, err := channel.runServiceChainCommands(ctx, operation,
		fmt.Sprintf("abf policy add id %d acl %d via ip6-lookup-in-table %d", v6Policy, v6ACL, dnsIPv6TableID),
		fmt.Sprintf("abf attach ip6 policy %d priority 0 %s", v6Policy, interception.LANInterface),
		fmt.Sprintf("show abf policy %d", v4Policy), fmt.Sprintf("show abf policy %d", v6Policy),
		fmt.Sprintf("show abf attach %s", interception.LANInterface), "show acl-plugin acl", fmt.Sprintf("show ip fib table %d", dnsIPv6TableID))
	if err != nil {
		return Reply{}, err
	}
	results = append(results, v6Applied...)
	if observed, ok := observedABFACLID(resultStdoutLast(results, fmt.Sprintf("show abf policy %d", v4Policy))); !ok || observed != v4ACL {
		return Reply{}, snapshotDecodeError("transparent DNS IPv4 ABF readback references ACL %d, want %d", observed, v4ACL)
	}
	if observed, ok := observedABFACLID(resultStdoutLast(results, fmt.Sprintf("show abf policy %d", v6Policy))); !ok || observed != v6ACL {
		return Reply{}, snapshotDecodeError("transparent DNS IPv6 ABF readback references ACL %d, want %d", observed, v6ACL)
	}
	attachReadback := resultStdoutLast(results, fmt.Sprintf("show abf attach %s", interception.LANInterface))
	if !routePolicyAttached(attachReadback, v4Policy) || !routePolicyAttached(attachReadback, v6Policy) {
		return Reply{}, snapshotDecodeError("transparent DNS ABF attachment readback is incomplete")
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func appendUniqueAttachment(attachments []string, name string) []string {
	for _, attachment := range attachments {
		if attachment == name {
			return attachments
		}
	}
	return append(attachments, name)
}

func dnsPolicyAttachments(output string, policyID int, family string) []string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	currentInterface, currentFamily := "", ""
	attachments := []string{}
	seen := map[string]bool{}
	needle := fmt.Sprintf("policy:%d ", policyID)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "ipv4:":
			currentFamily = "ip4"
			continue
		case "ipv6:":
			currentFamily = "ip6"
			continue
		}
		if line == trimmed && trimmed != "" {
			currentInterface, currentFamily = trimmed, ""
			continue
		}
		if currentFamily == family && currentInterface != "" && strings.Contains(trimmed, needle) && !seen[currentInterface] {
			seen[currentInterface] = true
			attachments = append(attachments, currentInterface)
		}
	}
	return attachments
}
