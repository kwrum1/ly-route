package vpp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (channel vppctlChannel) doSmartQoSLifecycle(ctx context.Context, operation Operation, expected SmartQoSInterface) (Reply, error) {
	reply, err := channel.doCommands(ctx, operation)
	if err != nil {
		return reply, err
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok {
		return reply, fmt.Errorf("smart-QoS operation returned untyped payload %T", reply.Payload)
	}
	readback, err := commandOutput(payload.CommandResults, "show ly-route smart-qos")
	if err != nil {
		return reply, err
	}
	disabling := operationHasCommand(operation, "smart-qos interface "+expected.VPPInterface+" disable")
	if disabling {
		err = validateSmartQoSDisabledReadback(expected, readback)
	} else {
		err = validateSmartQoSReadback(expected, readback)
	}
	if err != nil {
		return reply, err
	}
	payload.Readback = expected
	reply.Payload = payload
	return reply, nil
}

func validateSmartQoSDisabledReadback(expected SmartQoSInterface, readback string) error {
	prefix := "interface " + expected.VPPInterface + " enabled"
	for _, line := range strings.Split(strings.ReplaceAll(readback, "\r", ""), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return fmt.Errorf("smart-QoS disable readback still reports enabled interface %q", expected.VPPInterface)
		}
	}
	return nil
}

func validateSmartQoSReadback(expected SmartQoSInterface, readback string) error {
	for _, required := range []string{"state running", "algorithm fq-codel", "qualification production"} {
		if !hasExactOutputLine(readback, required) {
			return fmt.Errorf("smart-QoS semantic readback missing %q", required)
		}
	}
	want := "interface " + expected.VPPInterface + " enabled rate-kbps " + strconv.FormatUint(expected.RateKbps, 10) + " host-isolation " + expected.HostIsolation
	for _, line := range strings.Split(strings.ReplaceAll(readback, "\r", ""), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), want+" ") || strings.TrimSpace(line) == want {
			return nil
		}
	}
	return fmt.Errorf("smart-QoS semantic readback missing interface contract %q", want)
}

func hasExactOutputLine(output, expected string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r", ""), "\n") {
		if strings.TrimSpace(line) == expected {
			return true
		}
	}
	return false
}
