package vpp

import (
	"fmt"
	"strconv"
	"strings"

	"ly-route/backend/internal/orchestrator"
)

func DecodeServiceChainReadback(chain orchestrator.ServiceChain, operations []Operation, results []VPPCTLCommandResult) (ServiceChainReadback, error) {
	if err := orchestrator.ValidateServiceChainState(chain); err != nil {
		return ServiceChainReadback{}, fmt.Errorf("%w: %w", ErrServiceChainReadback, err)
	}
	expected := len(chain.Forward.Hops) + len(chain.Reverse.Hops)
	if chain.Direct {
		expected = 0
	}
	if len(operations) != expected {
		return ServiceChainReadback{}, fmt.Errorf("%w: operation count %d, want %d", ErrServiceChainReadback, len(operations), expected)
	}
	readback := ServiceChainReadback{ChainID: chain.ID, Policies: make([]ServiceChainPolicyReadback, 0, expected)}
	seenInterfaces := make(map[string]struct{}, expected)
	for _, operation := range operations {
		policyReadback, err := decodeServiceChainPolicyReadback(operation, results)
		if err != nil {
			return ServiceChainReadback{}, err
		}
		readback.Policies = append(readback.Policies, policyReadback)
		if _, exists := seenInterfaces[policyReadback.IngressInterface]; exists {
			continue
		}
		output, outputErr := commandOutput(results, "show interface "+policyReadback.IngressInterface)
		if outputErr != nil {
			return ServiceChainReadback{}, fmt.Errorf("%w: interface %q", ErrServiceChainReadback, policyReadback.IngressInterface)
		}
		counters, parseErr := parseServiceChainInterfaceCounters(policyReadback.IngressInterface, output)
		if parseErr != nil {
			return ServiceChainReadback{}, parseErr
		}
		seenInterfaces[policyReadback.IngressInterface] = struct{}{}
		readback.Interfaces = append(readback.Interfaces, counters)
	}
	return readback, nil
}

func decodeServiceChainPolicyReadback(operation Operation, results []VPPCTLCommandResult) (ServiceChainPolicyReadback, error) {
	policy, ok := operation.Payload.(ServiceChainPolicy)
	if !ok {
		return ServiceChainPolicyReadback{}, fmt.Errorf("%w: operation %q payload is %T", ErrServiceChainReadback, operation.Name, operation.Payload)
	}
	aclOutput, err := commandOutput(results, "show acl-plugin acl index "+strconv.Itoa(policy.ACLID))
	if err != nil {
		return ServiceChainPolicyReadback{}, fmt.Errorf("%w: ACL identity: %w", ErrServiceChainReadback, err)
	}
	acl, err := parseObservedServiceChainACL(aclOutput)
	if err != nil {
		return ServiceChainPolicyReadback{}, err
	}
	if acl.ID != policy.ACLID || acl.Tag != serviceChainACLTag(policy) {
		return ServiceChainPolicyReadback{}, serviceChainDecodeError("ACL identity does not match policy %d", policy.PolicyID)
	}
	if acl.Match != policy.Match {
		return ServiceChainPolicyReadback{}, serviceChainDecodeError("ACL tuple does not match policy %d", policy.PolicyID)
	}
	policyOutput, err := commandOutput(results, policyShowCommand(policy))
	if err != nil {
		return ServiceChainPolicyReadback{}, fmt.Errorf("%w: ABF policy identity: %w", ErrServiceChainReadback, err)
	}
	observedPolicy, err := parseObservedServiceChainABFPolicy(policyOutput)
	if err != nil {
		return ServiceChainPolicyReadback{}, err
	}
	if observedPolicy.ID != policy.PolicyID || observedPolicy.ACLID != acl.ID {
		return ServiceChainPolicyReadback{}, serviceChainDecodeError("ABF policy identity does not match policy %d", policy.PolicyID)
	}
	if observedPolicy.AddressFamily != policy.AddressFamily || observedPolicy.NextHop != policy.NextHop || observedPolicy.ServiceInterface != policy.ServiceInterface {
		return ServiceChainPolicyReadback{}, serviceChainDecodeError("ABF path does not match policy %d", policy.PolicyID)
	}
	attachmentCommand := attachmentShowCommand(policy)
	attachmentOutput, err := commandOutput(results, attachmentCommand)
	if err != nil {
		return ServiceChainPolicyReadback{}, fmt.Errorf("%w: attachment identity: %w", ErrServiceChainReadback, err)
	}
	attachments, err := parseObservedServiceChainAttachments(attachmentOutput)
	if err != nil {
		return ServiceChainPolicyReadback{}, err
	}
	var observedAttachment observedServiceChainAttachment
	matches := 0
	for _, attachment := range attachments {
		if attachment.PolicyID == observedPolicy.ID {
			observedAttachment = attachment
			matches++
		}
	}
	if matches != 1 {
		return ServiceChainPolicyReadback{}, serviceChainDecodeError("attachment identity for policy %d returned %d matches", policy.PolicyID, matches)
	}
	if observedAttachment.AddressFamily != observedPolicy.AddressFamily {
		return ServiceChainPolicyReadback{}, serviceChainDecodeError("attachment family does not match policy %d", policy.PolicyID)
	}
	if observedAttachment.Priority != policy.Priority {
		return ServiceChainPolicyReadback{}, serviceChainDecodeError("attachment priority does not match policy %d", policy.PolicyID)
	}
	return ServiceChainPolicyReadback{
		ChainID: policy.ChainID, Direction: policy.Direction, Position: policy.Position, Group: policy.Group,
		ACLID: acl.ID, PolicyID: observedPolicy.ID, Priority: observedAttachment.Priority, AddressFamily: observedAttachment.AddressFamily,
		IngressInterface: strings.TrimPrefix(attachmentCommand, "show abf attach "), ServiceInterface: observedPolicy.ServiceInterface,
		NextHop: observedPolicy.NextHop, Match: acl.Match, Attached: true,
	}, nil
}

func parseServiceChainInterfaceCounters(interfaceName, output string) (ServiceChainInterfaceReadback, error) {
	readback := ServiceChainInterfaceReadback{Interface: interfaceName}
	found := make(map[string]bool, 4)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		last := len(fields) - 1
		value, err := strconv.ParseUint(fields[last], 10, 64)
		if err != nil {
			continue
		}
		key := fields[last-2] + " " + fields[last-1]
		switch key {
		case "rx packets":
			readback.RXPackets = value
		case "tx packets":
			readback.TXPackets = value
		case "rx bytes":
			readback.RXBytes = value
		case "tx bytes":
			readback.TXBytes = value
		default:
			continue
		}
		found[key] = true
	}
	if len(found) != 4 {
		return ServiceChainInterfaceReadback{}, fmt.Errorf("%w: interface %q counters in %q", ErrServiceChainReadback, interfaceName, strings.TrimSpace(output))
	}
	return readback, nil
}
