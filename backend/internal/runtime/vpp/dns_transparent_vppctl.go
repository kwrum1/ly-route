package vpp

import (
	"context"
	"fmt"
	"strings"
)

const dnsIPv4TableID = 100

// The DNS intercept plugin sits before NAT and policy routing in VPP's IPv4
// feature arc. It already performs the TCP/UDP port-53 match, so rebuilding a
// parallel ABF/ACL graph is both redundant and unsafe on VPP 25.x. Keep this
// lifecycle limited to the dedicated local-service FIB and the native feature.
func (channel vppctlChannel) doDNSTransparentLifecycle(ctx context.Context, operation Operation, interception DNSTransparentInterception) (Reply, error) {
	reply, err := channel.doCommands(ctx, operation)
	if err != nil {
		return Reply{}, err
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok {
		return Reply{}, snapshotDecodeError("transparent DNS lifecycle returned %T", reply.Payload)
	}
	if err := verifyDNSTransparentReadback(interception, payload.CommandResults); err != nil {
		return Reply{}, err
	}
	return reply, nil
}

func (channel vppctlChannel) doDNSTransparentDeleteLifecycle(ctx context.Context, operation Operation, interception DNSTransparentInterception) (Reply, error) {
	commands := []string{
		"?set ly-route dns-intercept disable",
		fmt.Sprintf("?ip route del table %d 0.0.0.0/0", dnsIPv4TableID),
		fmt.Sprintf("?ip table del %d", dnsIPv4TableID),
		"show ly-route dns-intercept",
	}
	reply, err := channel.doCommands(ctx, Operation{
		Name:           operation.Name,
		RequestID:      operation.RequestID,
		Resource:       operation.Resource,
		Payload:        interception,
		VPPCtlCommands: commands,
	})
	if err != nil {
		return Reply{}, err
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok {
		return Reply{}, snapshotDecodeError("transparent DNS delete returned %T", reply.Payload)
	}
	if output := resultStdoutLast(payload.CommandResults, "show ly-route dns-intercept"); strings.Contains(output, "enabled 1") {
		return Reply{}, snapshotDecodeError("transparent DNS interception remains enabled for %s", interception.LANInterface)
	}
	return reply, nil
}
