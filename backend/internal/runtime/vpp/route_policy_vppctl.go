package vpp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

type routePolicyVPPCTLSpec struct {
	policyID int
	aclID    int
	tableID  int
	priority int
	tag      string
	ingress  string
	via      string
	acl      string
	apply    bool
}

func (channel vppctlChannel) doRoutePolicyLifecycle(ctx context.Context, operation Operation) (Reply, error) {
	spec, err := parseRoutePolicyVPPCTLSpec(operation)
	if err != nil {
		return Reply{}, err
	}
	results, err := channel.removeRoutePolicyState(ctx, operation, spec)
	if err != nil || !spec.apply {
		return routePolicyLifecycleReply(operation, results), err
	}
	created, err := channel.runServiceChainCommands(ctx, operation, routePolicyCreateACLCommand(spec))
	if err != nil {
		return Reply{}, err
	}
	actualACLID, err := allocatedServiceChainACLID(created)
	if err != nil {
		return Reply{}, err
	}
	commands := make([]string, 0, len(operation.VPPCtlCommands)-1)
	for _, raw := range operation.VPPCtlCommands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		command = strings.ReplaceAll(command, "$LY_ROUTE_LAN_INTERFACE", strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")))
		switch {
		case strings.HasPrefix(command, "set acl-plugin acl "):
			continue
		case strings.HasPrefix(command, "abf policy add "):
			command = strings.Replace(command, fmt.Sprintf(" acl %d ", spec.aclID), fmt.Sprintf(" acl %d ", actualACLID), 1)
		case command == fmt.Sprintf("show acl-plugin acl index %d", spec.aclID):
			command = fmt.Sprintf("show acl-plugin acl index %d", actualACLID)
		}
		commands = append(commands, command)
	}
	applied, err := channel.runServiceChainCommands(ctx, operation, commands...)
	if err != nil {
		return Reply{}, err
	}
	results = append(results, created...)
	results = append(results, applied...)
	policyOutput := resultStdoutLast(results, fmt.Sprintf("show abf policy %d", spec.policyID))
	actualObservedACL, present := observedABFACLID(policyOutput)
	if !present || actualObservedACL != actualACLID {
		return Reply{}, snapshotDecodeError("route policy %q ABF readback does not reference allocated ACL %d", operation.Resource, actualACLID)
	}
	aclOutput := resultStdoutLast(results, fmt.Sprintf("show acl-plugin acl index %d", actualACLID))
	if !strings.Contains(aclOutput, "tag {"+spec.tag+"}") {
		return Reply{}, snapshotDecodeError("route policy %q ACL tag readback is missing: %q", operation.Resource, aclOutput)
	}
	attachOutput := resultStdoutLast(results, "show abf attach "+spec.ingress)
	if !routePolicyAttached(attachOutput, spec.policyID) {
		return Reply{}, snapshotDecodeError("route policy %q ABF attachment readback is missing: %q", operation.Resource, attachOutput)
	}
	if strings.TrimSpace(resultStdoutLast(results, fmt.Sprintf("show ip fib table %d", spec.tableID))) == "" {
		return Reply{}, snapshotDecodeError("route policy %q private FIB readback is empty", operation.Resource)
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func (channel vppctlChannel) removeRoutePolicyState(ctx context.Context, operation Operation, spec routePolicyVPPCTLSpec) ([]VPPCTLCommandResult, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("show abf policy %d", spec.policyID), "show acl-plugin acl")
	if err != nil {
		return nil, err
	}
	policyOutput := resultStdout(results, fmt.Sprintf("show abf policy %d", spec.policyID))
	if actualACLID, present := observedABFACLID(policyOutput); present {
		via := observedABFDeleteVia(policyOutput)
		if via == "" {
			via = spec.via
		}
		if via == "" {
			return nil, snapshotDecodeError("route policy %q live ABF path cannot be removed safely", operation.Resource)
		}
		removed, removeErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("abf attach ip4 del policy %d %s", spec.policyID, spec.ingress),
			fmt.Sprintf("abf policy del id %d acl %d via %s", spec.policyID, actualACLID, via),
			fmt.Sprintf("delete acl-plugin acl index %d", actualACLID))
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	for _, id := range taggedServiceChainACLIDs(resultStdout(results, "show acl-plugin acl"), spec.tag) {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("delete acl-plugin acl index %d", id))
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	cleaned, err := channel.runServiceChainCommands(ctx, operation,
		fmt.Sprintf("ip table del %d", spec.tableID),
		fmt.Sprintf("show abf policy %d", spec.policyID),
		"show acl-plugin acl",
		"show abf attach "+spec.ingress,
		fmt.Sprintf("show ip fib table %d", spec.tableID))
	if err != nil {
		return nil, err
	}
	results = append(results, cleaned...)
	if _, present := observedABFACLID(resultStdoutLast(results, fmt.Sprintf("show abf policy %d", spec.policyID))); present {
		return nil, snapshotDecodeError("route policy %q ABF policy remains after deletion", operation.Resource)
	}
	if len(taggedServiceChainACLIDs(resultStdoutLast(results, "show acl-plugin acl"), spec.tag)) != 0 {
		return nil, snapshotDecodeError("route policy %q tagged ACL remains after deletion", operation.Resource)
	}
	if routePolicyAttached(resultStdoutLast(results, "show abf attach "+spec.ingress), spec.policyID) {
		return nil, snapshotDecodeError("route policy %q remains attached after deletion", operation.Resource)
	}
	if strings.TrimSpace(resultStdoutLast(results, fmt.Sprintf("show ip fib table %d", spec.tableID))) != "" {
		return nil, snapshotDecodeError("route policy %q private FIB remains after deletion", operation.Resource)
	}
	return results, nil
}

func parseRoutePolicyVPPCTLSpec(operation Operation) (routePolicyVPPCTLSpec, error) {
	id := strings.TrimSpace(operation.Resource)
	if id == "" {
		return routePolicyVPPCTLSpec{}, snapshotDecodeError("route policy resource is empty")
	}
	spec := routePolicyVPPCTLSpec{
		policyID: stableID("route-abf:"+id, 10000, 8999),
		aclID:    stableID("route-acl:"+id, 10000, 49999),
		tableID:  stableID("route-table:"+id, 50000, 49999),
		tag:      "ly-route-" + safeTag(id),
		ingress:  "lyroute-$LY_ROUTE_LAN_INTERFACE",
	}
	if policy, ok := operation.Payload.(trafficpolicy.RoutePolicy); ok {
		spec.via = routeNextHop(policy)
	}
	for _, raw := range operation.VPPCtlCommands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		switch {
		case strings.HasPrefix(command, "set acl-plugin acl index "):
			spec.acl = command
		case strings.HasPrefix(command, "abf policy add "):
			spec.apply = true
			if index := strings.Index(command, " via "); index >= 0 {
				spec.via = strings.TrimSpace(command[index+5:])
			}
		case strings.HasPrefix(command, "abf attach ip4 policy "):
			fields := strings.Fields(command)
			if len(fields) == 8 {
				spec.priority, _ = strconv.Atoi(fields[6])
				spec.ingress = fields[7]
			}
		}
	}
	if spec.apply && spec.acl == "" {
		return routePolicyVPPCTLSpec{}, snapshotDecodeError("route policy %q has no ACL command", id)
	}
	spec.ingress = strings.ReplaceAll(spec.ingress, "$LY_ROUTE_LAN_INTERFACE", strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")))
	return spec, nil
}

func routePolicyCreateACLCommand(spec routePolicyVPPCTLSpec) string {
	command := strings.Replace(spec.acl, fmt.Sprintf(" index %d", spec.aclID), "", 1)
	// VPP parses the ACL tag before the rule list. The declarative command map
	// retains tags at the tail for readability, so normalize it for allocation.
	tagIndex := strings.LastIndex(command, " tag ")
	if tagIndex < 0 {
		return command
	}
	tag := command[tagIndex:]
	command = command[:tagIndex]
	for _, marker := range []string{" permit ", " deny "} {
		if ruleIndex := strings.Index(command, marker); ruleIndex >= 0 {
			return command[:ruleIndex] + tag + command[ruleIndex:]
		}
	}
	return command + tag
}

func observedABFACLID(output string) (int, bool) {
	lines := nonBlankLines(output)
	if len(lines) == 0 || strings.Contains(strings.ToLower(lines[0]), "invalid policy") {
		return 0, false
	}
	for _, field := range strings.Fields(lines[0]) {
		if strings.HasPrefix(field, "acl:") {
			id, err := strconv.Atoi(strings.TrimPrefix(field, "acl:"))
			return id, err == nil
		}
	}
	return 0, false
}

func observedABFDeleteVia(output string) string {
	for _, line := range nonBlankLines(output) {
		fields := strings.Fields(line)
		if len(fields) >= 5 && strings.HasPrefix(fields[0], "[@") && fields[2] == "via" {
			return fields[3] + " " + strings.TrimRight(fields[4], ",:")
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "lookup") && strings.Contains(lower, "table:") {
			for _, field := range fields {
				if strings.HasPrefix(field, "table:") {
					if _, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(field, "table:"), ",")); err == nil {
						return "ip4-lookup-in-table " + strings.TrimSuffix(strings.TrimPrefix(field, "table:"), ",")
					}
				}
			}
		}
	}
	if strings.Contains(output, "dpo-receive") || strings.Contains(output, "receive:") {
		return "local"
	}
	return ""
}

func routePolicyAttached(output string, policyID int) bool {
	for _, field := range strings.Fields(output) {
		if field == "policy:"+strconv.Itoa(policyID) {
			return true
		}
	}
	return false
}

func routePolicyLifecycleReply(operation Operation, results []VPPCTLCommandResult) Reply {
	return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}
}
