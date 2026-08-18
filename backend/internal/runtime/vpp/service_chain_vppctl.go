package vpp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (channel vppctlChannel) doServiceChainApply(ctx context.Context, operation Operation, policy ServiceChainPolicy) (Reply, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, policyShowCommand(policy))
	if err != nil {
		return Reply{}, err
	}
	observed, present, err := observedServiceChainPolicyFromResults(results, policy)
	if err != nil {
		return Reply{}, err
	}
	if present {
		policy.ACLID = observed.ACLID
		readback, readErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("show acl-plugin acl index %d", policy.ACLID), attachmentShowCommand(policy), "show interface "+policy.IngressInterface)
		if readErr != nil {
			return Reply{}, readErr
		}
		results = append(results, readback...)
		if serviceChainPolicyMatches(policy, observed, results) {
			return serviceChainLifecycleReply(operation, policy, results), nil
		}
		cleanup, cleanupErr := channel.removeObservedServiceChainPolicy(ctx, operation, policy, observed)
		if cleanupErr != nil {
			return Reply{}, cleanupErr
		}
		results = append(results, cleanup...)
	} else {
		orphanResults, orphanErr := channel.removeTaggedServiceChainACLs(ctx, operation, serviceChainACLTag(policy))
		if orphanErr != nil {
			return Reply{}, orphanErr
		}
		results = append(results, orphanResults...)
	}

	created, err := channel.runServiceChainCommands(ctx, operation, serviceChainACLCommand(policy, false))
	if err != nil {
		return Reply{}, err
	}
	results = append(results, created...)
	policy.ACLID, err = allocatedServiceChainACLID(created)
	if err != nil {
		return Reply{}, err
	}
	applied, err := channel.runServiceChainCommands(ctx, operation, serviceChainPolicyCommands(policy)[1:]...)
	if err != nil {
		return Reply{}, err
	}
	finalResults := append(append([]VPPCTLCommandResult(nil), created...), applied...)
	appliedObserved, appliedPresent, appliedErr := observedServiceChainPolicyFromResults(finalResults, policy)
	if appliedErr != nil {
		return Reply{}, appliedErr
	}
	if !appliedPresent || !serviceChainPolicyMatches(policy, appliedObserved, finalResults) {
		return Reply{}, serviceChainDecodeError("applied policy %d failed strict readback: acl=%q policy=%q attach=%q", policy.PolicyID,
			strings.TrimSpace(resultStdoutLast(finalResults, fmt.Sprintf("show acl-plugin acl index %d", policy.ACLID))),
			strings.TrimSpace(resultStdoutLast(finalResults, policyShowCommand(policy))),
			strings.TrimSpace(resultStdoutLast(finalResults, attachmentShowCommand(policy))))
	}
	return serviceChainLifecycleReply(operation, policy, finalResults), nil
}

func (channel vppctlChannel) doServiceChainDelete(ctx context.Context, operation Operation, policy ServiceChainPolicy) (Reply, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, policyShowCommand(policy))
	if err != nil {
		return Reply{}, err
	}
	observed, present, err := observedServiceChainPolicyFromResults(results, policy)
	if err != nil {
		return Reply{}, err
	}
	if present {
		policy.ACLID = observed.ACLID
		removed, removeErr := channel.removeObservedServiceChainPolicy(ctx, operation, policy, observed)
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	} else {
		removed, removeErr := channel.removeTaggedServiceChainACLs(ctx, operation, serviceChainACLTag(policy))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	verified, err := channel.runServiceChainCommands(ctx, operation, policyShowCommand(policy), attachmentShowCommand(policy))
	if err != nil {
		return Reply{}, err
	}
	results = append(results, verified...)
	if !serviceChainPolicyAbsent(results, policy) {
		return Reply{}, serviceChainDecodeError("deleted policy %d remains attached or configured", policy.PolicyID)
	}
	return serviceChainLifecycleReply(operation, policy, results), nil
}

func (channel vppctlChannel) removeObservedServiceChainPolicy(ctx context.Context, operation Operation, policy ServiceChainPolicy, observed observedServiceChainABFPolicy) ([]VPPCTLCommandResult, error) {
	return channel.runServiceChainCommands(ctx, operation,
		fmt.Sprintf("abf attach %s del policy %d %s", observed.AddressFamily, observed.ID, policy.IngressInterface),
		fmt.Sprintf("abf policy del id %d acl %d via %s %s", observed.ID, observed.ACLID, observed.NextHop, observed.ServiceInterface),
		fmt.Sprintf("delete acl-plugin acl index %d", observed.ACLID),
		fmt.Sprintf("show abf policy %d", observed.ID),
		fmt.Sprintf("show acl-plugin acl index %d", observed.ACLID),
		"show abf attach "+policy.IngressInterface)
}

func (channel vppctlChannel) removeTaggedServiceChainACLs(ctx context.Context, operation Operation, tag string) ([]VPPCTLCommandResult, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, "show acl-plugin acl")
	if err != nil {
		return nil, err
	}
	for _, id := range taggedServiceChainACLIDs(resultStdout(results, "show acl-plugin acl"), tag) {
		show := fmt.Sprintf("show acl-plugin acl index %d", id)
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("delete acl-plugin acl index %d", id), show)
		if removeErr != nil {
			return nil, removeErr
		}
		if strings.TrimSpace(resultStdout(removed, show)) != "" {
			return nil, serviceChainDecodeError("orphan ACL %d remains after deletion", id)
		}
		results = append(results, removed...)
	}
	return results, nil
}

func (channel vppctlChannel) runServiceChainCommands(ctx context.Context, operation Operation, commands ...string) ([]VPPCTLCommandResult, error) {
	reply, err := channel.doCommands(ctx, Operation{Name: "vpp.service-chain.lifecycle", RequestID: operation.RequestID, Resource: operation.Resource, VPPCtlCommands: commands})
	if err != nil {
		return nil, err
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok {
		return nil, serviceChainDecodeError("lifecycle command returned %T", reply.Payload)
	}
	return payload.CommandResults, nil
}

func observedServiceChainPolicyFromResults(results []VPPCTLCommandResult, policy ServiceChainPolicy) (observedServiceChainABFPolicy, bool, error) {
	output := strings.TrimSpace(resultStdout(results, policyShowCommand(policy)))
	if output == "" || strings.Contains(strings.ToLower(output), "invalid policy") {
		return observedServiceChainABFPolicy{}, false, nil
	}
	observed, err := parseObservedServiceChainABFPolicy(output)
	return observed, err == nil, err
}

func serviceChainPolicyMatches(policy ServiceChainPolicy, observed observedServiceChainABFPolicy, results []VPPCTLCommandResult) bool {
	if observed.ID != policy.PolicyID || observed.ACLID != policy.ACLID || observed.AddressFamily != policy.AddressFamily || observed.NextHop != policy.NextHop || observed.ServiceInterface != policy.ServiceInterface {
		return false
	}
	acl, err := parseObservedServiceChainACL(resultStdoutLast(results, fmt.Sprintf("show acl-plugin acl index %d", policy.ACLID)))
	if err != nil || acl.ID != policy.ACLID || acl.Tag != serviceChainACLTag(policy) || acl.Match != policy.Match {
		return false
	}
	attachments, err := parseObservedServiceChainAttachments(resultStdoutLast(results, attachmentShowCommand(policy)))
	if err != nil {
		return false
	}
	for _, attachment := range attachments {
		if attachment.PolicyID == policy.PolicyID && attachment.AddressFamily == policy.AddressFamily && attachment.Priority == policy.Priority {
			return true
		}
	}
	return false
}

func serviceChainPolicyAbsent(results []VPPCTLCommandResult, policy ServiceChainPolicy) bool {
	policyOutput := strings.ToLower(strings.TrimSpace(resultStdoutLast(results, policyShowCommand(policy))))
	if policyOutput != "" && !strings.Contains(policyOutput, "invalid policy") {
		return false
	}
	for _, field := range strings.Fields(resultStdoutLast(results, attachmentShowCommand(policy))) {
		if field == "policy:"+strconv.Itoa(policy.PolicyID) {
			return false
		}
	}
	return true
}

func allocatedServiceChainACLID(results []VPPCTLCommandResult) (int, error) {
	for _, result := range results {
		for _, field := range strings.Fields(result.Stdout) {
			if id, err := strconv.Atoi(strings.TrimPrefix(field, "index:")); strings.HasPrefix(field, "index:") && err == nil && id >= 0 {
				return id, nil
			}
		}
	}
	return 0, serviceChainDecodeError("VPP did not return an allocated ACL index")
}

func taggedServiceChainACLIDs(output, tag string) []int {
	var ids []int
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		// Stock VPP prints: `acl-index <id> count <n> tag {<tag>}`.
		// The tag is one field; requiring a sixth field silently skipped every
		// dynamic ACL and left stale objects behind during replacement.
		if len(fields) < 5 || fields[0] != "acl-index" || fields[2] != "count" || fields[4] != "tag" {
			continue
		}
		if len(fields) < 6 || strings.Trim(fields[5], "{}") != tag {
			continue
		}
		if id, err := strconv.Atoi(fields[1]); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func resultStdout(results []VPPCTLCommandResult, command string) string {
	for _, result := range results {
		if result.Command == command {
			return result.Stdout
		}
	}
	return ""
}

func resultStdoutLast(results []VPPCTLCommandResult, command string) string {
	var output string
	for _, result := range results {
		if result.Command == command {
			output = result.Stdout
		}
	}
	return output
}

func serviceChainLifecycleReply(operation Operation, policy ServiceChainPolicy, results []VPPCTLCommandResult) Reply {
	return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{Readback: policy, CommandResults: results}}
}
