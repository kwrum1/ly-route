package vpp

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"regexp"
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
	var results []VPPCTLCommandResult
	if !strings.HasSuffix(operation.Name, ".replay") {
		results, err = channel.removeRoutePolicyState(ctx, operation, spec)
		if err != nil || !spec.apply {
			return routePolicyLifecycleReply(operation, results), err
		}
	} else if !spec.apply {
		return routePolicyLifecycleReply(operation, nil), nil
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
	results, err := channel.runServiceChainCommands(ctx, operation,
		fmt.Sprintf("show abf policy %d", spec.policyID),
		"show ip fib summary")
	if err != nil {
		return nil, err
	}
	policyOutput := resultStdout(results, fmt.Sprintf("show abf policy %d", spec.policyID))
	if actualACLID, present := observedABFACLID(policyOutput); present {
		via := routePolicyABFDeleteVia(spec, policyOutput, resultStdout(results, "show ip fib summary"))
		if via == "" {
			return nil, snapshotDecodeError("route policy %q live ABF path cannot be removed safely", operation.Resource)
		}
		detached, detachErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("abf attach ip4 del policy %d %s", spec.policyID, spec.ingress),
			"show abf attach "+spec.ingress)
		if detachErr != nil {
			return nil, detachErr
		}
		results = append(results, detached...)
		if routePolicyAttached(resultStdoutLast(results, "show abf attach "+spec.ingress), spec.policyID) {
			return nil, snapshotDecodeError("route policy %q remains attached; refusing unsafe policy deletion", operation.Resource)
		}
		removed, removeErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("abf policy del id %d acl %d via %s", spec.policyID, actualACLID, via),
			fmt.Sprintf("show abf policy %d", spec.policyID))
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
		if _, present := observedABFACLID(resultStdoutLast(results, fmt.Sprintf("show abf policy %d", spec.policyID))); present {
			return nil, snapshotDecodeError("route policy %q remains after deletion; refusing referenced ACL deletion", operation.Resource)
		}
		aclRemoved, aclErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("delete acl-plugin acl index %d", actualACLID),
			fmt.Sprintf("show acl-plugin acl index %d", actualACLID))
		if aclErr != nil {
			return nil, aclErr
		}
		results = append(results, aclRemoved...)
		if strings.TrimSpace(resultStdoutLast(results, fmt.Sprintf("show acl-plugin acl index %d", actualACLID))) != "" {
			return nil, snapshotDecodeError("route policy %q ACL %d remains after deletion", operation.Resource, actualACLID)
		}
	}
	inventory, err := channel.runServiceChainCommands(ctx, operation, "show acl-plugin acl")
	if err != nil {
		return nil, err
	}
	results = append(results, inventory...)
	for _, id := range taggedServiceChainACLIDs(resultStdoutLast(results, "show acl-plugin acl"), spec.tag) {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("delete acl-plugin acl index %d", id))
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	cleaned, err := channel.runServiceChainCommands(ctx, operation,
		// VPP 25.x can retain a recursive-resolution lock when a private FIB
		// still owns its default/sentinel paths.  Remove those owned paths
		// explicitly before asking VPP to destroy the table; otherwise an
		// apparently successful `ip table del` leaves a ghost FIB behind.
		fmt.Sprintf("?ip route del table %d 0.0.0.0/0", spec.tableID),
		fmt.Sprintf("?ip route del table %d 0.0.0.0/32", spec.tableID),
		fmt.Sprintf("?ip table del %d", spec.tableID),
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

func routePolicyABFDeleteVia(spec routePolicyVPPCTLSpec, policyOutput, fibSummary string) string {
	if via := observedABFDeleteVia(policyOutput); via != "" {
		return via
	}
	// VPP 25.x normally prints only the internal fib-index for a recursive
	// ABF path. Resolve that index against the live FIB inventory. Never fall
	// back to the declarative table ID: a partial rollback can remove that
	// table while leaving an ABF object behind, and asking VPP to delete the
	// policy through a nonexistent table can crash the 25.10 ABF plugin.
	if fibIndex, ok := observedABFFibIndex(policyOutput); ok {
		if tableID, ok := ipv4TableIDForFibIndex(fibSummary, fibIndex); ok {
			return fmt.Sprintf("ip4-lookup-in-table %d", tableID)
		}
		return ""
	}
	if tableID, ok := routePolicyLookupTableID(spec.via); ok && ipv4FibSummaryContainsTable(fibSummary, tableID) {
		return strings.TrimSpace(spec.via)
	}
	return ""
}

var routePolicyFibIndexPattern = regexp.MustCompile("\\bfib-index:\\s*([0-9]+)\\b")
var ipv4FibSummaryPattern = regexp.MustCompile("(?m)^ipv4-VRF:([0-9]+),\\s*fib_index:([0-9]+),")

func observedABFFibIndex(output string) (int, bool) {
	match := routePolicyFibIndexPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return 0, false
	}
	index, err := strconv.Atoi(match[1])
	return index, err == nil
}

func ipv4TableIDForFibIndex(summary string, wanted int) (int, bool) {
	for _, match := range ipv4FibSummaryPattern.FindAllStringSubmatch(summary, -1) {
		tableID, tableErr := strconv.Atoi(match[1])
		fibIndex, fibErr := strconv.Atoi(match[2])
		if tableErr == nil && fibErr == nil && fibIndex == wanted {
			return tableID, true
		}
	}
	return 0, false
}

func ipv4FibSummaryContainsTable(summary string, wanted int) bool {
	for _, match := range ipv4FibSummaryPattern.FindAllStringSubmatch(summary, -1) {
		tableID, err := strconv.Atoi(match[1])
		if err == nil && tableID == wanted {
			return true
		}
	}
	return false
}

func routePolicyLookupTableID(via string) (int, bool) {
	fields := strings.Fields(strings.TrimSpace(via))
	if len(fields) != 2 || fields[0] != "ip4-lookup-in-table" {
		return 0, false
	}
	tableID, err := strconv.Atoi(fields[1])
	return tableID, err == nil
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
	// New production plans rewrite the placeholder before execution.  Older
	// persisted cleanup operations may still carry it, so resolve both the
	// legacy Linux-interface variable and the explicit VPP-interface variable.
	if vppIngress := strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_VPP_INTERFACE")); vppIngress != "" {
		spec.ingress = strings.ReplaceAll(spec.ingress, "lyroute-$LY_ROUTE_LAN_INTERFACE", vppIngress)
	}
	spec.ingress = strings.ReplaceAll(spec.ingress, "$LY_ROUTE_LAN_INTERFACE", strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")))
	if spec.ingress == "" || spec.ingress == "lyroute-" || strings.Contains(spec.ingress, "$LY_ROUTE_LAN_INTERFACE") {
		return routePolicyVPPCTLSpec{}, snapshotDecodeError("route policy %q has no resolved LAN VPP ingress interface", id)
	}
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
	attachedNextHop := false
	for _, line := range nonBlankLines(output) {
		fields := strings.Fields(line)
		if attachedNextHop {
			attachedNextHop = false
			if len(fields) >= 2 {
				nextHop := strings.TrimRight(fields[0], ",:")
				interfaceName := strings.TrimRight(fields[1], ",:")
				address, err := netip.ParseAddr(nextHop)
				if err == nil && address.Is4() && interfaceNameSafe(interfaceName) {
					return address.String() + " " + interfaceName
				}
			}
		}
		if len(fields) >= 5 && strings.HasPrefix(fields[0], "[@") && fields[2] == "via" {
			return fields[3] + " " + strings.TrimRight(fields[4], ",:")
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "attached-nexthop:") {
			attachedNextHop = true
			continue
		}
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
