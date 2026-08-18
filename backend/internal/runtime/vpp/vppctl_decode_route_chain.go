package vpp

import (
	"errors"
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
	addRoutePolicyLocalDestinationPrefixes(routeOptions, request.Candidates.RoutePolicies, request.LocalDestinations)
	policies := make([]trafficpolicy.RoutePolicy, 0, len(request.RoutePolicies))
	for _, id := range request.RoutePolicies {
		candidate := candidates[strings.TrimSpace(id)]
		policyID := stableID("route-abf:"+candidate.ID, 10000, 8999)
		tableID := stableID("route-table:"+candidate.ID, 50000, 49999)
		if radixPlan, radix := compileRoutePolicyRadixPlan(candidate, groups, routeOptions[candidate.ID]); radix {
			preNATOutput, preNATErr := commandOutput(results, "show ly-route pre-nat-route")
			if preNATErr != nil {
				return RoutePolicyReadback{}, preNATErr
			}
			present, verifyErr := verifyRoutePolicyRadixReadback(preNATOutput, request, candidate, policyID, tableID, radixPlan)
			if verifyErr != nil {
				return RoutePolicyReadback{}, verifyErr
			}
			if !present {
				if request.AllowMissing {
					continue
				}
				return RoutePolicyReadback{}, snapshotDecodeError("route policy %q pre-NAT classifier is missing", candidate.ID)
			}
			paths, pathErr := parseFIBResult(results, tableID)
			if pathErr != nil {
				return RoutePolicyReadback{}, pathErr
			}
			expected := routePolicyFIBVia(radixPlan.routeTarget)
			if len(paths) != 1 || !fibPathMatchesExpected(paths[0].via, expected) {
				if request.AllowMissing {
					continue
				}
				return RoutePolicyReadback{}, snapshotDecodeError("route policy %q radix FIB path does not match candidate: observed=%v expected=%q target=%q", candidate.ID, paths, expected, radixPlan.routeTarget)
			}
			policies = append(policies, candidate)
			continue
		}
		policyOutput, policyErr := vppctlOutputAllowEmpty(results, fmt.Sprintf("show abf policy %d", policyID))
		if policyErr != nil {
			return RoutePolicyReadback{}, policyErr
		}
		observedACLID, policyPresent := observedABFACLID(policyOutput)
		if request.AllowMissing && !policyPresent {
			// A failed or interrupted apply can leave the tagged ACL behind after
			// the ABF policy has disappeared.  The complete ACL inventory plus an
			// explicit "Invalid policy ID" response proves this is repairable
			// product drift; the next reconcile will delete/recreate the route.
			continue
		}
		aclOutput, aclID, found, err := taggedACLOutput(results, "ly-route-"+safeTag(candidate.ID))
		if err != nil {
			// Interrupted route replacement can leave an old ACL alongside its
			// replacement with the same tag. The active ABF policy is the
			// authoritative owner in that case; use its ACL for a verified
			// snapshot so the lifecycle can remove every duplicate and rebuild
			// the current path. Malformed inventories and an absent ABF remain
			// fail-closed.
			if !policyPresent {
				return RoutePolicyReadback{}, err
			}
			var activeFound bool
			aclOutput, activeFound, err = indexedACLOutput(results, observedACLID)
			if err != nil || !activeFound || !strings.Contains(aclOutput, "tag {ly-route-"+safeTag(candidate.ID)+"}") {
				return RoutePolicyReadback{}, err
			}
			aclID = observedACLID
			found = true
		}
		if !found {
			aclID = observedACLID
			if !policyPresent {
				aclID = stableID("route-acl:"+candidate.ID, 10000, 49999)
			}
			aclOutput, err = commandOutput(results, fmt.Sprintf("show acl-plugin acl index %d", aclID))
			if err != nil {
				return RoutePolicyReadback{}, snapshotDecodeError("route policy %q tagged ACL is missing: %v", candidate.ID, err)
			}
		}
		aclMatch := candidate.Match
		if option, optimized := routeOptions[candidate.ID]; optimized && option.optimizedIPv4 {
			aclMatch = trafficpolicy.Match{Sources: append([]string(nil), aclMatch.Sources...), Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}, SourcePorts: []string{"any"}, DestPorts: []string{"any"}}
		}
		localDestinations := []string(nil)
		if option, optimized := routeOptions[candidate.ID]; optimized && option.optimizedIPv4 {
			// Optimized policies preserve the LAN prefix in the private FIB, not
			// in the ACL. The ACL proof therefore must not expect a synthetic
			// deny rule in this branch.
		} else {
			localDestinations = request.LocalDestinations
		}
		aclProof := aclCandidateProof{numericID: aclID, id: candidate.ID, action: routeACLAction(candidate.Action), match: aclMatch, localDestinations: localDestinations}
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
			if request.AllowMissing && errors.Is(err, errABFPathDrift) {
				// A partially applied or older route object can retain the
				// policy/ACL while pointing at a different path representation.
				// Treat that verified drift as absent so reconciliation removes
				// and rebuilds it in the current form.
				continue
			}
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
				if request.AllowMissing {
					continue
				}
				return RoutePolicyReadback{}, snapshotDecodeError("route policy %q optimized FIB default path does not match candidate", candidate.ID)
			}
		} else if len(paths) != 1 || !fibPathMatchesExpected(paths[0].via, via) {
			if request.AllowMissing {
				continue
			}
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

// VPP resolves a point-to-point next hop to a zero-address adjacency in the
// FIB.  The ABF policy still retains the configured next-hop address, while
// `show ip fib` reports `0.0.0.0 <interface>`.  Treat that representation as
// equivalent only when the resolved interface is identical; never discard an
// interface mismatch during readback.
func fibPathMatchesExpected(actual, expected string) bool {
	actual = strings.TrimSpace(actual)
	expected = strings.TrimSpace(expected)
	if strings.HasPrefix(actual, expected) {
		return true
	}
	actualFields := strings.Fields(actual)
	expectedFields := strings.Fields(expected)
	if len(actualFields) < 2 || len(expectedFields) != 2 || actualFields[0] != "0.0.0.0" {
		return false
	}
	// The compact forwarding chain appends `: mtu:...` to the interface
	// token, unlike the configured path-list output.
	actualInterface := strings.TrimRight(actualFields[1], ":,")
	return actualInterface == expectedFields[1]
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
