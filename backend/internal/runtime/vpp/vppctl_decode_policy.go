package vpp

import (
	"fmt"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

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
	if _, _, found, err := taggedACLOutput(results, "ly-route-"+safeTag(id)); err != nil {
		return err
	} else if found {
		return snapshotDecodeError("deleted route policy %q tagged ACL remains", id)
	}
	policyID := stableID("route-abf:"+id, 10000, 8999)
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
		paths, err := parseFIBResult(results, wanGroupTableID(candidate.ID))
		if err != nil {
			return WANGroupReadback{}, err
		}
		if len(paths) == 0 || candidate.Mode != trafficpolicy.WANGroupPrimaryBackup && len(paths) != len(candidate.Members) {
			return WANGroupReadback{}, snapshotDecodeError("WAN group %q active path count does not match candidate", candidate.ID)
		}
		live := make([]fibPath, 0, len(paths))
		for _, path := range paths {
			live = append(live, path)
		}
		activeMatches := 0
		for _, member := range candidate.Members {
			expectedPath := candidate.Paths[member]
			expectedVia := strings.TrimSpace(expectedPath.VPPInterface)
			if expectedVia == "" {
				expectedVia = member
			}
			for _, path := range live {
				if strings.Contains(path.via, expectedVia) && (expectedPath.NextHop == "" || strings.Contains(path.via, expectedPath.NextHop)) {
					activeMatches++
					break
				}
			}
		}
		if candidate.Mode == trafficpolicy.WANGroupPrimaryBackup {
			if activeMatches != 1 {
				return WANGroupReadback{}, snapshotDecodeError("WAN group %q active primary/backup path does not match candidate", candidate.ID)
			}
		} else if activeMatches != len(candidate.Members) {
			return WANGroupReadback{}, snapshotDecodeError("WAN group %q active members do not match candidate", candidate.ID)
		}
		if configuredPathListsErr == nil && !configuredWANPathListMatches(candidate, configuredPathLists) {
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
	expectedFibIndex := abfTableFibIndex(results, proof.via)
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "path-list:["), strings.HasPrefix(line, "path:["):
			continue
		case strings.HasPrefix(line, "[@") && strings.Contains(line, "via "+proof.via):
			if foundPath {
				return snapshotDecodeError("ABF policy %d path is ambiguous", proof.policyID)
			}
			foundPath = true
		case expectedFibIndex != "" && strings.TrimSpace(line) == "fib-index:"+expectedFibIndex:
			if foundPath {
				return snapshotDecodeError("ABF policy %d path is ambiguous", proof.policyID)
			}
			foundPath = true
		case strings.TrimSpace(line) == proof.via:
			continue
		default:
			return snapshotDecodeError("unknown ABF policy grammar %q in %q", line, output)
		}
	}
	if !foundPath {
		return snapshotDecodeError("ABF policy %d path is missing", proof.policyID)
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
