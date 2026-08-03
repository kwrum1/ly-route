package vpp

import (
	"fmt"
	"strings"
)

func decodeVPPCTLReadback(operation Operation, results []VPPCTLCommandResult) (any, error) {
	if !strings.HasSuffix(operation.Name, ".snapshot") {
		return nil, nil
	}
	request, ok := operation.Payload.(SnapshotRequest)
	if !ok {
		return nil, snapshotDecodeError("snapshot request payload has type %T", operation.Payload)
	}
	switch SnapshotCapability(operation.Resource) {
	case SnapshotCapabilityInterfaces:
		return decodeVPPCTLInterfaces(request, results)
	case SnapshotCapabilityBonds:
		return decodeVPPCTLBonds(request, results)
	case SnapshotCapabilityRoutePolicies:
		return decodeVPPCTLRoutes(request, results)
	case SnapshotCapabilityWANGroups:
		return decodeVPPCTLWANGroups(request, results)
	case SnapshotCapabilityACLs:
		return decodeVPPCTLACLs(request, results)
	case SnapshotCapabilityQoS:
		return decodeVPPCTLQoS(request, results)
	case SnapshotCapabilityNAT44:
		return decodeVPPCTLNAT44(request, results)
	default:
		return nil, snapshotDecodeError("unsupported production capability %q", operation.Resource)
	}
}

func unwrapVPPCTLReadback(payload any) any {
	if envelope, ok := payload.(VPPCTLReplyPayload); ok {
		return envelope.Readback
	}
	return payload
}

func snapshotDecodeError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSnapshotIncomplete, fmt.Sprintf(format, args...))
}

func commandOutput(results []VPPCTLCommandResult, command string) (string, error) {
	var output string
	matches := 0
	for _, result := range results {
		if result.Command != command {
			continue
		}
		matches++
		output = result.Stdout
		if result.Retval != 0 {
			return "", snapshotDecodeError("command %q returned retval %d", command, result.Retval)
		}
	}
	if matches != 1 {
		return "", snapshotDecodeError("command %q returned %d result rows", command, matches)
	}
	if strings.TrimSpace(output) == "" {
		return "", snapshotDecodeError("command %q returned incomplete output", command)
	}
	return output, nil
}

func routeSnapshotCommands(request SnapshotRequest) []string {
	ids := append(append([]string(nil), request.RoutePolicies...), request.AbsentRoutePolicies...)
	commands := make([]string, 0, 1+len(ids)*2)
	commands = append(commands, "show acl-plugin acl")
	for _, id := range ids {
		id = strings.TrimSpace(id)
		commands = appendUnique(commands,
			fmt.Sprintf("show abf policy %d", stableID("route-abf:"+id, 10000, 8999)),
			fmt.Sprintf("show ip fib table %d", stableID("route-table:"+id, 50000, 49999)),
		)
	}
	for _, group := range request.Candidates.WANGroups {
		commands = appendUnique(commands, fmt.Sprintf("?show ip fib table %d", wanGroupTableID(group.ID)))
	}
	return commands
}

func routeSnapshotCommandsLegacy(request SnapshotRequest) []string {
	ids := append(append([]string(nil), request.RoutePolicies...), request.AbsentRoutePolicies...)
	commands := make([]string, 0, len(ids)*3)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		commands = appendUnique(commands,
			fmt.Sprintf("show acl-plugin acl index %d", stableID("route-acl:"+id, 10000, 49999)),
			fmt.Sprintf("show abf policy %d", stableID("route-abf:"+id, 10000, 8999)),
			fmt.Sprintf("show ip fib table %d", stableID("route-table:"+id, 50000, 49999)),
		)
	}
	return commands
}

func wanGroupSnapshotCommands(request SnapshotRequest) []string {
	ids := append(append([]string(nil), request.WANGroups...), request.AbsentWANGroups...)
	commands := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		commands = appendUnique(commands, fmt.Sprintf("show ip fib table %d", wanGroupTableID(id)))
	}
	if len(request.WANGroups) > 0 {
		commands = appendUnique(commands, "?show fib path-lists")
	}
	return commands
}

func aclSnapshotCommands(request SnapshotRequest) []string {
	ids := append(append([]string(nil), request.ACLs...), request.AbsentACLs...)
	commands := make([]string, 0, len(ids))
	for _, id := range ids {
		commands = appendUnique(commands, fmt.Sprintf("show acl-plugin acl index %d", stableID("security-acl:"+strings.TrimSpace(id), 50000, 49999)))
	}
	return commands
}

func qosSnapshotCommands(request SnapshotRequest) []string {
	var commands []string
	for _, group := range request.Candidates.QoS {
		for _, command := range flowGroupCommands(group) {
			command = strings.TrimSpace(strings.TrimPrefix(command, "?"))
			if strings.HasPrefix(command, "show ") {
				commands = appendUnique(commands, command)
			}
		}
	}
	return commands
}

func appendUnique(commands []string, additions ...string) []string {
	for _, addition := range additions {
		found := false
		for _, command := range commands {
			if command == addition {
				found = true
				break
			}
		}
		if !found {
			commands = append(commands, addition)
		}
	}
	return commands
}
