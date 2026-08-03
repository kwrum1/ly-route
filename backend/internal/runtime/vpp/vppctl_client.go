package vpp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

type VPPCTLClient struct {
	Binary     string
	dynamicACL bool
}

func NewVPPCTLClient(binary string) VPPCTLClient {
	if strings.TrimSpace(binary) == "" {
		binary = "vppctl"
	}
	return VPPCTLClient{Binary: binary}
}

func NewProductionVPPCTLClient(binary string) VPPCTLClient {
	client := NewVPPCTLClient(binary)
	client.dynamicACL = true
	return client
}

func (client VPPCTLClient) OpenChannel(context.Context) (Channel, error) {
	if _, err := exec.LookPath(client.Binary); err != nil {
		return nil, fmt.Errorf("vppctl binary %q is unavailable: %w", client.Binary, err)
	}
	return vppctlChannel{binary: client.Binary, dynamicACL: client.dynamicACL}, nil
}

type vppctlChannel struct {
	binary     string
	dynamicACL bool
}

type VPPCTLCommandResult struct {
	Command string
	Stdout  string
	Stderr  string
	Retval  int32
}

type VPPCTLReplyPayload struct {
	Readback       any
	CommandResults []VPPCTLCommandResult
}

func (channel vppctlChannel) Do(ctx context.Context, operation Operation) (Reply, error) {
	if channel.dynamicACL && operation.Name == "vpp.security-acl.snapshot" {
		operation.VPPCtlCommands = []string{"show acl-plugin acl"}
	}
	if channel.dynamicACL && operation.Name == "vpp.qos.snapshot" {
		request, ok := operation.Payload.(SnapshotRequest)
		if !ok {
			return Reply{}, snapshotDecodeError("QoS snapshot payload has type %T", operation.Payload)
		}
		operation.VPPCtlCommands = dynamicQoSSnapshotCommands(request)
	}
	if acl, ok := operation.Payload.(trafficpolicy.SecurityACL); ok && channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.security-acl") {
		deleting := strings.HasSuffix(operation.Name, ".rollback-delete") || operationHasCommand(operation, "delete acl-plugin acl")
		return channel.doSecurityACLLifecycle(ctx, operation, acl, deleting)
	}
	if steering, ok := operation.Payload.(proxy.VPPSteeringInstruction); ok && channel.dynamicACL && steering.TargetKind == "vpp.abf.policy" {
		if strings.HasSuffix(operation.Name, ".rollback-delete") {
			return channel.doProxyABFDelete(ctx, operation, steering)
		}
		return channel.doProxyABFLifecycle(ctx, operation, steering)
	}
	if channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.route-policy") && !strings.HasSuffix(operation.Name, ".snapshot") {
		return channel.doRoutePolicyLifecycle(ctx, operation)
	}
	if interception, ok := operation.Payload.(DNSTransparentInterception); ok && channel.dynamicACL && operation.Name == "vpp.dns-transparent-interception" {
		return channel.doDNSTransparentLifecycle(ctx, operation, interception)
	}
	if generation, ok := operation.Payload.(SecurityGeneration); ok && channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.security-generation") {
		return channel.doSecurityGenerationLifecycle(ctx, operation, generation, strings.HasSuffix(operation.Name, ".rollback-delete"))
	}
	if management, ok := operation.Payload.(ManagementLCP); ok && operation.Name == "vpp.management-lcp" {
		return channel.doManagementLCPLifecycle(ctx, operation, management)
	}
	if smartQoS, ok := operation.Payload.(SmartQoSInterface); ok && strings.HasPrefix(operation.Name, "vpp.smart-qos") {
		return channel.doSmartQoSLifecycle(ctx, operation, smartQoS)
	}
	if group, ok := operation.Payload.(flow.VPPObjectGroup); ok && channel.dynamicACL && flowGroupUsesACL(group) {
		deleting := strings.Contains(operation.Name, "rollback-delete") || operationHasCommand(operation, "delete acl-plugin acl") || operationHasCommand(operation, "policer del")
		return channel.doFlowQoSGroupLifecycle(ctx, operation, group, deleting)
	}
	if target, ok := operation.Payload.(flow.Target); ok && channel.dynamicACL && flowTargetUsesACL(target) {
		deleting := strings.Contains(operation.Name, "rollback-delete") || operationHasCommand(operation, "delete acl-plugin acl") || operationHasCommand(operation, "policer del")
		return channel.doFlowQoSTargetLifecycle(ctx, operation, target, deleting)
	}
	if policy, ok := operation.Payload.(ServiceChainPolicy); ok {
		switch operation.Name {
		case "vpp.service-chain.abf-policy":
			return channel.doServiceChainApply(ctx, operation, policy)
		case "vpp.service-chain.delete", "vpp.service-chain.bypass-delete":
			return channel.doServiceChainDelete(ctx, operation, policy)
		}
	}
	return channel.doCommands(ctx, operation)
}

func operationHasCommand(operation Operation, fragment string) bool {
	for _, command := range operation.VPPCtlCommands {
		if strings.Contains(command, fragment) {
			return true
		}
	}
	return false
}

func (channel vppctlChannel) doCommands(ctx context.Context, operation Operation) (Reply, error) {
	results := make([]VPPCTLCommandResult, 0, len(operation.VPPCtlCommands))
	for _, command := range operation.VPPCtlCommands {
		command = strings.TrimSpace(command)
		ignoreFailure := strings.HasPrefix(command, "?")
		command = strings.TrimSpace(strings.TrimPrefix(command, "?"))
		logicalCommand := command
		if lanInterface := strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")); lanInterface != "" {
			command = strings.ReplaceAll(command, "$LY_ROUTE_LAN_INTERFACE", lanInterface)
		}
		if command == "" {
			continue
		}
		args := strings.Fields(command)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, channel.binary, args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		retval := int32(0)
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				retval = int32(exitErr.ExitCode())
			} else {
				retval = -1
			}
		}
		results = append(results, VPPCTLCommandResult{Command: logicalCommand, Stdout: stdout.String(), Stderr: stderr.String(), Retval: retval})
		if err != nil {
			if ignoreFailure {
				continue
			}
			reply := Reply{Operation: operation.Name, Retval: retval, Payload: VPPCTLReplyPayload{CommandResults: results}}
			failure := fmt.Errorf("vppctl %s command %q failed with retval %d: %w: %s", operation.Name, command, retval, err, strings.TrimSpace(stderr.String()))
			if strings.HasSuffix(operation.Name, ".snapshot") {
				return reply, fmt.Errorf("%w: %v", ErrSnapshotIncomplete, failure)
			}
			return reply, failure
		}
	}
	payload := VPPCTLReplyPayload{CommandResults: results}
	readback, err := decodeVPPCTLReadback(operation, results)
	if err != nil {
		return Reply{Operation: operation.Name, Payload: payload}, err
	}
	payload.Readback = readback
	return Reply{Operation: operation.Name, Payload: payload}, nil
}

func (vppctlChannel) Close() error { return nil }
