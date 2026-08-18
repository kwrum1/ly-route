package vpp

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

type aclCandidateProof struct {
	numericID        int
	id               string
	action           string
	match            trafficpolicy.Match
	localDestinations []string
	allowUnmatched bool
}

func decodeVPPCTLACLs(request SnapshotRequest, results []VPPCTLCommandResult) (ACLReadback, error) {
	candidates := make(map[string]trafficpolicy.SecurityACL, len(request.Candidates.ACLs))
	for _, candidate := range request.Candidates.ACLs {
		if _, duplicate := candidates[candidate.ID]; duplicate || strings.TrimSpace(candidate.ID) == "" {
			return ACLReadback{}, snapshotDecodeError("ACL candidate %q is missing or ambiguous", candidate.ID)
		}
		candidates[candidate.ID] = candidate
	}
	for _, id := range request.ACLs {
		if _, ok := candidates[strings.TrimSpace(id)]; !ok {
			return ACLReadback{}, snapshotDecodeError("ACL %q has no candidate", id)
		}
	}
	for _, id := range request.AbsentACLs {
		if hasVPPCTLCommand(results, "show acl-plugin acl") {
			if _, _, found, err := taggedACLOutput(results, "ly-route-"+safeTag(id)); err != nil {
				return ACLReadback{}, err
			} else if found {
				return ACLReadback{}, snapshotDecodeError("deleted ACL %q remains", id)
			}
		} else {
			commands := aclSnapshotCommands(SnapshotRequest{AbsentACLs: []string{id}})
			if err := verifyVPPCTLAbsence(results, commands, "ACL", id); err != nil {
				return ACLReadback{}, err
			}
		}
	}
	acls := make([]trafficpolicy.SecurityACL, 0, len(request.ACLs))
	for _, id := range request.ACLs {
		candidate := candidates[strings.TrimSpace(id)]
		aclID := stableID("security-acl:"+candidate.ID, 50000, 49999)
		aclOutput := ""
		if hasVPPCTLCommand(results, "show acl-plugin acl") {
			var found bool
			var err error
			aclOutput, aclID, found, err = taggedACLOutput(results, "ly-route-"+safeTag(candidate.ID))
			if err != nil {
				return ACLReadback{}, err
			}
			if !found {
				return ACLReadback{}, snapshotDecodeError("ACL %q tagged runtime object is missing", candidate.ID)
			}
		}
		proof := aclCandidateProof{numericID: aclID, id: candidate.ID, action: candidate.Action, match: candidate.Match}
		if aclOutput != "" {
			if err := verifyACLOutput(aclOutput, proof); err != nil {
				return ACLReadback{}, err
			}
		} else if err := verifyACLResult(results, proof); err != nil {
			return ACLReadback{}, err
		}
		acls = append(acls, candidate)
	}
	return ACLReadback{ACLs: acls}, nil
}

func hasVPPCTLCommand(results []VPPCTLCommandResult, command string) bool {
	for _, result := range results {
		if result.Command == command {
			return true
		}
	}
	return false
}

func verifyACLResult(results []VPPCTLCommandResult, proof aclCandidateProof) error {
	output, err := commandOutput(results, fmt.Sprintf("show acl-plugin acl index %d", proof.numericID))
	if err != nil {
		return err
	}
	return verifyACLOutput(output, proof)
}

func verifyACLOutput(output string, proof aclCandidateProof) error {
	lines := nonBlankLines(output)
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "applied inbound on sw_if_index:") || strings.HasPrefix(trimmed, "applied outbound on sw_if_index:") || strings.HasPrefix(trimmed, "used in lookup context index:") {
			continue
		}
		filtered = append(filtered, line)
	}
	lines = filtered
	if len(lines) < 2 {
		return snapshotDecodeError("ACL %q output is truncated", proof.id)
	}
	header := strings.Fields(lines[0])
	if len(header) != 6 || header[0] != "acl-index" || header[2] != "count" || header[4] != "tag" {
		return snapshotDecodeError("unknown ACL header grammar %q", lines[0])
	}
	parsedID, idErr := strconv.Atoi(header[1])
	count, countErr := strconv.Atoi(header[3])
	tag := strings.TrimSuffix(strings.TrimPrefix(header[5], "{"), "}")
	if idErr != nil || countErr != nil || parsedID != proof.numericID || tag != "ly-route-"+safeTag(proof.id) || count != len(lines)-1 {
		return snapshotDecodeError("ACL %q header does not match candidate", proof.id)
	}
	actual := make([]string, 0, len(lines)-1)
	seen := make(map[string]struct{}, len(lines)-1)
	ipv6Fallback := "permit src ::/0 dst ::/0 proto 0 sport 0-65535 dport 0-65535"
	foundIPv6Fallback := false
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 13 || !strings.HasSuffix(fields[0], ":") || (fields[1] != "ipv4" && fields[1] != "ipv6") || fields[3] != "src" || fields[5] != "dst" || fields[7] != "proto" || fields[9] != "sport" || fields[11] != "dport" {
			return snapshotDecodeError("unknown ACL rule grammar %q", line)
		}
		fields[10] = aclReadbackPort(fields[10])
		fields[12] = aclReadbackPort(fields[12])
		canonical := strings.Join(fields[2:], " ")
		if fields[1] == "ipv6" {
			if !proof.allowUnmatched || canonical != ipv6Fallback || foundIPv6Fallback {
				return snapshotDecodeError("ACL %q contains an unexpected IPv6 rule", proof.id)
			}
			foundIPv6Fallback = true
			continue
		}
		if _, duplicate := seen[canonical]; duplicate {
			return snapshotDecodeError("ACL %q contains duplicate rules", proof.id)
		}
		seen[canonical] = struct{}{}
		actual = append(actual, canonical)
	}
	if proof.allowUnmatched && !foundIPv6Fallback {
		return snapshotDecodeError("ACL %q is missing its IPv6 unmatched-traffic permit", proof.id)
	}
	expected, err := expectedACLRulesWithLocalBypass(proof.action, proof.match, proof.allowUnmatched, proof.localDestinations)
	if err != nil {
		return err
	}
	sort.Strings(actual)
	if strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		// A live gateway can legitimately carry the pre-local-bypass ACL while
		// the reconciler is preparing the first upgrade to this compiler. Accept
		// that exact legacy form so reconciliation can replace it; any unrelated
		// rule drift still fails closed.
		if len(proof.localDestinations) > 0 {
			legacy, legacyErr := expectedACLRulesWithFallback(proof.action, proof.match, proof.allowUnmatched)
			if legacyErr == nil && strings.Join(actual, "\n") == strings.Join(legacy, "\n") {
				return nil
			}
		}
		return snapshotDecodeError("ACL %q rules do not match candidate: actual=%q expected=%q", proof.id, actual, expected)
	}
	return nil
}

func taggedACLOutput(results []VPPCTLCommandResult, tag string) (string, int, bool, error) {
	output, err := vppctlOutputAllowEmpty(results, "show acl-plugin acl")
	if err != nil {
		return "", 0, false, err
	}
	lines := nonBlankLines(output)
	var matched string
	matchedID := 0
	for index := 0; index < len(lines); {
		fields := strings.Fields(lines[index])
		if len(fields) != 6 || fields[0] != "acl-index" || fields[2] != "count" || fields[4] != "tag" {
			index++
			continue
		}
		id, idErr := strconv.Atoi(fields[1])
		count, countErr := strconv.Atoi(fields[3])
		if idErr != nil || countErr != nil || count < 0 || index+count >= len(lines) {
			return "", 0, false, snapshotDecodeError("malformed ACL inventory header %q", lines[index])
		}
		observedTag := strings.Trim(strings.TrimSpace(fields[5]), "{}")
		if observedTag == tag {
			if matched != "" {
				return "", 0, false, snapshotDecodeError("ACL tag %q is ambiguous", tag)
			}
			matched = strings.Join(lines[index:index+count+1], "\n")
			matchedID = id
		}
		index += count + 1
		for index < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[index]), "used in lookup context index:") {
			index++
		}
	}
	return matched, matchedID, matched != "", nil
}

// indexedACLOutput extracts a single ACL block from the inventory. It is used
// only when an ABF policy has already proved ownership of the numeric ACL id;
// a duplicate tag alone is never accepted as authoritative.
func indexedACLOutput(results []VPPCTLCommandResult, wantedID int) (string, bool, error) {
	output, err := vppctlOutputAllowEmpty(results, "show acl-plugin acl")
	if err != nil {
		return "", false, err
	}
	lines := nonBlankLines(output)
	var matched string
	for index := 0; index < len(lines); {
		fields := strings.Fields(lines[index])
		if len(fields) != 6 || fields[0] != "acl-index" || fields[2] != "count" || fields[4] != "tag" {
			index++
			continue
		}
		id, idErr := strconv.Atoi(fields[1])
		count, countErr := strconv.Atoi(fields[3])
		if idErr != nil || countErr != nil || count < 0 || index+count >= len(lines) {
			return "", false, snapshotDecodeError("malformed ACL inventory header %q", lines[index])
		}
		if id == wantedID {
			if matched != "" {
				return "", false, snapshotDecodeError("ACL index %d is ambiguous", wantedID)
			}
			matched = strings.Join(lines[index:index+count+1], "\n")
		}
		index += count + 1
		for index < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[index]), "used in lookup context index:") {
			index++
		}
	}
	return matched, matched != "", nil
}

func vppctlOutputAllowEmpty(results []VPPCTLCommandResult, command string) (string, error) {
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
	return output, nil
}

func expectedACLRules(action string, match trafficpolicy.Match) ([]string, error) {
	return expectedACLRulesWithFallback(action, match, false)
}

func expectedACLRulesWithLocalBypass(action string, match trafficpolicy.Match, allowUnmatched bool, localDestinations []string) ([]string, error) {
	rules, err := expectedACLRulesWithFallback(action, match, allowUnmatched)
	if err != nil || action != "permit" || len(localDestinations) == 0 || !routePolicyHasIPv4CatchAll(match.Destinations) {
		return rules, err
	}
	for _, source := range nonEmptyList(match.Sources, "0.0.0.0/0") {
		for _, destination := range localDestinations {
			for _, protocol := range nonEmptyList(match.Protocols, "any") {
				protocol, protocolErr := vppProtocol(protocol)
				if protocolErr != nil {
					return nil, protocolErr
				}
				for _, sourcePort := range nonEmptyList(match.SourcePorts, "any") {
					for _, destinationPort := range nonEmptyList(match.DestPorts, "any") {
						rules = append(rules, fmt.Sprintf("deny src %s dst %s proto %s sport %s dport %s", aclAddressValue(source), aclAddressValue(destination), protocol, aclReadbackPort(sourcePort), aclReadbackPort(destinationPort)))
					}
				}
			}
		}
	}
	sort.Strings(rules)
	return rules, nil
}

func expectedACLRulesWithFallback(action string, match trafficpolicy.Match, allowUnmatched bool) ([]string, error) {
	var rules []string
	for _, source := range nonEmptyList(match.Sources, "0.0.0.0/0") {
		for _, destination := range nonEmptyList(match.Destinations, "0.0.0.0/0") {
			source = aclAddressValue(source)
			destination = aclAddressValue(destination)
			for _, protocol := range nonEmptyList(match.Protocols, "any") {
				protocol, err := vppProtocol(protocol)
				if err != nil {
					return nil, err
				}
				for _, sourcePort := range nonEmptyList(match.SourcePorts, "any") {
					for _, destinationPort := range nonEmptyList(match.DestPorts, "any") {
						rules = append(rules, fmt.Sprintf("%s src %s dst %s proto %s sport %s dport %s", action, source, destination, protocol, aclReadbackPort(sourcePort), aclReadbackPort(destinationPort)))
					}
				}
			}
		}
	}
	if action == "permit" && allowUnmatched {
		fallback := "permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535"
		seen := false
		for _, rule := range rules {
			if rule == fallback {
				seen = true
				break
			}
		}
		if !seen {
			rules = append(rules, fallback)
		}
	}
	sort.Strings(rules)
	return rules, nil
}

func aclReadbackPort(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "any" {
		return "0-65535"
	}
	if first, second, found := strings.Cut(value, "-"); found && first == second {
		return first
	}
	return value
}

func vppProtocol(protocol string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "any":
		return "0", nil
	case "icmp":
		return "1", nil
	case "tcp":
		return "6", nil
	case "udp":
		return "17", nil
	default:
		return "", snapshotDecodeError("unsupported ACL protocol %q", protocol)
	}
}
