package vpp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
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

// vppRouteBatchBegin/end are internal declarative markers.  VPP's CLI has a
// finite input-line size, so a large provider prefix set must be sent through
// `vppctl exec` as many short commands instead of one giant ACL line.
const (
	vppRouteBatchBegin = "__ly-route-vpp-batch-begin__"
	vppRouteBatchEnd   = "__ly-route-vpp-batch-end__"
)

func (channel vppctlChannel) Do(ctx context.Context, operation Operation) (Reply, error) {
	if attachment, ok := operation.Payload.(NativeAttachment); ok && attachment.Hook == NativeHookVMXNET3 {
		return channel.doVMXNET3Lifecycle(ctx, operation, attachment)
	}
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
	if mapping, ok := operation.Payload.(nat.PortMapping); ok && channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.nat44-ed.port-map") {
		return channel.doNAT44MappingLifecycle(ctx, operation, natReturnGuardForPortMapping(mapping))
	}
	if mapping, ok := operation.Payload.(nat.StaticMapping); ok && channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.nat44-ed.static-mapping") {
		return channel.doNAT44MappingLifecycle(ctx, operation, natReturnGuardForStaticMapping(mapping))
	}
	if interception, ok := operation.Payload.(DNSTransparentInterception); ok && channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.dns-transparent-interception") {
		if strings.HasSuffix(operation.Name, ".rollback-delete") {
			return channel.doDNSTransparentDeleteLifecycle(ctx, operation, interception)
		}
		return channel.doDNSTransparentLifecycle(ctx, operation, interception)
	}
	if generation, ok := operation.Payload.(SecurityGeneration); ok && channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.security-generation") {
		return channel.doSecurityGenerationLifecycle(ctx, operation, generation, strings.HasSuffix(operation.Name, ".rollback-delete"))
	}
	if management, ok := operation.Payload.(ManagementLCP); ok && (strings.HasPrefix(operation.Name, "vpp.management-lcp") || strings.HasPrefix(operation.Name, "vpp.lan-control-lcp")) {
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

func (channel vppctlChannel) doVMXNET3Lifecycle(ctx context.Context, operation Operation, attachment NativeAttachment) (Reply, error) {
	results := make([]VPPCTLCommandResult, 0, 6)
	run := func(command string, args ...string) (string, error) {
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
		results = append(results, VPPCTLCommandResult{Command: command, Stdout: stdout.String(), Stderr: stderr.String(), Retval: retval})
		if err != nil {
			return stdout.String(), fmt.Errorf("vppctl %s failed with retval %d: %w: %s", command, retval, err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), nil
	}

	if strings.HasSuffix(operation.Name, ".rollback-delete") {
		if strings.TrimSpace(attachment.VPPInterface) != "" {
			_, _ = run("delete interface vmxnet3 "+attachment.VPPInterface, "delete", "interface", "vmxnet3", attachment.VPPInterface)
		}
		if _, err := run("show interface", "show", "interface"); err != nil {
			return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, err
		}
		return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, nil
	}
	if strings.TrimSpace(attachment.PCIAddress) == "" {
		return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, errors.New("VMXNET3 attachment has no PCI address")
	}
	_, _ = run("create interface vmxnet3 "+attachment.PCIAddress, "create", "interface", "vmxnet3", attachment.PCIAddress)
	vmxnet3Output, err := run("show vmxnet3", "show", "vmxnet3")
	if err != nil {
		return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, err
	}
	actual := parseVMXNET3Interface(vmxnet3Output, attachment.PCIAddress)
	if actual == "" {
		return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, fmt.Errorf("VPP VMXNET3 interface for PCI %s was not found", attachment.PCIAddress)
	}
	desired := strings.TrimSpace(attachment.VPPInterface)
	if desired == "" {
		desired = actual
	}
	if actual != desired {
		if _, err := run("set interface name "+actual+" "+desired, "set", "interface", "name", actual, desired); err != nil {
			return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, err
		}
	}
	if _, err := run("set interface state "+desired+" up", "set", "interface", "state", desired, "up"); err != nil {
		return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, err
	}
	if _, err := run("show hardware-interfaces "+desired, "show", "hardware-interfaces", desired); err != nil {
		return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, err
	}
	if _, err := run("show interface "+desired, "show", "interface", desired); err != nil {
		return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, err
	}
	return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, nil
}

func parseVMXNET3Interface(output, pci string) string {
	current := ""
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "Interface:" {
			current = fields[1]
			continue
		}
		if len(fields) >= 3 && fields[0] == "PCI" && fields[1] == "Address:" && fields[2] == pci {
			return current
		}
	}
	return ""
}

func operationHasCommand(operation Operation, fragment string) bool {
	for _, command := range operation.VPPCtlCommands {
		if strings.Contains(command, fragment) {
			return true
		}
	}
	return false
}

func (vppctlChannel) Close() error { return nil }
