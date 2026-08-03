package vpp

import (
	"context"
	"fmt"

	"ly-route/backend/internal/orchestrator"
)

func BuildServiceChainTransitionOperations(requestID string, desired, active orchestrator.ServiceChain, attachments []NativeAttachment) ([]Operation, []Operation, error) {
	desiredOperations, err := BuildServiceChainOperations(requestID, desired, attachments)
	if err != nil {
		return nil, nil, err
	}
	activeOperations, err := BuildServiceChainOperations(requestID, active, attachments)
	if err != nil {
		return nil, nil, err
	}
	deleteOperations := make([]Operation, 0, len(desiredOperations))
	for _, operation := range desiredOperations {
		policy, ok := operation.Payload.(ServiceChainPolicy)
		if !ok {
			return nil, nil, fmt.Errorf("%w: transition operation %q has payload %T", ErrServiceChainCapability, operation.Name, operation.Payload)
		}
		deleteOperations = append(deleteOperations, Operation{
			Name:      "vpp.service-chain.delete",
			RequestID: requestID,
			Resource:  operation.Resource,
			Payload:   policy,
			VPPCtlCommands: []string{
				fmt.Sprintf("?abf attach %s del policy %d %s", policy.AddressFamily, policy.PolicyID, policy.IngressInterface),
				fmt.Sprintf("?abf policy del id %d acl %d via %s %s", policy.PolicyID, policy.ACLID, policy.NextHop, policy.ServiceInterface),
				fmt.Sprintf("?delete acl-plugin acl index %d", policy.ACLID),
				policyShowCommand(policy),
				fmt.Sprintf("show acl-plugin acl index %d", policy.ACLID),
				attachmentShowCommand(policy),
			},
		})
	}
	return deleteOperations, activeOperations, nil
}

func (a Adapter) ApplyServiceChainTransition(ctx context.Context, requestID string, desired, active orchestrator.ServiceChain, attachments []NativeAttachment) (ServiceChainApplyResult, error) {
	deleteOperations, activeOperations, err := BuildServiceChainTransitionOperations(requestID, desired, active, attachments)
	if err != nil {
		return ServiceChainApplyResult{}, err
	}
	operations := append(append([]Operation(nil), deleteOperations...), activeOperations...)
	if len(operations) == 0 {
		return ServiceChainApplyResult{Receipt: Receipt{RequestID: requestID}, Readback: ServiceChainReadback{ChainID: active.ID}}, nil
	}
	if a.Client == nil {
		return ServiceChainApplyResult{}, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return ServiceChainApplyResult{}, fmt.Errorf("%w: open service chain transition channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	for _, operation := range deleteOperations {
		if _, err := doOperation(ctx, channel, operation); err != nil {
			return ServiceChainApplyResult{}, err
		}
	}
	results := make([]VPPCTLCommandResult, 0)
	appliedOperations := make([]Operation, 0, len(activeOperations))
	for _, operation := range activeOperations {
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
	readback := ServiceChainReadback{ChainID: active.ID}
	if len(activeOperations) > 0 {
		readback, err = DecodeServiceChainReadback(active, appliedOperations, results)
		if err != nil {
			return ServiceChainApplyResult{}, err
		}
	}
	operations = append(append([]Operation(nil), deleteOperations...), appliedOperations...)
	return ServiceChainApplyResult{Receipt: Receipt{RequestID: requestID, Operations: operations}, Readback: readback}, nil
}

func BuildServiceChainBypassOperations(requestID string, desired orchestrator.ServiceChain) ([]Operation, error) {
	if err := orchestrator.ValidateServiceChainState(desired); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServiceChainCapability, err)
	}
	interfaces := map[string]string{}
	for _, path := range []orchestrator.ServiceChainPath{desired.Forward, desired.Reverse} {
		interfaces[path.IngressInterface] = "lyroute-" + path.IngressInterface
		interfaces[path.ExitInterface] = "lyroute-" + path.ExitInterface
		for _, hop := range path.Hops {
			interfaces[hop.IngressInterface] = "lyroute-" + hop.IngressInterface
			interfaces[hop.ServiceInterface] = "lyroute-" + hop.ServiceInterface
			interfaces[hop.ReturnInterface] = "lyroute-" + hop.ReturnInterface
		}
	}
	operations := []Operation{}
	for _, path := range []orchestrator.ServiceChainPath{desired.Forward, desired.Reverse} {
		for _, hop := range path.Hops {
			add := serviceChainOperation(requestID, serviceChainPolicyInput{chainID: desired.ID, direction: path.Direction, match: path.Match, hop: hop, interfaces: interfaces})
			policy := add.Payload.(ServiceChainPolicy)
			operations = append(operations, Operation{Name: "vpp.service-chain.bypass-delete", RequestID: requestID, Resource: add.Resource, Payload: policy, VPPCtlCommands: []string{
				fmt.Sprintf("?abf attach %s del policy %d %s", policy.AddressFamily, policy.PolicyID, policy.IngressInterface),
				fmt.Sprintf("?abf policy del id %d acl %d via %s %s", policy.PolicyID, policy.ACLID, policy.NextHop, policy.ServiceInterface),
				fmt.Sprintf("?delete acl-plugin acl index %d", policy.ACLID),
				policyShowCommand(policy),
				fmt.Sprintf("show acl-plugin acl index %d", policy.ACLID),
				attachmentShowCommand(policy),
			}})
		}
	}
	return operations, nil
}

func (a Adapter) ApplyServiceChainBypass(ctx context.Context, requestID string, desired orchestrator.ServiceChain) (ServiceChainApplyResult, error) {
	operations, err := BuildServiceChainBypassOperations(requestID, desired)
	if err != nil {
		return ServiceChainApplyResult{}, err
	}
	if len(operations) == 0 {
		return ServiceChainApplyResult{Receipt: Receipt{RequestID: requestID}, Readback: ServiceChainReadback{ChainID: desired.ID}}, nil
	}
	if a.Client == nil {
		return ServiceChainApplyResult{}, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return ServiceChainApplyResult{}, fmt.Errorf("%w: open service chain bypass channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	for _, operation := range operations {
		if _, err := doOperation(ctx, channel, operation); err != nil {
			return ServiceChainApplyResult{}, err
		}
	}
	return ServiceChainApplyResult{Receipt: Receipt{RequestID: requestID, Operations: operations}, Readback: ServiceChainReadback{ChainID: desired.ID}}, nil
}
