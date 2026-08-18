package vpp

import (
	"context"
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
	return &vppctlChannel{binary: client.Binary, dynamicACL: client.dynamicACL}, nil
}

type vppctlChannel struct {
	binary                string
	dynamicACL            bool
	natReturnGuardIngress string
	lanVPPInterface       string
}

func (channel *vppctlChannel) setNATReturnGuardIngress(ingress string) {
	channel.natReturnGuardIngress = strings.TrimSpace(ingress)
}

func (channel *vppctlChannel) setLANVPPInterface(ingress string) {
	channel.lanVPPInterface = strings.TrimSpace(ingress)
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
	operation = rewriteOperationLANInterface(operation, channel.lanVPPInterface)
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
		// The native pre-NAT route classifier owns ordinary IPv4 policy routing.
		// Some persisted plans created before that classifier still carry ABF
		// commands, including full-rebuild cleanup operations. Sending those
		// commands through the VPP 25.x ABF lifecycle can tear down a referenced
		// FIB and crash the data plane. Prefer the native lifecycle whenever the
		// policy can be represented by the classifier, regardless of the age of
		// the serialized operation.
		if policy, ok := operation.Payload.(trafficpolicy.RoutePolicy); ok && (routePolicySupportsNativePreNAT(policy) || operationHasCommand(operation, "set ly-route pre-nat-route add")) {
			return channel.doPreNATRoutePolicyLifecycle(ctx, operation)
		}
		if operationHasCommand(operation, "set ly-route pre-nat-route add") {
			return channel.doPreNATRoutePolicyLifecycle(ctx, operation)
		}
		return channel.doRoutePolicyLifecycle(ctx, operation)
	}
	if mapping, ok := operation.Payload.(nat.PortMapping); ok && channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.nat44-ed.port-map") {
		guard := natReturnGuardForPortMapping(mapping)
		guard.ingressVPPInterface = channel.natReturnGuardIngress
		return channel.doNAT44MappingLifecycle(ctx, operation, guard)
	}
	if mapping, ok := operation.Payload.(nat.StaticMapping); ok && channel.dynamicACL && strings.HasPrefix(operation.Name, "vpp.nat44-ed.static-mapping") {
		guard := natReturnGuardForStaticMapping(mapping)
		guard.ingressVPPInterface = channel.natReturnGuardIngress
		return channel.doNAT44MappingLifecycle(ctx, operation, guard)
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
	if group, ok := operation.Payload.(flow.VPPObjectGroup); ok && channel.dynamicACL && flowGroupNeedsDynamicLifecycle(group) {
		deleting := strings.Contains(operation.Name, "rollback-delete") || operationHasCommand(operation, "delete acl-plugin acl") || operationHasCommand(operation, "policer del")
		return channel.doFlowQoSGroupLifecycle(ctx, operation, group, deleting)
	}
	if target, ok := operation.Payload.(flow.Target); ok && channel.dynamicACL && flowTargetNeedsDynamicLifecycle(target) {
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

func rewriteOperationLANInterface(operation Operation, ingress string) Operation {
	ingress = strings.TrimSpace(ingress)
	if ingress == "" {
		return operation
	}
	operation.VPPCtlCommands = append([]string(nil), operation.VPPCtlCommands...)
	for index, command := range operation.VPPCtlCommands {
		operation.VPPCtlCommands[index] = strings.ReplaceAll(command, "lyroute-$LY_ROUTE_LAN_INTERFACE", ingress)
		operation.VPPCtlCommands[index] = strings.ReplaceAll(operation.VPPCtlCommands[index], "host-$LY_ROUTE_LAN_INTERFACE", ingress)
	}
	switch payload := operation.Payload.(type) {
	case flow.VPPObjectGroup:
		operation.Payload = rewriteFlowObjectGroupLANInterface(payload, ingress)
	case flow.Target:
		operation.Payload = rewriteFlowTargetLANInterface(payload, ingress)
	case SnapshotRequest:
		payload.LANVPPInterface = ingress
		operation.Payload = payload
	}
	return operation
}

func rewriteFlowObjectGroupLANInterface(group flow.VPPObjectGroup, ingress string) flow.VPPObjectGroup {
	group.Objects = append([]flow.VPPObject(nil), group.Objects...)
	for index := range group.Objects {
		object := &group.Objects[index]
		object.Attachments = append([]string(nil), object.Attachments...)
		for attachmentIndex, attachment := range object.Attachments {
			object.Attachments[attachmentIndex] = strings.ReplaceAll(attachment, "host-$LY_ROUTE_LAN_INTERFACE", ingress)
			object.Attachments[attachmentIndex] = strings.ReplaceAll(object.Attachments[attachmentIndex], "lyroute-$LY_ROUTE_LAN_INTERFACE", ingress)
		}
	}
	return group
}

func rewriteFlowTargetLANInterface(target flow.Target, ingress string) flow.Target {
	target.Attachments = append([]string(nil), target.Attachments...)
	for index, attachment := range target.Attachments {
		target.Attachments[index] = strings.ReplaceAll(attachment, "host-$LY_ROUTE_LAN_INTERFACE", ingress)
		target.Attachments[index] = strings.ReplaceAll(target.Attachments[index], "lyroute-$LY_ROUTE_LAN_INTERFACE", ingress)
	}
	return target
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
