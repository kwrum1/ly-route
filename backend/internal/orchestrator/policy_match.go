package orchestrator

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

func parsePolicyMatch(input PolicyMatchInput, objects map[string]policyIPObject) (policyMatch, error) {
	sources, err := parseIPSelector(input.Sources, objects)
	if err != nil {
		return policyMatch{}, fmt.Errorf("sources: %w", err)
	}
	destinations, err := parseIPSelector(input.Destinations, objects)
	if err != nil {
		return policyMatch{}, fmt.Errorf("destinations: %w", err)
	}
	if !validProtocol(input.Protocol) {
		return policyMatch{}, fmt.Errorf("%w: protocol %q", ErrInvalidPolicyMatch, input.Protocol)
	}
	if len(input.SourcePorts)+len(input.DestinationPorts) > 0 && input.Protocol != ProtocolTCP && input.Protocol != ProtocolUDP {
		return policyMatch{}, fmt.Errorf("%w: protocol %q cannot match ports", ErrInvalidPolicyMatch, input.Protocol)
	}
	sourcePorts, err := parsePortRanges(input.SourcePorts)
	if err != nil {
		return policyMatch{}, err
	}
	destinationPorts, err := parsePortRanges(input.DestinationPorts)
	if err != nil {
		return policyMatch{}, err
	}
	return policyMatch{sources: sources, destinations: destinations, protocol: input.Protocol, sourcePorts: sourcePorts, destinationPorts: destinationPorts}, nil
}

func parseIPSelector(raw []string, objects map[string]policyIPObject) (ipSelector, error) {
	if len(raw) == 0 {
		return ipSelector{}, ErrInvalidPolicyMatch
	}
	if len(raw) == 1 && strings.TrimSpace(raw[0]) == "any" {
		return ipSelector{any: true}, nil
	}
	references := make([]policyName, 0, len(raw))
	prefixes := make([]netip.Prefix, 0)
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		id := strings.TrimSpace(value)
		if id == "any" || id == "" {
			return ipSelector{}, ErrInvalidPolicyMatch
		}
		object, exists := objects[id]
		if !exists {
			return ipSelector{}, fmt.Errorf("%w: IP object %q", ErrDeletedPolicyReference, id)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		references = append(references, object.id)
		prefixes = append(prefixes, object.prefixes...)
	}
	slices.SortFunc(references, func(left, right policyName) int { return strings.Compare(left.String(), right.String()) })
	return ipSelector{references: references, prefixes: prefixes}, nil
}

func parsePortRanges(inputs []PortRangeInput) ([]portRange, error) {
	ranges := make([]portRange, 0, len(inputs))
	seen := make(map[portRange]struct{}, len(inputs))
	for _, input := range inputs {
		if input.Start == 0 || input.End == 0 || input.Start > input.End {
			return nil, fmt.Errorf("%w: port range %d-%d", ErrInvalidPolicyMatch, input.Start, input.End)
		}
		parsed := portRange{start: input.Start, end: input.End}
		if _, exists := seen[parsed]; exists {
			continue
		}
		seen[parsed] = struct{}{}
		ranges = append(ranges, parsed)
	}
	slices.SortFunc(ranges, func(left, right portRange) int {
		if left.start != right.start {
			return int(left.start) - int(right.start)
		}
		return int(left.end) - int(right.end)
	})
	return ranges, nil
}

func parsePolicyAction(input ActionInput, groups map[string]InterfaceName) (policyAction, error) {
	switch input.Kind {
	case ActionVia:
		group, exists := groups[strings.TrimSpace(input.Group)]
		if strings.TrimSpace(input.Group) == "" {
			return policyAction{}, fmt.Errorf("%w: via requires group", ErrInvalidPolicyAction)
		}
		if !exists {
			return policyAction{}, fmt.Errorf("%w: orchestration group %q", ErrDeletedPolicyReference, input.Group)
		}
		return policyAction{kind: ActionVia, group: group}, nil
	case ActionDirect, ActionDrop:
		if strings.TrimSpace(input.Group) != "" {
			return policyAction{}, fmt.Errorf("%w: %s cannot include group", ErrInvalidPolicyAction, input.Kind)
		}
		return policyAction{kind: input.Kind}, nil
	default:
		return policyAction{}, fmt.Errorf("%w: %q", ErrInvalidPolicyAction, input.Kind)
	}
}

func ParseFlow(input FlowInput) (PolicyFlow, error) {
	source, err := netip.ParseAddr(strings.TrimSpace(input.SourceIP))
	if err != nil {
		return PolicyFlow{}, fmt.Errorf("%w: source IP", ErrInvalidPolicyFlow)
	}
	destination, err := netip.ParseAddr(strings.TrimSpace(input.DestinationIP))
	if err != nil {
		return PolicyFlow{}, fmt.Errorf("%w: destination IP", ErrInvalidPolicyFlow)
	}
	if input.Protocol == ProtocolAny || !validProtocol(input.Protocol) {
		return PolicyFlow{}, fmt.Errorf("%w: protocol %q", ErrInvalidPolicyFlow, input.Protocol)
	}
	hasPorts := input.SourcePort != 0 || input.DestinationPort != 0
	if hasPorts != (input.Protocol == ProtocolTCP || input.Protocol == ProtocolUDP) {
		return PolicyFlow{}, fmt.Errorf("%w: protocol and ports disagree", ErrInvalidPolicyFlow)
	}
	return PolicyFlow{sourceIP: source, destinationIP: destination, protocol: input.Protocol, sourcePort: input.SourcePort, destinationPort: input.DestinationPort, parsed: true}, nil
}

func validProtocol(protocol Protocol) bool {
	switch protocol {
	case ProtocolAny, ProtocolTCP, ProtocolUDP, ProtocolICMP, ProtocolICMPv6:
		return true
	default:
		return false
	}
}

func (match policyMatch) matches(flow PolicyFlow) bool {
	return match.sources.matches(flow.sourceIP) && match.destinations.matches(flow.destinationIP) && (match.protocol == ProtocolAny || match.protocol == flow.protocol) && portsMatch(match.sourcePorts, flow.sourcePort) && portsMatch(match.destinationPorts, flow.destinationPort)
}

func (selector ipSelector) matches(address netip.Addr) bool {
	if selector.any {
		return true
	}
	for _, prefix := range selector.prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func portsMatch(ranges []portRange, port uint16) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, item := range ranges {
		if port >= item.start && port <= item.end {
			return true
		}
	}
	return false
}
