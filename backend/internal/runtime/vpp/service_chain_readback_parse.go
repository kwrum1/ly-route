package vpp

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"ly-route/backend/internal/orchestrator"
)

type observedServiceChainACL struct {
	ID    int
	Tag   string
	Match orchestrator.FlowTuple
}

type observedServiceChainABFPolicy struct {
	ID               int
	ACLID            int
	AddressFamily    string
	NextHop          string
	ServiceInterface string
}

type observedServiceChainAttachment struct {
	AddressFamily string
	PolicyID      int
	Priority      int
}

func parseObservedServiceChainACL(output string) (observedServiceChainACL, error) {
	lines := nonBlankLines(output)
	if len(lines) < 2 {
		return observedServiceChainACL{}, serviceChainDecodeError("ACL output contains %d lines, want at least 2", len(lines))
	}
	for _, line := range lines[2:] {
		if !strings.HasPrefix(strings.TrimSpace(line), "used in lookup context index:") {
			return observedServiceChainACL{}, serviceChainDecodeError("ACL output contains unknown trailing state %q", line)
		}
	}
	header := strings.Fields(lines[0])
	if len(header) != 6 || header[0] != "acl-index" || header[2] != "count" || header[3] != "1" || header[4] != "tag" {
		return observedServiceChainACL{}, serviceChainDecodeError("ACL identity header %q", lines[0])
	}
	aclID, err := strconv.Atoi(header[1])
	if err != nil {
		return observedServiceChainACL{}, serviceChainDecodeError("ACL identity %q", header[1])
	}
	tag := strings.TrimSuffix(strings.TrimPrefix(header[5], "{"), "}")
	match, err := parseObservedServiceChainACLRule(lines[1])
	if err != nil {
		return observedServiceChainACL{}, err
	}
	return observedServiceChainACL{ID: aclID, Tag: tag, Match: match}, nil
}

func parseObservedServiceChainACLRule(line string) (orchestrator.FlowTuple, error) {
	fields := strings.Fields(line)
	if len(fields) != 13 || fields[0] != "0:" || fields[2] != "permit" || fields[3] != "src" || fields[5] != "dst" || fields[7] != "proto" || fields[9] != "sport" || fields[11] != "dport" {
		return orchestrator.FlowTuple{}, serviceChainDecodeError("ACL tuple grammar %q", line)
	}
	family, err := parseObservedAddressFamily(fields[1])
	if err != nil {
		return orchestrator.FlowTuple{}, err
	}
	source, err := parseObservedHostPrefix(fields[4], family)
	if err != nil {
		return orchestrator.FlowTuple{}, err
	}
	destination, err := parseObservedHostPrefix(fields[6], family)
	if err != nil {
		return orchestrator.FlowTuple{}, err
	}
	protocol, err := parseObservedServiceChainProtocol(fields[8], family)
	if err != nil {
		return orchestrator.FlowTuple{}, err
	}
	sourcePort, err := parseObservedServiceChainPort(fields[10])
	if err != nil {
		return orchestrator.FlowTuple{}, err
	}
	destinationPort, err := parseObservedServiceChainPort(fields[12])
	if err != nil {
		return orchestrator.FlowTuple{}, err
	}
	return orchestrator.FlowTuple{SourceIP: source, DestinationIP: destination, Protocol: protocol, SourcePort: sourcePort, DestinationPort: destinationPort}, nil
}

func parseObservedServiceChainABFPolicy(output string) (observedServiceChainABFPolicy, error) {
	lines := nonBlankLines(output)
	if len(lines) < 2 {
		return observedServiceChainABFPolicy{}, serviceChainDecodeError("ABF policy output is truncated")
	}
	header := strings.Fields(lines[0])
	if len(header) != 3 || !strings.HasPrefix(header[0], "abf:[") {
		return observedServiceChainABFPolicy{}, serviceChainDecodeError("ABF policy identity header %q", lines[0])
	}
	policyID, err := parseObservedPrefixedInt(header[1], "policy:")
	if err != nil {
		return observedServiceChainABFPolicy{}, err
	}
	aclID, err := parseObservedPrefixedInt(header[2], "acl:")
	if err != nil {
		return observedServiceChainABFPolicy{}, err
	}
	observed := observedServiceChainABFPolicy{ID: policyID, ACLID: aclID}
	paths := 0
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "[@") || fields[2] != "via" {
			continue
		}
		family, familyErr := parseObservedAddressFamily(fields[1])
		if familyErr != nil {
			return observedServiceChainABFPolicy{}, familyErr
		}
		nextHop, addressErr := netip.ParseAddr(fields[3])
		if addressErr != nil {
			return observedServiceChainABFPolicy{}, serviceChainDecodeError("ABF path next hop %q", fields[3])
		}
		paths++
		observed.AddressFamily = family
		observed.NextHop = nextHop.String()
		observed.ServiceInterface = strings.TrimRight(fields[4], ",:")
	}
	if paths != 1 {
		return observedServiceChainABFPolicy{}, serviceChainDecodeError("ABF path count %d, want 1", paths)
	}
	return observed, nil
}

func parseObservedServiceChainAttachments(output string) ([]observedServiceChainAttachment, error) {
	var family string
	var attachments []observedServiceChainAttachment
	for _, line := range nonBlankLines(output) {
		switch line {
		case "ipv4:", "ipv6:":
			parsed, err := parseObservedAddressFamily(strings.TrimSuffix(line, ":"))
			if err != nil {
				return nil, err
			}
			family = parsed
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "abf-interface-attach:" {
			continue
		}
		if family == "" {
			return nil, serviceChainDecodeError("attachment family is missing")
		}
		policyID, err := parseObservedPrefixedInt(fields[1], "policy:")
		if err != nil {
			return nil, err
		}
		priority, err := parseObservedPrefixedInt(fields[2], "priority:")
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, observedServiceChainAttachment{AddressFamily: family, PolicyID: policyID, Priority: priority})
	}
	if len(attachments) == 0 {
		return nil, serviceChainDecodeError("attachment state is missing")
	}
	return attachments, nil
}

func parseObservedAddressFamily(raw string) (string, error) {
	switch raw {
	case "ipv4":
		return "ip4", nil
	case "ipv6":
		return "ip6", nil
	default:
		return "", serviceChainDecodeError("address family %q", raw)
	}
}

func parseObservedHostPrefix(raw, family string) (string, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || prefix.Bits() != prefix.Addr().BitLen() || (prefix.Addr().Is6() && family != "ip6") || (!prefix.Addr().Is6() && family != "ip4") {
		return "", serviceChainDecodeError("ACL tuple host prefix %q", raw)
	}
	return prefix.Addr().String(), nil
}

func parseObservedServiceChainProtocol(raw, family string) (orchestrator.Protocol, error) {
	switch raw {
	case "6":
		return orchestrator.ProtocolTCP, nil
	case "17":
		return orchestrator.ProtocolUDP, nil
	case "1":
		if family == "ip4" {
			return orchestrator.ProtocolICMP, nil
		}
	case "58":
		if family == "ip6" {
			return orchestrator.ProtocolICMPv6, nil
		}
	}
	return "", serviceChainDecodeError("ACL tuple protocol %q for %s", raw, family)
}

func parseObservedServiceChainPort(raw string) (uint16, error) {
	if raw == "0-65535" {
		return 0, nil
	}
	bounds := strings.Split(raw, "-")
	if len(bounds) == 1 {
		value, err := parseObservedServiceChainPortNumber(bounds[0])
		if err != nil {
			return 0, serviceChainDecodeError("ACL tuple port %q", raw)
		}
		return value, nil
	}
	if len(bounds) != 2 || bounds[0] != bounds[1] {
		return 0, serviceChainDecodeError("ACL tuple port %q", raw)
	}
	value, err := parseObservedServiceChainPortNumber(bounds[0])
	if err != nil {
		return 0, serviceChainDecodeError("ACL tuple port %q", raw)
	}
	return value, nil
}

func parseObservedServiceChainPortNumber(raw string) (uint16, error) {
	if raw == "" {
		return 0, strconv.ErrSyntax
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	value, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || value == 0 {
		return 0, strconv.ErrSyntax
	}
	return uint16(value), nil
}

func parseObservedPrefixedInt(raw, prefix string) (int, error) {
	if !strings.HasPrefix(raw, prefix) {
		return 0, serviceChainDecodeError("identity field %q", raw)
	}
	value, err := strconv.Atoi(strings.TrimPrefix(raw, prefix))
	if err != nil || value < 0 {
		return 0, serviceChainDecodeError("identity field %q", raw)
	}
	return value, nil
}

func serviceChainDecodeError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrServiceChainReadback, fmt.Sprintf(format, args...))
}
