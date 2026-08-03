package vpp

import (
	"context"
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/proxy"
)

func (channel vppctlChannel) doProxyABFLifecycle(ctx context.Context, operation Operation, steering proxy.VPPSteeringInstruction) (Reply, error) {
	resource := strings.TrimSpace(steering.EgressID)
	if resource == "" {
		resource = string(steering.Handoff)
	}
	policyID := stableID("abf:"+resource, 1000, 8999)
	stableACLID := stableID("acl:"+resource, 1000, 8999)
	tag := "ly-route-" + safeTag(resource)
	commands := operation.VPPCtlCommands
	create := ""
	for _, raw := range commands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		if strings.HasPrefix(command, "set acl-plugin acl ") {
			create = strings.Replace(command, fmt.Sprintf(" index %d", stableACLID), "", 1)
			break
		}
	}
	if create == "" {
		return Reply{}, snapshotDecodeError("proxy ABF %q has no ACL creation command", resource)
	}
	created, err := channel.runServiceChainCommands(ctx, operation, create)
	if err != nil {
		return Reply{}, err
	}
	actualACLID, err := allocatedServiceChainACLID(created)
	if err != nil {
		return Reply{}, err
	}
	applyCommands := make([]string, 0, len(commands))
	for _, raw := range commands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		if strings.HasPrefix(command, "set acl-plugin acl ") {
			continue
		}
		command = strings.Replace(command, fmt.Sprintf(" acl %d ", stableACLID), fmt.Sprintf(" acl %d ", actualACLID), 1)
		command = strings.Replace(command, fmt.Sprintf("show acl-plugin acl index %d", stableACLID), fmt.Sprintf("show acl-plugin acl index %d", actualACLID), 1)
		applyCommands = append(applyCommands, command)
	}
	applied, err := channel.runServiceChainCommands(ctx, operation, applyCommands...)
	if err != nil {
		return Reply{}, err
	}
	results := append(created, applied...)
	policyOutput := resultStdoutLast(results, fmt.Sprintf("show abf policy %d", policyID))
	observedACL, present := observedABFACLID(policyOutput)
	if !present || observedACL != actualACLID {
		return Reply{}, snapshotDecodeError("proxy ABF %q references ACL %d, want %d", resource, observedACL, actualACLID)
	}
	aclOutput := resultStdoutLast(results, fmt.Sprintf("show acl-plugin acl index %d", actualACLID))
	if !strings.Contains(aclOutput, "tag {"+tag+"}") {
		return Reply{}, snapshotDecodeError("proxy ABF %q ACL tag readback is missing", resource)
	}
	if !routePolicyAttached(resultStdoutLast(results, "show abf attach lyroute-$LY_ROUTE_LAN_INTERFACE"), policyID) {
		return Reply{}, snapshotDecodeError("proxy ABF %q attachment readback is missing", resource)
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func (channel vppctlChannel) doProxyABFDelete(ctx context.Context, operation Operation, steering proxy.VPPSteeringInstruction) (Reply, error) {
	resource := strings.TrimSpace(steering.EgressID)
	if resource == "" {
		resource = string(steering.Handoff)
	}
	policyID := stableID("abf:"+resource, 1000, 8999)
	tag := "ly-route-" + safeTag(resource)
	results, err := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("show abf policy %d", policyID), "show acl-plugin acl")
	if err != nil {
		return Reply{}, err
	}
	aclID, present := observedABFACLID(resultStdout(results, fmt.Sprintf("show abf policy %d", policyID)))
	if present {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("abf attach ip4 del policy %d lyroute-$LY_ROUTE_LAN_INTERFACE", policyID),
			fmt.Sprintf("abf policy del id %d acl %d via local", policyID, aclID),
			fmt.Sprintf("delete acl-plugin acl index %d", aclID))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	for _, id := range taggedServiceChainACLIDs(resultStdout(results, "show acl-plugin acl"), tag) {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("delete acl-plugin acl index %d", id))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	verified, err := channel.runServiceChainCommands(ctx, operation,
		fmt.Sprintf("show abf policy %d", policyID), "show acl-plugin acl", "show abf attach lyroute-$LY_ROUTE_LAN_INTERFACE")
	if err != nil {
		return Reply{}, err
	}
	results = append(results, verified...)
	if _, stillPresent := observedABFACLID(resultStdoutLast(results, fmt.Sprintf("show abf policy %d", policyID))); stillPresent {
		return Reply{}, snapshotDecodeError("proxy ABF %q policy remains after deletion", resource)
	}
	if len(taggedServiceChainACLIDs(resultStdoutLast(results, "show acl-plugin acl"), tag)) != 0 {
		return Reply{}, snapshotDecodeError("proxy ABF %q ACL remains after deletion", resource)
	}
	if routePolicyAttached(resultStdoutLast(results, "show abf attach lyroute-$LY_ROUTE_LAN_INTERFACE"), policyID) {
		return Reply{}, snapshotDecodeError("proxy ABF %q attachment remains after deletion", resource)
	}
	return routePolicyLifecycleReply(operation, results), nil
}
