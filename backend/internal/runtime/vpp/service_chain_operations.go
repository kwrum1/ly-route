package vpp

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"ly-route/backend/internal/orchestrator"
)

type serviceChainPolicyInput struct {
	chainID    string
	direction  orchestrator.ServiceChainDirection
	match      orchestrator.FlowTuple
	hop        orchestrator.ServiceChainHop
	interfaces map[string]string
}

func BuildServiceChainOperations(requestID string, chain orchestrator.ServiceChain, attachments []NativeAttachment) ([]Operation, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("%w: request ID is required", ErrServiceChainCapability)
	}
	if err := orchestrator.ValidateServiceChainState(chain); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServiceChainCapability, err)
	}
	if chain.Direct {
		return nil, nil
	}
	interfaces := make(map[string]string, len(attachments))
	for _, attachment := range attachments {
		dpdkIdentityApproved := attachment.Mode == NativeModeDPDKVFIO && decimalIdentifierSafe(attachment.IOMMUGroup) ||
			attachment.Mode == NativeModeDPDKUIO && attachment.IOMMUGroup == "none"
		approvedTier := (attachment.Tier == "" || attachment.Tier == DataplaneTierNative) && approvedNativeMode(attachment.Hook, attachment.Mode) ||
			attachment.Tier == DataplaneTierDPDK && attachment.Hook == NativeHookDPDK && pciAddressSafe(attachment.PCIAddress) && dpdkIdentityApproved
		if attachment.capabilityFingerprint == "" || attachment.capabilityFingerprint != nativeAttachmentFingerprint(attachment) || !approvedTier {
			return nil, fmt.Errorf("%w: interface %q lacks approved high-performance dataplane proof", ErrServiceChainCapability, strings.TrimSpace(attachment.LinuxInterface))
		}
		linuxName := strings.TrimSpace(attachment.LinuxInterface)
		vppName := strings.TrimSpace(attachment.VPPInterface)
		if linuxName != "" && vppName != "" {
			interfaces[linuxName] = vppName
		}
	}
	if err := requireServiceChainInterfaces(chain, interfaces); err != nil {
		return nil, err
	}
	operations := make([]Operation, 0, len(chain.Forward.Hops)+len(chain.Reverse.Hops))
	for _, hop := range chain.Forward.Hops {
		operations = append(operations, serviceChainOperation(requestID, serviceChainPolicyInput{chainID: chain.ID, direction: orchestrator.ServiceChainForward, match: chain.Forward.Match, hop: hop, interfaces: interfaces}))
	}
	for _, hop := range chain.Reverse.Hops {
		operations = append(operations, serviceChainOperation(requestID, serviceChainPolicyInput{chainID: chain.ID, direction: orchestrator.ServiceChainReverse, match: chain.Reverse.Match, hop: hop, interfaces: interfaces}))
	}
	return operations, nil
}

func (a Adapter) ApplyServiceChain(ctx context.Context, requestID string, chain orchestrator.ServiceChain, attachments []NativeAttachment) (ServiceChainApplyResult, error) {
	operations, err := BuildServiceChainOperations(requestID, chain, attachments)
	if err != nil || len(operations) == 0 {
		return ServiceChainApplyResult{Receipt: Receipt{RequestID: requestID, Operations: operations}, Readback: ServiceChainReadback{ChainID: chain.ID}}, err
	}
	if a.Client == nil {
		return ServiceChainApplyResult{}, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return ServiceChainApplyResult{}, fmt.Errorf("%w: open service chain channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	results := make([]VPPCTLCommandResult, 0)
	appliedOperations := make([]Operation, 0, len(operations))
	for _, operation := range operations {
		reply, operationErr := doOperation(ctx, channel, operation)
		if operationErr != nil {
			return ServiceChainApplyResult{}, operationErr
		}
		payload, ok := reply.Payload.(VPPCTLReplyPayload)
		if !ok {
			return ServiceChainApplyResult{}, fmt.Errorf("%w: operation %q returned %T", ErrServiceChainReadback, operation.Name, reply.Payload)
		}
		results = append(results, payload.CommandResults...)
		operation = serviceChainAppliedOperation(operation, payload)
		appliedOperations = append(appliedOperations, operation)
	}
	readback, err := DecodeServiceChainReadback(chain, appliedOperations, results)
	if err != nil {
		return ServiceChainApplyResult{}, err
	}
	return ServiceChainApplyResult{Receipt: Receipt{RequestID: requestID, Operations: appliedOperations}, Readback: readback}, nil
}

func serviceChainAppliedOperation(operation Operation, payload VPPCTLReplyPayload) Operation {
	policy, ok := payload.Readback.(ServiceChainPolicy)
	if !ok {
		return operation
	}
	operation.Payload = policy
	operation.VPPCtlCommands = serviceChainPolicyCommands(policy)
	return operation
}

func serviceChainOperation(requestID string, input serviceChainPolicyInput) Operation {
	resource := input.chainID + ":" + string(input.direction) + ":" + strconv.Itoa(input.hop.Position)
	policy := ServiceChainPolicy{
		ChainID: input.chainID, Direction: input.direction, Position: input.hop.Position, Group: input.hop.Group,
		ACLID: stableID("service-chain-acl:"+resource, 1000, 8999), PolicyID: stableID("service-chain-abf:"+resource, 10000, 39999), Priority: input.hop.Position * 10,
		IngressInterface: input.interfaces[input.hop.IngressInterface], ServiceInterface: input.interfaces[input.hop.ServiceInterface], ReturnInterface: input.interfaces[input.hop.ReturnInterface], NextHop: input.hop.NextHop, Match: input.match,
	}
	if address, err := netip.ParseAddr(input.match.SourceIP); err == nil && address.Is6() {
		policy.AddressFamily = "ip6"
	} else {
		policy.AddressFamily = "ip4"
	}
	return Operation{Name: "vpp.service-chain.abf-policy", RequestID: requestID, Resource: resource, Payload: policy, VPPCtlCommands: serviceChainPolicyCommands(policy)}
}

func requireServiceChainInterfaces(chain orchestrator.ServiceChain, interfaces map[string]string) error {
	required := map[string]struct{}{chain.Forward.IngressInterface: {}, chain.Forward.ExitInterface: {}, chain.Reverse.IngressInterface: {}, chain.Reverse.ExitInterface: {}}
	for _, path := range []orchestrator.ServiceChainPath{chain.Forward, chain.Reverse} {
		for _, hop := range path.Hops {
			required[hop.IngressInterface] = struct{}{}
			required[hop.ServiceInterface] = struct{}{}
			required[hop.ReturnInterface] = struct{}{}
		}
	}
	for name := range required {
		if interfaces[name] == "" {
			return fmt.Errorf("%w: interface %q", ErrServiceChainCapability, name)
		}
	}
	return nil
}
