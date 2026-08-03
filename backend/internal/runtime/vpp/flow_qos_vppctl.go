package vpp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/flow"
)

func flowGroupUsesACL(group flow.VPPObjectGroup) bool {
	return group.Kind == "vpp.acl.drop" || group.Kind == "vpp.behavior.rate"
}

func flowTargetUsesACL(target flow.Target) bool {
	return target.Kind == "vpp.acl.drop" || target.Kind == "vpp.behavior.rate"
}

func qosGroupByKind(groups []flow.VPPObjectGroup, kind string) (flow.VPPObjectGroup, bool) {
	for _, group := range groups {
		if group.Kind == kind {
			return group, true
		}
	}
	return flow.VPPObjectGroup{}, false
}

func flowGroupDeleteCommands(group flow.VPPObjectGroup) []string {
	var commands []string
	for _, object := range group.Objects {
		target := flow.Target{Kind: group.Kind, RuleID: object.RuleID, Granularity: object.Granularity, Action: object.Action, Class: object.Class, DSCP: object.DSCP, RemarkBehavior: object.RemarkBehavior, Policer: object.Policer, Match: object.Match, Attachments: object.Attachments}
		commands = append(commands, flowTargetDeleteCommands(target)...)
	}
	if len(commands) == 0 {
		return deleteQoSCommands(group.Kind)
	}
	return commands
}

func (channel vppctlChannel) doFlowQoSGroupLifecycle(ctx context.Context, operation Operation, group flow.VPPObjectGroup, deleting bool) (Reply, error) {
	results := make([]VPPCTLCommandResult, 0)
	for _, object := range group.Objects {
		target := flow.Target{Kind: group.Kind, RuleID: object.RuleID, Granularity: object.Granularity, Action: object.Action, Class: object.Class, DSCP: object.DSCP, RemarkBehavior: object.RemarkBehavior, Policer: object.Policer, Match: object.Match, Attachments: object.Attachments}
		targetOperation := operation
		targetOperation.Resource = object.RuleID
		targetOperation.Payload = target
		result, err := channel.applyFlowQoSTarget(ctx, targetOperation, target, deleting)
		if err != nil {
			return Reply{}, err
		}
		results = append(results, result...)
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func (channel vppctlChannel) doFlowQoSTargetLifecycle(ctx context.Context, operation Operation, target flow.Target, deleting bool) (Reply, error) {
	results, err := channel.applyFlowQoSTarget(ctx, operation, target, deleting)
	if err != nil {
		return Reply{}, err
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func (channel vppctlChannel) applyFlowQoSTarget(ctx context.Context, operation Operation, target flow.Target, deleting bool) ([]VPPCTLCommandResult, error) {
	tag := "ly-route-" + safeTag(target.RuleID)
	results, err := channel.removeFlowQoSTarget(ctx, operation, target, tag)
	if err != nil || deleting {
		return results, err
	}
	commands := flowTargetCommands(target)
	stableACLID := stableID("flow-acl-drop:"+target.RuleID, 10000, 49999)
	if target.Kind == "vpp.behavior.rate" {
		stableACLID = stableID("flow-acl-rate:"+target.RuleID, 10000, 49999)
	}
	create := ""
	for _, raw := range commands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		if strings.HasPrefix(command, "set acl-plugin acl ") {
			create = strings.Replace(command, fmt.Sprintf(" index %d", stableACLID), "", 1)
			break
		}
	}
	if create == "" {
		return nil, snapshotDecodeError("flow QoS rule %q has no ACL creation command", target.RuleID)
	}
	create = normalizeDynamicACLTag(create)
	created, err := channel.runServiceChainCommands(ctx, operation, create)
	if err != nil {
		return nil, err
	}
	actualACLID, err := allocatedServiceChainACLID(created)
	if err != nil {
		return nil, err
	}
	apply := make([]string, 0, len(commands)+4)
	for _, raw := range commands {
		command := strings.TrimSpace(raw)
		plain := strings.TrimSpace(strings.TrimPrefix(command, "?"))
		if strings.HasPrefix(plain, "set acl-plugin acl ") {
			continue
		}
		command = strings.ReplaceAll(command, strconv.Itoa(stableACLID), strconv.Itoa(actualACLID))
		apply = append(apply, command)
	}
	apply = append(apply, "show acl-plugin interface")
	for _, attachment := range target.Attachments {
		apply = append(apply, "show interface "+flowAttachmentInterface(attachment))
	}
	applied, err := channel.runServiceChainCommands(ctx, operation, apply...)
	if err != nil {
		return nil, err
	}
	results = append(results, created...)
	results = append(results, applied...)
	aclOutput := resultStdoutLast(results, fmt.Sprintf("show acl-plugin acl index %d", actualACLID))
	action := "deny"
	if target.Kind == "vpp.behavior.rate" {
		action = "permit"
	}
	if err := verifyACLOutput(aclOutput, aclCandidateProof{numericID: actualACLID, id: target.RuleID, action: action, match: policyMatch(target.Match)}); err != nil {
		return nil, err
	}
	interfaceOutput := resultStdoutLast(results, "show acl-plugin interface")
	for _, attachment := range target.Attachments {
		interfaceName := flowAttachmentInterface(attachment)
		direction := flowAttachmentDirection(attachment)
		identity := resultStdoutLast(results, "show interface "+interfaceName)
		if !securityACLInterfaceAttached(interfaceOutput, identity, interfaceName, direction, actualACLID) {
			return nil, snapshotDecodeError("flow QoS rule %q ACL attachment is missing on %s", target.RuleID, interfaceName)
		}
	}
	if target.Kind == "vpp.behavior.rate" {
		if err := verifyPolicerResult(results, target); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func normalizeDynamicACLTag(command string) string {
	tagIndex := strings.LastIndex(command, " tag ")
	if tagIndex < 0 {
		return command
	}
	tag := command[tagIndex:]
	command = command[:tagIndex]
	for _, marker := range []string{" permit ", " deny ", " permit+reflect "} {
		if ruleIndex := strings.Index(command, marker); ruleIndex >= 0 {
			return command[:ruleIndex] + tag + command[ruleIndex:]
		}
	}
	return command + tag
}

func (channel vppctlChannel) removeFlowQoSTarget(ctx context.Context, operation Operation, target flow.Target, tag string) ([]VPPCTLCommandResult, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, "show acl-plugin acl")
	if err != nil {
		return nil, err
	}
	for _, id := range taggedServiceChainACLIDs(resultStdoutLast(results, "show acl-plugin acl"), tag) {
		commands := flowDetachACLCommands(target, id)
		commands = append(commands, fmt.Sprintf("?delete acl-plugin acl index %d", id))
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, commands...)
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	if target.Kind == "vpp.behavior.rate" {
		name := "ly_route_" + safeTag(target.RuleID)
		commands := flowDetachPolicerCommands(target, name)
		commands = append(commands, "?policer del name "+name)
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, commands...)
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	verified, err := channel.runServiceChainCommands(ctx, operation, "show acl-plugin acl")
	if err != nil {
		return nil, err
	}
	results = append(results, verified...)
	if len(taggedServiceChainACLIDs(resultStdoutLast(results, "show acl-plugin acl"), tag)) != 0 {
		return nil, snapshotDecodeError("flow QoS ACL tag %q remains after deletion", tag)
	}
	return results, nil
}

func flowDetachACLCommands(target flow.Target, aclID int) []string {
	commands := make([]string, 0, len(target.Attachments))
	for _, attachment := range target.Attachments {
		commands = append(commands, fmt.Sprintf("?set acl-plugin interface %s %s acl %d del", flowAttachmentInterface(attachment), flowAttachmentDirection(attachment), aclID))
	}
	return commands
}

func flowDetachPolicerCommands(target flow.Target, name string) []string {
	commands := make([]string, 0, len(target.Attachments))
	for _, attachment := range target.Attachments {
		commands = append(commands, fmt.Sprintf("?policer %s unapply name %s %s", flowAttachmentDirection(attachment), name, flowAttachmentInterface(attachment)))
	}
	return commands
}

func flowAttachmentDirection(attachment string) string {
	if strings.HasPrefix(attachment, "output:") {
		return "output"
	}
	return "input"
}

func flowAttachmentInterface(attachment string) string {
	return nativeLANInterface(strings.TrimPrefix(strings.TrimPrefix(attachment, "input:"), "output:"))
}

func dynamicQoSSnapshotCommands(request SnapshotRequest) []string {
	commands := qosSnapshotCommands(request)
	filtered := make([]string, 0, len(commands)+1)
	usesACL := false
	for _, group := range request.Candidates.QoS {
		usesACL = usesACL || flowGroupUsesACL(group)
	}
	for _, command := range commands {
		if usesACL && strings.HasPrefix(strings.TrimPrefix(command, "?"), "show acl-plugin acl index ") {
			continue
		}
		filtered = appendUnique(filtered, command)
	}
	if usesACL {
		filtered = appendUnique(filtered, "show acl-plugin acl")
	}
	return filtered
}
