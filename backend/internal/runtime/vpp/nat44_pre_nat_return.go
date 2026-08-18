package vpp

import (
	"fmt"
	"net/netip"
	"strings"
)

func (guard natReturnGuard) preNATBypassCommand() (string, error) {
	address, err := netip.ParseAddr(strings.TrimSpace(guard.internalAddress))
	if err != nil || !address.Is4() {
		return "", snapshotDecodeError("NAT return guard %q has invalid internal IPv4 address %q", guard.resource, guard.internalAddress)
	}
	protocol := strings.ToLower(strings.TrimSpace(guard.protocol))
	if protocol == "" {
		protocol = "any"
	}
	if protocol != "any" && protocol != "tcp" && protocol != "udp" {
		return "", snapshotDecodeError("NAT return guard %q has invalid protocol %q", guard.resource, guard.protocol)
	}
	first, last := 0, 65535
	if guard.internalPort != 0 {
		if guard.internalPort < 1 || guard.internalPort > 65535 {
			return "", snapshotDecodeError("NAT return guard %q has invalid internal port %d", guard.resource, guard.internalPort)
		}
		first, last = guard.internalPort, guard.internalPort
	}
	return fmt.Sprintf(
		"set ly-route pre-nat-route add id %d priority 0 source %s/32 destination 0.0.0.0/0 protocol %s sport %d-%d dport 0-65535 bypass",
		guard.policyID(), address, protocol, first, last,
	), nil
}

func (guard natReturnGuard) preNATBypassDeleteCommand() string {
	return fmt.Sprintf("?set ly-route pre-nat-route del id %d", guard.policyID())
}

func verifyNATPreRouteBypass(output string, guard natReturnGuard) error {
	want := fmt.Sprintf("rule id %d ", guard.policyID())
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, want) && strings.Contains(line, " bypass 1") {
			return nil
		}
	}
	return natReturnGuardDrift(snapshotDecodeError("NAT return guard %q pre-NAT bypass is missing", guard.resource))
}
