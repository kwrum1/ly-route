package vpp

import (
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func verifyDynamicFlowACL(results []VPPCTLCommandResult, object flow.VPPObject, action string) error {
	output, aclID, found, err := taggedACLOutput(results, "ly-route-"+safeTag(object.RuleID))
	if err != nil {
		return err
	}
	if !found {
		return snapshotDecodeError("flow QoS ACL %q tagged runtime object is missing", object.RuleID)
	}
	return verifyACLOutput(output, aclCandidateProof{numericID: aclID, id: object.RuleID, action: action, match: policyMatch(object.Match)})
}

func policyMatch(match flow.Match) trafficpolicy.Match {
	return trafficpolicy.Match{Sources: match.Sources, Destinations: match.Destinations, Protocols: match.Protocols, SourcePorts: match.SourcePorts, DestPorts: match.DestPorts, Direction: match.Direction}
}
