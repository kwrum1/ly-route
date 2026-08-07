package vpp

import (
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func decodeVPPCTLRoutes(request SnapshotRequest, results []VPPCTLCommandResult) (RoutePolicyReadback, error) {
	candidates, err := routeCandidates(request)
	if err != nil {
		return RoutePolicyReadback{}, err
	}
	groups := make(map[string]trafficpolicy.WANGroup, len(request.Candidates.WANGroups))
	for _, group := range request.Candidates.WANGroups {
		groups[group.ID] = group
	}
	routeOptions := buildRoutePolicyCommandOptions(request.Candidates.RoutePolicies, groups)
	policies := make([]trafficpolicy.RoutePolicy, 0, len(request.RoutePolicies))
	for _, id := range request.RoutePolicies {
		candidate := candidates[strings.TrimSpace(id)]
		policyID := stableID("route-abf:"+candidate.ID, 10000, 8999)
		tableID := stableID("route-table:"+candidate.ID, 50000, 49999)
		aclOutput, aclID, found, err := taggedACLOutput(results, "ly-route-"+safeTag(candidate.ID))
		if err != nil {
			return RoutePolicyReadback{}, err
		}
		if !found {
			aclID = stableID("route-acl:"+candidate.ID, 10000, 49999)
			aclOutput, err = commandOutput(results, fmt.Sprintf("show acl-plugin acl index %d", aclID))
			if err != nil {
				return RoutePolicyReadback{}, snapshotDecodeError("route policy %q tagged ACL is missing: %v", candidate.ID, err)
			}
		}
		aclMatch := candidate.Match
		if option, optimized := routeOptions[candidate.ID]; optimized && option.optimizedIPv4 {
			aclMatch = trafficpolicy.Match{Sources: append([]string(nil), aclMatch.Sources...), Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}, SourcePorts: []string{"any"}, DestPorts: []string{"any"}}
		}
		aclProof := aclCandidateProof{numericID: aclID, id: candidate.ID, action: routeACLAction(candidate.Action), match: aclMatch}
		if err := verifyACLOutput(aclOutput, aclProof); err != nil {
			return RoutePolicyReadback{}, err
		}
		via := routeNextHop(candidate)
		if group, ok := groups[candidate.Egress]; ok {
			via = fmt.Sprintf("table %d", wanGroupTableID(group.ID))
		}
		if option, optimized := routeOptions[candidate.ID]; optimized && option.optimizedIPv4 {
			via = fmt.Sprintf("table %d", tableID)
		}
		if err := verifyABFPolicy(results, abfCandidateProof{policyID: policyID, aclID: aclID, via: via}); err != nil {
			return RoutePolicyReadback{}, err
		}
		paths, err := parseFIBResult(results, tableID)
		if err != nil {
			return RoutePolicyReadback{}, err
		}
		if candidate.Action == "deny" {
			if len(paths) != 0 {
				return RoutePolicyReadback{}, snapshotDecodeError("denied route policy %q has a live route", candidate.ID)
			}
		} else if option, optimized := routeOptions[candidate.ID]; optimized && option.optimizedIPv4 {
			expected := routePolicyFIBVia(option.defaultVia)
			if len(paths) == 0 || !fibPathsContain(paths, expected) {
				return RoutePolicyReadback{}, snapshotDecodeError("route policy %q optimized FIB default path does not match candidate", candidate.ID)
			}
		} else if len(paths) != 1 || !strings.HasPrefix(paths[0].via, via) {
			return RoutePolicyReadback{}, snapshotDecodeError("route policy %q FIB path does not match candidate", candidate.ID)
		}
		policies = append(policies, candidate)
	}
	for _, id := range request.AbsentRoutePolicies {
		if err := verifyRoutePolicyAbsence(results, strings.TrimSpace(id)); err != nil {
			return RoutePolicyReadback{}, err
		}
	}
	return RoutePolicyReadback{Policies: policies}, nil
}

func routePolicyFIBVia(via string) string {
	via = strings.TrimSpace(via)
	const prefix = "ip4-lookup-in-table "
	if strings.HasPrefix(via, prefix) {
		return "table " + strings.TrimSpace(strings.TrimPrefix(via, prefix))
	}
	return via
}

func fibPathsContain(paths []fibPath, expected string) bool {
	for _, path := range paths {
		if strings.HasPrefix(path.via, strings.TrimSpace(expected)) {
			return true
		}
	}
	return false
}
