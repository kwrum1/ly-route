package vpp

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

type preNATPortRange struct {
	first uint16
	last  uint16
}

// lanDHCPBroadcastPreNATRuleID reserves the earliest pre-NAT decision for all
// DHCPv4 client requests. A catch-all policy route runs in this feature arc
// before ABF and must never steer Discover, Request, or Renew traffic into a
// WAN or proxy table.
const lanDHCPBroadcastPreNATRuleID = 65000

const lanLocalPreNATRuleID = 64999

// preNATRoutePolicyCommands reads the data-LAN prefix directly from VPP. The
// control API's LAN_CIDR value belongs to the management plane and is not a
// reliable source when management and data ports are configured separately.
func (channel vppctlChannel) preNATRoutePolicyCommands(ctx context.Context, operation Operation, spec routePolicyVPPCTLSpec) ([]string, bool, error) {
	policy := spec.policy
	if !spec.apply || policy.Action == "deny" || policy.Path == nil {
		return nil, false, nil
	}
	results, err := channel.runServiceChainCommands(ctx, operation, "show interface address "+spec.ingress)
	if err != nil {
		return nil, false, err
	}
	lanPrefix, err := preNATLANPrefix(resultStdoutLast(results, "show interface address "+spec.ingress))
	if err != nil {
		return nil, false, snapshotDecodeError("route policy %q cannot install pre-NAT classifier: %v", policy.ID, err)
	}
	return buildPreNATRoutePolicyCommands(spec, lanPrefix)
}

// buildPreNATRoutePolicyCommands mirrors the declarative route-policy match
// in a small VPP feature that runs before IPv4 NAT. The existing ABF operation
// is retained for readback and compatibility; it cannot be the first
// classifier because VPP's standard feature arc rewrites the source address
// first.
func buildPreNATRoutePolicyCommands(spec routePolicyVPPCTLSpec, lanPrefix netip.Prefix) ([]string, bool, error) {
	policy := spec.policy
	if !spec.apply || policy.Action == "deny" {
		return nil, false, nil
	}
	// A WAN-group policy deliberately has no single WANPath: its private FIB
	// resolves through the group table. It is still a valid native pre-NAT
	// policy and must not be mistaken for a deletion.
	skipNAT := policy.Path != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(policy.Path.VPPInterface)), "lypxin")
	return buildPreNATRoutePolicyCommandsForTable(policy, spec.policyID, spec.tableID, spec.ingress, lanPrefix, skipNAT)
}

func buildPreNATRoutePolicyCommandsForTable(policy trafficpolicy.RoutePolicy, policyID, tableID int, ingress string, lanPrefix netip.Prefix, skipNAT bool) ([]string, bool, error) {
	if strings.EqualFold(strings.TrimSpace(policy.Action), "deny") {
		return nil, false, nil
	}
	if !lanPrefix.Addr().Is4() {
		return nil, false, snapshotDecodeError("route policy %q has no IPv4 LAN prefix", policy.ID)
	}
	lanPrefix = lanPrefix.Masked()

	sources, err := preNATIPv4Selectors(policy.Match.Sources)
	if err != nil {
		return nil, false, fmt.Errorf("route policy %q source selector: %w", policy.ID, err)
	}
	destinations, err := preNATIPv4Selectors(policy.Match.Destinations)
	if err != nil {
		return nil, false, fmt.Errorf("route policy %q destination selector: %w", policy.ID, err)
	}
	if len(sources) == 0 || len(destinations) == 0 {
		// An IPv6-only policy remains on its existing IPv6 path.
		return nil, false, nil
	}
	protocols := preNATProtocols(policy.Match.Protocols)
	sourcePorts, err := preNATPortRanges(policy.Match.SourcePorts)
	if err != nil {
		return nil, false, fmt.Errorf("route policy %q source port: %w", policy.ID, err)
	}
	destinationPorts, err := preNATPortRanges(policy.Match.DestPorts)
	if err != nil {
		return nil, false, fmt.Errorf("route policy %q destination port: %w", policy.ID, err)
	}

	skipNATArgument := ""
	if skipNAT {
		skipNATArgument = " skip-nat"
	}
	commands := []string{
		fmt.Sprintf("set ly-route pre-nat-route interface %s lan-prefix %s", ingress, lanPrefix),
		fmt.Sprintf("?set ly-route pre-nat-route del id %d", lanDHCPBroadcastPreNATRuleID),
		fmt.Sprintf("set ly-route pre-nat-route add id %d priority 0 source 0.0.0.0/0 destination 0.0.0.0/0 protocol udp sport 68-68 dport 67-67 bypass", lanDHCPBroadcastPreNATRuleID),
		fmt.Sprintf("?set ly-route pre-nat-route del id %d", lanLocalPreNATRuleID),
		fmt.Sprintf("set ly-route pre-nat-route add id %d priority 1 source %s destination %s protocol any sport 0-65535 dport 0-65535 bypass", lanLocalPreNATRuleID, lanPrefix, lanPrefix),
		fmt.Sprintf("set ly-route pre-nat-route del id %d", policyID),
	}
	additions := make([]string, 0, len(sources)*len(destinations)*len(protocols)*len(sourcePorts)*len(destinationPorts))
	for _, source := range sources {
		for _, destination := range destinations {
			if destination == "0.0.0.0/0" && lanPrefix.String() != "0.0.0.0/0" {
				// The plugin excludes the LAN prefix globally, matching the
				// deny-before-permit behavior of the generated ABF ACL.
			}
			for _, protocol := range protocols {
				for _, sourcePort := range sourcePorts {
					for _, destinationPort := range destinationPorts {
						additions = append(additions, fmt.Sprintf(
							"set ly-route pre-nat-route add id %d priority %d source %s destination %s protocol %s sport %d-%d dport %d-%d table %d%s",
							policyID, vppABFPriority(policy.Priority), source, destination, protocol,
							sourcePort.first, sourcePort.last,
							destinationPort.first, destinationPort.last, tableID, skipNATArgument))
					}
				}
			}
		}
	}
	if len(additions) > 0 {
		commands = append(commands, vppRouteBatchBegin)
		commands = append(commands, additions...)
		commands = append(commands, vppRouteBatchEnd)
	}
	commands = append(commands, "show ly-route pre-nat-route")
	return commands, true, nil
}

func preNATLANPrefix(output string) (netip.Prefix, error) {
	for _, field := range strings.Fields(output) {
		prefix, err := netip.ParsePrefix(strings.TrimRight(field, ","))
		if err == nil && prefix.Addr().Is4() {
			return prefix.Masked(), nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("no IPv4 address found in VPP interface output %q", strings.TrimSpace(output))
}

func preNATProtocols(protocols []string) []string {
	if len(protocols) == 0 {
		return []string{"any"}
	}
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range protocols {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			value = "any"
		}
		if value == "tcp" || value == "udp" || value == "icmp" || value == "any" {
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				result = append(result, value)
			}
		}
	}
	if len(result) == 0 {
		return []string{"any"}
	}
	sort.Strings(result)
	return result
}

func preNATPortRanges(values []string) ([]preNATPortRange, error) {
	if len(values) == 0 {
		return []preNATPortRange{{first: 0, last: 65535}}, nil
	}
	result := []preNATPortRange{}
	seen := map[preNATPortRange]struct{}{}
	for _, raw := range values {
		raw = strings.TrimSpace(strings.ToLower(raw))
		if raw == "" || raw == "any" || raw == "0-65535" {
			raw = "0-65535"
		}
		parts := strings.Split(raw, "-")
		if len(parts) > 2 {
			return nil, fmt.Errorf("invalid port range %q", raw)
		}
		first, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || first < 0 || first > 65535 {
			return nil, fmt.Errorf("invalid port range %q", raw)
		}
		last := first
		if len(parts) == 2 {
			last, err = strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil || last < first || last > 65535 {
				return nil, fmt.Errorf("invalid port range %q", raw)
			}
		}
		rangeValue := preNATPortRange{first: uint16(first), last: uint16(last)}
		if _, ok := seen[rangeValue]; !ok {
			seen[rangeValue] = struct{}{}
			result = append(result, rangeValue)
		}
	}
	return result, nil
}

func preNATIPv4Selectors(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{"0.0.0.0/0"}, nil
	}
	result := []string{}
	seen := map[string]struct{}{}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.EqualFold(raw, "any") {
			raw = "0.0.0.0/0"
		}
		var prefixes []netip.Prefix
		if strings.Contains(raw, "-") {
			parts := strings.Split(raw, "-")
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid IP range %q", raw)
			}
			start, startErr := netip.ParseAddr(strings.TrimSpace(parts[0]))
			end, endErr := netip.ParseAddr(strings.TrimSpace(parts[1]))
			if startErr != nil || endErr != nil || !start.Is4() || !end.Is4() {
				return nil, fmt.Errorf("invalid IPv4 range %q", raw)
			}
			prefixes = ipv4RangePrefixes(start, end)
		} else if prefix, err := netip.ParsePrefix(raw); err == nil {
			if prefix.Addr().Is4() {
				prefixes = []netip.Prefix{prefix.Masked()}
			}
		} else if address, err := netip.ParseAddr(raw); err == nil && address.Is4() {
			prefixes = []netip.Prefix{netip.PrefixFrom(address, 32)}
		} else if strings.Contains(raw, ":") {
			// IPv6 route policies use the existing ip6 ABF path.
			continue
		} else {
			return nil, fmt.Errorf("invalid IP selector %q", raw)
		}
		for _, prefix := range prefixes {
			value := prefix.String()
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				result = append(result, value)
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func ipv4RangePrefixes(start, end netip.Addr) []netip.Prefix {
	startValue := netipIPv4Value(start)
	endValue := netipIPv4Value(end)
	if startValue > endValue {
		return nil
	}
	result := []netip.Prefix{}
	for startValue <= endValue {
		alignmentHostBits := uint(32)
		if startValue != 0 {
			alignmentHostBits = uint(bitsTrailingZeros32(startValue))
		}
		remainingHostBits := uint(bitsFloorLog2(endValue - startValue + 1))
		if alignmentHostBits > remainingHostBits {
			alignmentHostBits = remainingHostBits
		}
		prefixLen := 32 - int(alignmentHostBits)
		address := netip.AddrFrom4([4]byte{byte(startValue >> 24), byte(startValue >> 16), byte(startValue >> 8), byte(startValue)})
		result = append(result, netip.PrefixFrom(address, prefixLen).Masked())
		blockSize := uint64(1) << alignmentHostBits
		if blockSize == 0 || uint64(startValue)+blockSize > uint64(^uint32(0)) {
			break
		}
		startValue += uint32(blockSize)
	}
	return result
}

func netipIPv4Value(address netip.Addr) uint32 {
	bytes := address.As4()
	return uint32(bytes[0])<<24 | uint32(bytes[1])<<16 | uint32(bytes[2])<<8 | uint32(bytes[3])
}

func bitsTrailingZeros32(value uint32) int {
	count := 0
	for value&1 == 0 {
		count++
		value >>= 1
	}
	return count
}

func bitsFloorLog2(value uint32) int {
	result := -1
	for value != 0 {
		result++
		value >>= 1
	}
	return result
}
