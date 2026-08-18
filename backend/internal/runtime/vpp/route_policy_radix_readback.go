package vpp

import (
	"net/netip"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

type routePolicyRadixPlan struct {
	lanPrefix   netip.Prefix
	routeTarget string
	ruleCount   int
	skipNAT     bool
}

type preNATRoutePolicySummary struct {
	priority int
	prefixes int
	tableID  int
	fibIndex int
	skipNAT  bool
	bypass   bool
}

type preNATRouteHeader struct {
	enabled       int
	interfaceName string
	lanPrefix     string
	ruleCount     int
	radixNodes    int
}

func compileRoutePolicyRadixPlan(policy trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup, options routePolicyCommandOptions) (routePolicyRadixPlan, bool) {
	action := strings.ToLower(strings.TrimSpace(policy.Action))
	if action == "" {
		action = "route"
	}
	if (action != "route" && action != "nat") || len(options.localDestinations) == 0 {
		return routePolicyRadixPlan{}, false
	}
	lanPrefix, err := netip.ParsePrefix(strings.TrimSpace(options.localDestinations[0]))
	if err != nil || !lanPrefix.Addr().Is4() {
		return routePolicyRadixPlan{}, false
	}
	routeTarget := routePolicyTarget(policy, wanGroups)
	if strings.TrimSpace(routeTarget) == "" || strings.EqualFold(strings.TrimSpace(routeTarget), "local") {
		return routePolicyRadixPlan{}, false
	}
	sources, sourceErr := preNATIPv4Selectors(policy.Match.Sources)
	destinations, destinationErr := preNATIPv4Selectors(policy.Match.Destinations)
	sourcePorts, sourcePortErr := preNATPortRanges(policy.Match.SourcePorts)
	destinationPorts, destinationPortErr := preNATPortRanges(policy.Match.DestPorts)
	if sourceErr != nil || destinationErr != nil || sourcePortErr != nil || destinationPortErr != nil || len(sources) == 0 || len(destinations) == 0 {
		return routePolicyRadixPlan{}, false
	}
	lengths := []int{len(sources), len(destinations), len(preNATProtocols(policy.Match.Protocols)), len(sourcePorts), len(destinationPorts)}
	ruleCount := 1
	maxInt := int(^uint(0) >> 1)
	for _, length := range lengths {
		if length == 0 || ruleCount > maxInt/length {
			return routePolicyRadixPlan{}, false
		}
		ruleCount *= length
	}
	return routePolicyRadixPlan{
		lanPrefix:   lanPrefix.Masked(),
		routeTarget: routeTarget,
		ruleCount:   ruleCount,
		skipNAT:     policy.Path != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(policy.Path.VPPInterface)), "lypxin"),
	}, true
}

func preNATRoutePolicySummaryForID(output string, wantedID int) (preNATRoutePolicySummary, bool, error) {
	lines := nonBlankLines(output)
	if len(lines) == 0 {
		return preNATRoutePolicySummary{}, false, snapshotDecodeError("pre-NAT route inventory is empty")
	}
	if _, err := decodePreNATRouteHeader(lines[0]); err != nil {
		return preNATRoutePolicySummary{}, false, err
	}
	var matched *preNATRoutePolicySummary
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 15 || fields[0] != "rule" || fields[1] != "id" || fields[3] != "priority" || fields[5] != "prefixes" || fields[7] != "table" || fields[9] != "fib-index" || fields[11] != "skip-nat" || fields[13] != "bypass" {
			return preNATRoutePolicySummary{}, false, snapshotDecodeError("unknown pre-NAT route rule grammar %q", line)
		}
		values := make([]int, 0, 7)
		for _, index := range []int{2, 4, 6, 8, 10, 12, 14} {
			value, err := strconv.Atoi(fields[index])
			if err != nil || value < 0 {
				return preNATRoutePolicySummary{}, false, snapshotDecodeError("invalid pre-NAT route rule value %q", fields[index])
			}
			values = append(values, value)
		}
		if values[0] != wantedID {
			continue
		}
		if matched != nil {
			return preNATRoutePolicySummary{}, false, snapshotDecodeError("pre-NAT route policy %d is ambiguous", wantedID)
		}
		summary := preNATRoutePolicySummary{
			priority: values[1],
			prefixes: values[2],
			tableID:  values[3],
			fibIndex: values[4],
			skipNAT:  values[5] == 1,
			bypass:   values[6] == 1,
		}
		if values[5] > 1 || values[6] > 1 {
			return preNATRoutePolicySummary{}, false, snapshotDecodeError("pre-NAT route policy %d has invalid flags", wantedID)
		}
		matched = &summary
	}
	if matched == nil {
		return preNATRoutePolicySummary{}, false, nil
	}
	return *matched, true, nil
}

func verifyRoutePolicyRadixReadback(output string, request SnapshotRequest, policy trafficpolicy.RoutePolicy, policyID, tableID int, plan routePolicyRadixPlan) (bool, error) {
	lines := nonBlankLines(output)
	if len(lines) == 0 {
		return false, snapshotDecodeError("route policy %q pre-NAT readback is empty", policy.ID)
	}
	header, err := decodePreNATRouteHeader(lines[0])
	if err != nil {
		return false, snapshotDecodeError("route policy %q pre-NAT header is malformed", policy.ID)
	}
	if header.enabled != 1 || header.lanPrefix != plan.lanPrefix.String() {
		// A VPP restart can legitimately leave the classifier absent before the
		// reconciliation transaction has replayed the desired policy. Report it
		// as missing so an AllowMissing snapshot can rebuild it instead of
		// permanently blocking recovery.
		return false, nil
	}
	if ingress := strings.TrimSpace(request.LANVPPInterface); ingress != "" && header.interfaceName != ingress {
		return false, nil
	}
	summary, found, err := preNATRoutePolicySummaryForID(output, policyID)
	if err != nil || !found {
		return found, err
	}
	if summary.priority != vppABFPriority(policy.Priority) || summary.prefixes != plan.ruleCount || summary.tableID != tableID || summary.fibIndex < 0 || summary.skipNAT != plan.skipNAT || summary.bypass {
		if request.AllowMissing {
			// A production recovery may find a classifier emitted by an older
			// command layout. Treat that stale object as absent so the desired
			// transaction can replace it atomically.
			return false, nil
		}
		return false, snapshotDecodeError("route policy %q pre-NAT summary does not match candidate", policy.ID)
	}
	return true, nil
}

func decodePreNATRouteHeader(line string) (preNATRouteHeader, error) {
	fields := strings.Fields(line)
	if len(fields) < 10 || fields[0] != "enabled" || fields[2] != "interface" {
		return preNATRouteHeader{}, snapshotDecodeError("unknown pre-NAT route header grammar %q", line)
	}
	lanPrefixIndex := 4
	if len(fields) > lanPrefixIndex && strings.HasPrefix(fields[lanPrefixIndex], "(") && strings.HasSuffix(fields[lanPrefixIndex], ")") {
		lanPrefixIndex++
	}
	if len(fields) != lanPrefixIndex+6 || fields[lanPrefixIndex] != "lan-prefix" || fields[lanPrefixIndex+2] != "rules" || fields[lanPrefixIndex+4] != "radix-nodes" {
		return preNATRouteHeader{}, snapshotDecodeError("unknown pre-NAT route header grammar %q", line)
	}
	enabled, err := strconv.Atoi(fields[1])
	if err != nil {
		return preNATRouteHeader{}, snapshotDecodeError("invalid pre-NAT route enabled value %q", fields[1])
	}
	ruleCount, err := strconv.Atoi(fields[lanPrefixIndex+3])
	if err != nil {
		return preNATRouteHeader{}, snapshotDecodeError("invalid pre-NAT route rule count %q", fields[lanPrefixIndex+3])
	}
	radixNodes, err := strconv.Atoi(fields[lanPrefixIndex+5])
	if err != nil {
		return preNATRouteHeader{}, snapshotDecodeError("invalid pre-NAT route radix count %q", fields[lanPrefixIndex+5])
	}
	return preNATRouteHeader{
		enabled:       enabled,
		interfaceName: fields[3],
		lanPrefix:     fields[lanPrefixIndex+1],
		ruleCount:     ruleCount,
		radixNodes:    radixNodes,
	}, nil
}
