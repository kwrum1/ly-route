package vpp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func (channel vppctlChannel) doSecurityACLLifecycle(ctx context.Context, operation Operation, acl trafficpolicy.SecurityACL, deleting bool) (Reply, error) {
	interfaceName, attachDirections := channel.securityACLTarget(operation, acl.Match.Direction)
	tag := "ly-route-" + safeTag(acl.ID)
	results, err := channel.removeSecurityACLState(ctx, operation, interfaceName, tag)
	if err != nil || deleting {
		return routePolicyLifecycleReply(operation, results), err
	}
	stableACLID := stableID("security-acl:"+acl.ID, 50000, 49999)
	create := ""
	for _, raw := range operation.VPPCtlCommands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		if strings.HasPrefix(command, "set acl-plugin acl ") {
			create = strings.Replace(command, fmt.Sprintf(" index %d", stableACLID), "", 1)
			break
		}
	}
	if create == "" {
		return Reply{}, snapshotDecodeError("security ACL %q has no creation command", acl.ID)
	}
	created, err := channel.runServiceChainCommands(ctx, operation, create)
	if err != nil {
		return Reply{}, err
	}
	actualACLID, err := allocatedServiceChainACLID(created)
	if err != nil {
		return Reply{}, err
	}
	attachCommands := []string{}
	for _, direction := range attachDirections {
		attachCommands = append(attachCommands, fmt.Sprintf("set acl-plugin interface %s %s acl %d", interfaceName, direction, actualACLID))
	}
	attachCommands = append(attachCommands,
		fmt.Sprintf("show acl-plugin acl index %d", actualACLID),
		"show acl-plugin interface",
		"show interface "+interfaceName)
	applied, err := channel.runServiceChainCommands(ctx, operation, attachCommands...)
	if err != nil {
		return Reply{}, err
	}
	results = append(results, created...)
	results = append(results, applied...)
	aclOutput := resultStdoutLast(results, fmt.Sprintf("show acl-plugin acl index %d", actualACLID))
	if err := verifyACLOutput(aclOutput, aclCandidateProof{numericID: actualACLID, id: acl.ID, action: acl.Action, match: acl.Match}); err != nil {
		return Reply{}, fmt.Errorf("%w: ACL output %q", err, aclOutput)
	}
	interfaceOutput := resultStdoutLast(results, "show acl-plugin interface")
	identityOutput := resultStdoutLast(results, "show interface "+interfaceName)
	for _, direction := range attachDirections {
		if !securityACLInterfaceAttached(interfaceOutput, identityOutput, interfaceName, direction, actualACLID) {
			return Reply{}, snapshotDecodeError("security ACL %q %s interface attachment is missing: %q", acl.ID, direction, interfaceOutput)
		}
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func (channel vppctlChannel) securityACLTarget(operation Operation, fallbackDirection string) (string, []string) {
	interfaceName := ""
	directions := make([]string, 0, 2)
	for _, raw := range operation.VPPCtlCommands {
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(raw, "?")))
		if len(fields) < 6 || fields[0] != "set" || fields[1] != "interface" || fields[3] != "acl" || fields[4] != "intfc" {
			continue
		}
		interfaceName = fields[5]
		if fields[2] == "input" || fields[2] == "output" {
			directions = appendUnique(directions, fields[2])
		}
	}
	if interfaceName == "" {
		interfaceName = strings.TrimSpace(channel.lanVPPInterface)
	}
	if interfaceName == "" {
		interfaceName = "lyroute-$LY_ROUTE_LAN_INTERFACE"
	}
	if len(directions) == 0 {
		directions = securityDirections(fallbackDirection)
	}
	return interfaceName, directions
}

func (channel vppctlChannel) removeSecurityACLState(ctx context.Context, operation Operation, interfaceName, tag string) ([]VPPCTLCommandResult, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, "show acl-plugin acl", "show acl-plugin interface")
	if err != nil {
		return nil, err
	}
	for _, id := range taggedServiceChainACLIDs(resultStdout(results, "show acl-plugin acl"), tag) {
		removeCommands := make([]string, 0, 3)
		for _, attachDirection := range []string{"input", "output"} {
			removeCommands = append(removeCommands, fmt.Sprintf("?set acl-plugin interface %s %s acl %d del", interfaceName, attachDirection, id))
		}
		removeCommands = append(removeCommands, fmt.Sprintf("delete acl-plugin acl index %d", id))
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, removeCommands...)
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	verified, err := channel.runServiceChainCommands(ctx, operation, "show acl-plugin acl", "show acl-plugin interface")
	if err != nil {
		return nil, err
	}
	results = append(results, verified...)
	if len(taggedServiceChainACLIDs(resultStdoutLast(results, "show acl-plugin acl"), tag)) != 0 {
		return nil, snapshotDecodeError("security ACL tag %q remains after deletion", tag)
	}
	return results, nil
}

func securityACLInterfaceAttached(output, identityOutput, interfaceName, direction string, aclID int) bool {
	for _, attachedID := range securityACLInterfaceIDs(output, identityOutput, interfaceName, direction) {
		if attachedID == aclID {
			return true
		}
	}
	return false
}

func securityACLInterfaceIDs(output, identityOutput, interfaceName, direction string) []int {
	if lan := strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")); lan != "" {
		interfaceName = strings.ReplaceAll(interfaceName, "$LY_ROUTE_LAN_INTERFACE", lan)
	}
	interfaceIndex := ""
	for _, line := range nonBlankLines(identityOutput) {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == interfaceName {
			if _, err := strconv.Atoi(fields[1]); err == nil {
				interfaceIndex = fields[1]
				break
			}
		}
	}
	if interfaceIndex == "" {
		return nil
	}
	currentInterface := ""
	result := []int{}
	for _, line := range nonBlankLines(output) {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "sw_if_index ") {
			currentInterface = strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(lower, "sw_if_index ")), ":")
			continue
		}
		if currentInterface != interfaceIndex || !strings.Contains(lower, direction) {
			continue
		}
		normalized := strings.NewReplacer("[", " ", "]", " ", ":", " ", ",", " ").Replace(line)
		for _, field := range strings.Fields(normalized) {
			if id, err := strconv.Atoi(field); err == nil {
				result = append(result, id)
			}
		}
	}
	return result
}
