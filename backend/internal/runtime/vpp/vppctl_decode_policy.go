package vpp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

var errABFPathDrift = errors.New("ABF path drift")

func snapshotABFPathDriftError(format string, args ...any) error {
	return fmt.Errorf("%w: %w: %s", ErrSnapshotIncomplete, errABFPathDrift, fmt.Sprintf(format, args...))
}

type fibPath struct {
	via        string
	weight     int
	preference int
}

type abfCandidateProof struct {
	policyID int
	aclID    int
	via      string
}

func verifyRoutePolicyAbsence(results []VPPCTLCommandResult, id string) error {
	policyID := stableID("route-abf:"+id, 10000, 8999)
	if hasVPPCTLCommand(results, "show ly-route pre-nat-route") {
		output, err := commandOutput(results, "show ly-route pre-nat-route")
		if err != nil {
			return err
		}
		if _, found, err := preNATRoutePolicySummaryForID(output, policyID); err != nil {
			return err
		} else if found {
			return snapshotDecodeError("deleted route policy %q pre-NAT classifier remains", id)
		}
	}
	if _, _, found, err := taggedACLOutput(results, "ly-route-"+safeTag(id)); err != nil {
		return err
	} else if found {
		return snapshotDecodeError("deleted route policy %q tagged ACL remains", id)
	}
	policyOutput, err := vppctlOutputAllowEmpty(results, fmt.Sprintf("show abf policy %d", policyID))
	if err != nil {
		return err
	}
	if _, present := observedABFACLID(policyOutput); present {
		return snapshotDecodeError("deleted route policy %q ABF policy remains", id)
	}
	tableID := stableID("route-table:"+id, 50000, 49999)
	fibOutput, err := vppctlOutputAllowEmpty(results, fmt.Sprintf("show ip fib table %d", tableID))
	if err != nil {
		return err
	}
	if strings.TrimSpace(fibOutput) != "" {
		return snapshotDecodeError("deleted route policy %q private FIB remains", id)
	}
	return nil
}

func decodeVPPCTLWANGroups(request SnapshotRequest, results []VPPCTLCommandResult) (WANGroupReadback, error) {
	candidates := make(map[string]trafficpolicy.WANGroup, len(request.Candidates.WANGroups))
	for _, candidate := range request.Candidates.WANGroups {
		if _, duplicate := candidates[candidate.ID]; duplicate || strings.TrimSpace(candidate.ID) == "" {
			return WANGroupReadback{}, snapshotDecodeError("WAN group candidate %q is missing or ambiguous", candidate.ID)
		}
		candidates[candidate.ID] = candidate
	}
	if err := requirePolicyCandidateNames(request.WANGroups, candidates, "WAN group"); err != nil {
		return WANGroupReadback{}, err
	}
	groups := make([]trafficpolicy.WANGroup, 0, len(request.WANGroups))
	configuredPathLists, configuredPathListsErr := parseConfiguredFIBPathLists(results)
	for _, id := range request.WANGroups {
		candidate := candidates[strings.TrimSpace(id)]
		observedCandidate, err := resolveRuntimePPPoEWANGroup(candidate)
		if err != nil {
			return WANGroupReadback{}, err
		}
		paths, err := parseWANGroupFIBResult(results, wanGroupTableID(candidate.ID))
		if err != nil {
			if request.AllowMissing && wanGroupFIBIsAbsent(results, candidate.ID) {
				continue
			}
			return WANGroupReadback{}, err
		}
		if len(paths) == 0 {
			if request.AllowMissing {
				// VPP keeps an empty table with a default drop DPO after a
				// restart or an interrupted transaction. It is drift that the
				// next apply can repair, not a valid active group.
				continue
			}
			return WANGroupReadback{}, snapshotDecodeError("WAN group %q active path count does not match candidate", candidate.ID)
		}
		if observedCandidate.Mode != trafficpolicy.WANGroupPrimaryBackup && len(paths) != len(observedCandidate.Members) {
			if request.AllowMissing {
				continue
			}
			return WANGroupReadback{}, snapshotDecodeError("WAN group %q active path count does not match candidate", candidate.ID)
		}
		live := make([]fibPath, 0, len(paths))
		for _, path := range paths {
			live = append(live, path)
		}
		activeMatches := 0
		for _, member := range observedCandidate.Members {
			expectedPath := observedCandidate.Paths[member]
			for _, path := range live {
				if activeWANPathMatches(path, expectedPath, member) {
					activeMatches++
					break
				}
			}
		}
		if observedCandidate.Mode == trafficpolicy.WANGroupPrimaryBackup {
			if activeMatches != 1 {
				if request.AllowMissing {
					continue
				}
				return WANGroupReadback{}, snapshotDecodeError("WAN group %q active primary/backup path does not match candidate", candidate.ID)
			}
		} else if activeMatches != len(observedCandidate.Members) {
			if request.AllowMissing {
				continue
			}
			return WANGroupReadback{}, snapshotDecodeError("WAN group %q active members do not match candidate", candidate.ID)
		}
		if configuredPathListsErr == nil && !configuredWANPathListMatches(observedCandidate, configuredPathLists) {
			if request.AllowMissing {
				continue
			}
			return WANGroupReadback{}, snapshotDecodeError("WAN group %q configured weights or preferences do not match candidate", candidate.ID)
		}
		groups = append(groups, candidate)
	}
	for _, id := range request.AbsentWANGroups {
		commands := wanGroupSnapshotCommands(SnapshotRequest{AbsentWANGroups: []string{id}})
		if err := verifyVPPCTLAbsence(results, commands, "WAN group", id); err != nil {
			return WANGroupReadback{}, err
		}
	}
	return WANGroupReadback{Groups: groups}, nil
}

func resolveRuntimePPPoEWANGroup(candidate trafficpolicy.WANGroup) (trafficpolicy.WANGroup, error) {
	if len(candidate.Paths) == 0 {
		return candidate, nil
	}
	paths := make(map[string]trafficpolicy.WANPath, len(candidate.Paths))
	for member, path := range candidate.Paths {
		resolved, err := resolveRuntimePPPoEInterface(path.VPPInterface)
		if err != nil {
			return trafficpolicy.WANGroup{}, fmt.Errorf("resolve WAN group %q member %q: %w", candidate.ID, member, err)
		}
		path.VPPInterface = resolved
		paths[member] = path
	}
	candidate.Paths = paths
	return candidate, nil
}

func wanGroupFIBIsAbsent(results []VPPCTLCommandResult, id string) bool {
	output, err := commandOutputAllowEmpty(results, fmt.Sprintf("show ip fib table %d", wanGroupTableID(id)))
	return err == nil && strings.TrimSpace(output) == ""
}

func activeWANPathMatches(path fibPath, expected trafficpolicy.WANPath, member string) bool {
	expectedVia := strings.TrimSpace(expected.VPPInterface)
	if expectedVia == "" {
		expectedVia = strings.TrimSpace(member)
	}
	if expectedVia == "" || !strings.Contains(path.via, expectedVia) {
		return false
	}
	if expected.NextHop == "" || strings.Contains(path.via, expected.NextHop) {
		return true
	}

	// VPP resolves a point-to-point PPPoE next hop to 0.0.0.0 in the
	// forwarding DPO. The configured path-list is verified separately and
	// retains the negotiated peer address.
	return strings.HasPrefix(strings.ToLower(expectedVia), "pppoe_session") &&
		strings.Contains(path.via, "0.0.0.0 "+expectedVia)
}

func verifyABFPolicy(results []VPPCTLCommandResult, proof abfCandidateProof) error {
	output, err := commandOutput(results, fmt.Sprintf("show abf policy %d", proof.policyID))
	if err != nil {
		return err
	}
	lines := nonBlankLines(output)
	if len(lines) < 2 {
		return snapshotDecodeError("ABF policy %d output is truncated", proof.policyID)
	}
	header := strings.Fields(lines[0])
	if len(header) != 3 || !strings.HasPrefix(header[0], "abf:[") || header[1] != fmt.Sprintf("policy:%d", proof.policyID) || header[2] != fmt.Sprintf("acl:%d", proof.aclID) {
		return snapshotDecodeError("ABF policy %d output does not match candidate", proof.policyID)
	}
	runtimeIndex := strings.TrimSuffix(strings.TrimPrefix(header[0], "abf:["), "]:")
	if _, err := strconv.Atoi(runtimeIndex); err != nil {
		return snapshotDecodeError("malformed ABF runtime index %q", runtimeIndex)
	}
	foundPath := false
	explicitPath := false
	var fallbackPath string
	expectedFibIndex := abfTableFibIndex(results, proof.via)
	expectedFields := strings.Fields(proof.via)
	var expectedInterfaceIndex int
	var expectedInterfaceIndexKnown bool
	interfaceInventoryPresent := false
	for _, result := range results {
		if result.Command == "show interface" {
			interfaceInventoryPresent = true
			break
		}
	}
	if len(expectedFields) == 2 && expectedFields[0] != "table" {
		expectedInterfaceIndex, expectedInterfaceIndexKnown = securityGenerationInterfaceIndex(resultStdoutLast(results, "show interface"), expectedFields[1])
	}
	unresolvedPath := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if unresolvedPath {
			if trimmed != "unresolved" {
				return snapshotDecodeError("ABF policy %d unresolved path has unexpected continuation %q", proof.policyID, line)
			}
			if interfaceInventoryPresent {
				// Post-apply readback carries an interface inventory. At that
				// point an unresolved path is not usable forwarding evidence.
				return snapshotABFPathDriftError("ABF policy %d path is unresolved", proof.policyID)
			}
			// A prepare snapshot has no interface inventory. Recognize this
			// stale grammar so reconciliation can remove/rebuild it instead of
			// being permanently blocked by the broken live object.
			unresolvedPath = false
			continue
		}
		// VPP prints a resolved service/next-hop path followed by a nested
		// DPO description. The nested DPO repeats the same `via` text but is
		// not a second ABF path. Prefer the declarative path line; older VPP
		// output sometimes omits that line and leaves only the nested DPO, so
		// retain one matching nested line as a compatibility fallback.
		if strings.HasPrefix(trimmed, "[@") {
			if !explicitPath && strings.Contains(trimmed, "via "+proof.via) {
				if fallbackPath != "" && fallbackPath != trimmed {
					return snapshotABFPathDriftError("ABF policy %d path is ambiguous", proof.policyID)
				}
				fallbackPath = trimmed
			}
			continue
		}
		if trimmed == "stacked-on:" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "path-list:["), strings.HasPrefix(trimmed, "path:["):
			continue
		case expectedFibIndex != "" && strings.TrimSpace(line) == "fib-index:"+expectedFibIndex:
			if explicitPath {
				return snapshotABFPathDriftError("ABF policy %d path is ambiguous", proof.policyID)
			}
			fallbackPath = ""
			foundPath = true
		case trimmed == proof.via || strings.HasPrefix(trimmed, proof.via+" "):
			// VPP 25.x renders a resolved point-to-point next hop as
			// "<next-hop> <interface> (p2p)". Older versions printed only
			// the two declarative tokens.
			if explicitPath {
				return snapshotDecodeError("ABF policy %d path is ambiguous", proof.policyID)
			}
			fallbackPath = ""
			explicitPath = true
			foundPath = true
		case len(expectedFields) == 2:
			fields := strings.Fields(trimmed)
			if len(fields) == 2 && fields[0] == expectedFields[0] && strings.HasPrefix(fields[1], "if_index:") {
				observedIndex, parseErr := strconv.Atoi(strings.TrimPrefix(fields[1], "if_index:"))
				if parseErr != nil || (interfaceInventoryPresent && (!expectedInterfaceIndexKnown || observedIndex != expectedInterfaceIndex)) {
					return snapshotABFPathDriftError("ABF policy %d path interface does not match candidate", proof.policyID)
				}
				if explicitPath {
					return snapshotABFPathDriftError("ABF policy %d path is ambiguous", proof.policyID)
				}
				fallbackPath = ""
				explicitPath = true
				foundPath = true
				unresolvedPath = true
			}
		default:
			return snapshotDecodeError("unknown ABF policy grammar %q in %q", line, output)
		}
	}
	if !foundPath && fallbackPath != "" {
		foundPath = true
	}
	if !foundPath {
		return snapshotABFPathDriftError("ABF policy %d path is missing", proof.policyID)
	}
	return nil
}

func routeCandidates(request SnapshotRequest) (map[string]trafficpolicy.RoutePolicy, error) {
	candidates := make(map[string]trafficpolicy.RoutePolicy, len(request.Candidates.RoutePolicies))
	for _, candidate := range request.Candidates.RoutePolicies {
		if _, duplicate := candidates[candidate.ID]; duplicate || strings.TrimSpace(candidate.ID) == "" {
			return nil, snapshotDecodeError("route policy candidate %q is missing or ambiguous", candidate.ID)
		}
		candidates[candidate.ID] = candidate
	}
	if err := requirePolicyCandidateNames(request.RoutePolicies, candidates, "route policy"); err != nil {
		return nil, err
	}
	return candidates, nil
}

func requirePolicyCandidateNames[T trafficpolicy.RoutePolicy | trafficpolicy.WANGroup](names []string, candidates map[string]T, kind string) error {
	for _, name := range names {
		if _, ok := candidates[strings.TrimSpace(name)]; !ok {
			return snapshotDecodeError("%s %q has no candidate", kind, name)
		}
	}
	return nil
}

func nonBlankLines(output string) []string {
	var lines []string
	for _, raw := range strings.Split(output, "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func equalTrimmedLines(output string, expected []string) bool {
	actual := nonBlankLines(output)
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}
