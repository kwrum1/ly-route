package vpp

import (
	"context"
	"fmt"
	"strings"

	"ly-route/backend/internal/orchestrator"
)

func (channel vppctlChannel) doManagementLCPLifecycle(ctx context.Context, operation Operation, management ManagementLCP) (Reply, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, "show lcp")
	if err != nil {
		return Reply{}, err
	}
	present := false
	for _, pair := range managementLCPPairs(resultStdout(results, "show lcp"), management.HostInterface) {
		if management.Enabled && pair == management.VPPInterface {
			present = true
			continue
		}
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("lcp delete %s", pair))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	if management.Enabled {
		commands := []string{"lcp lcp-sync on"}
		if !present {
			commands = append(commands, fmt.Sprintf("lcp create %s host-if %s", management.VPPInterface, management.HostInterface))
		}
		if management.IPv4BroadcastLocal {
			commands = append(commands, "ip route add 255.255.255.255/32 via local", "show ip fib 255.255.255.255")
		}
		commands = append(commands, "show lcp")
		applied, applyErr := channel.runServiceChainCommands(ctx, operation, commands...)
		if applyErr != nil {
			return Reply{}, applyErr
		}
		results = append(results, applied...)
		if !managementLCPPresent(resultStdoutLast(results, "show lcp"), management.VPPInterface, management.HostInterface) {
			return Reply{}, snapshotDecodeError("Linux control-plane LCP pair %s/%s is absent from readback", management.VPPInterface, management.HostInterface)
		}
		if management.IPv4BroadcastLocal && !ipv4BroadcastLocalRoutePresent(resultStdoutLast(results, "show ip fib 255.255.255.255")) {
			return Reply{}, snapshotDecodeError("IPv4 limited broadcast route is absent from Linux control-plane readback")
		}
		if management.IPv4DHCPBroadcastBypass {
			bypass := lanDHCPBroadcastBypassPolicy(management.VPPInterface)
			bypassOperation := Operation{
				Name:      "vpp.lan-dhcp-bypass",
				RequestID: operation.RequestID,
				Resource:  operation.Resource,
				Payload:   bypass,
			}
			bypassReply, bypassErr := channel.doServiceChainApply(ctx, bypassOperation, bypass)
			if bypassErr != nil {
				return Reply{}, bypassErr
			}
			if payload, ok := bypassReply.Payload.(VPPCTLReplyPayload); ok {
				results = append(results, payload.CommandResults...)
			}
		}
		return routePolicyLifecycleReply(operation, results), nil
	}
	if management.IPv4DHCPBroadcastBypass {
		bypass := lanDHCPBroadcastBypassPolicy(management.VPPInterface)
		bypassOperation := Operation{
			Name:      "vpp.lan-dhcp-bypass.rollback-delete",
			RequestID: operation.RequestID,
			Resource:  operation.Resource,
			Payload:   bypass,
		}
		bypassReply, bypassErr := channel.doServiceChainDelete(ctx, bypassOperation, bypass)
		if bypassErr != nil {
			return Reply{}, bypassErr
		}
		if payload, ok := bypassReply.Payload.(VPPCTLReplyPayload); ok {
			results = append(results, payload.CommandResults...)
		}
	}
	verified, verifyErr := channel.runServiceChainCommands(ctx, operation, "show lcp")
	if verifyErr != nil {
		return Reply{}, verifyErr
	}
	results = append(results, verified...)
	if len(managementLCPPairs(resultStdoutLast(results, "show lcp"), management.HostInterface)) != 0 {
		return Reply{}, snapshotDecodeError("exclusive management left LCP host interface %s attached", management.HostInterface)
	}
	return routePolicyLifecycleReply(operation, results), nil
}

const lanDHCPBroadcastBypassPolicyID = 65000

// lanDHCPBroadcastBypassPolicy is a reserved, receive-only ABF policy.  A
// DHCP client sends its initial discover to 255.255.255.255 before it has an
// address or a route.  User route policies must never capture that packet;
// the local ABF path hands it to the Linux LCP where Kea can answer it.
func lanDHCPBroadcastBypassPolicy(ingress string) ServiceChainPolicy {
	return ServiceChainPolicy{
		ChainID:          "lan-dhcp-broadcast",
		Direction:        orchestrator.ServiceChainForward,
		Position:         0,
		Group:            "lan-control",
		PolicyID:         lanDHCPBroadcastBypassPolicyID,
		Priority:         1,
		AddressFamily:    "ip4",
		IngressInterface: strings.TrimSpace(ingress),
		NextHop:          "local",
		Match: orchestrator.FlowTuple{
			SourceIP:        "0.0.0.0",
			DestinationIP:   "255.255.255.255",
			Protocol:        orchestrator.ProtocolUDP,
			DestinationPort: 67,
		},
	}
}

func ipv4BroadcastLocalRoutePresent(output string) bool {
	return strings.Contains(output, "255.255.255.255/32") && strings.Contains(output, "dpo-receive") && strings.Contains(output, "local0")
}

func managementLCPPairs(output, hostInterface string) []string {
	pairs := []string{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "itf-pair:" {
			continue
		}
		for _, field := range fields[2:] {
			if field == hostInterface {
				pairs = append(pairs, fields[2])
				break
			}
		}
	}
	return pairs
}

func managementLCPPresent(output, vppInterface, hostInterface string) bool {
	for _, pair := range managementLCPPairs(output, hostInterface) {
		if pair == vppInterface {
			return true
		}
	}
	return false
}
