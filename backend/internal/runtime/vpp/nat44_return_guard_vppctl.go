package vpp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

const natReturnGuardPriority = 2

var errNATReturnGuardDrift = errors.New("NAT return guard is missing or mismatched")

type natReturnGuard struct {
	resource            string
	externalAddress     string
	internalAddress     string
	protocol            string
	internalPort        int
	wanInterface        string
	wanNextHop          string
	ingressVPPInterface string
}

func (guard natReturnGuard) ingress() string {
	if strings.TrimSpace(guard.ingressVPPInterface) != "" {
		return strings.TrimSpace(guard.ingressVPPInterface)
	}
	return configuredLANVPPInterface()
}

func natReturnGuardForPortMapping(mapping nat.PortMapping) natReturnGuard {
	return natReturnGuard{
		resource:        mapping.ID,
		externalAddress: mapping.ExternalAddress,
		internalAddress: mapping.InternalHost,
		protocol:        mapping.Protocol,
		internalPort:    mapping.InternalPort,
		wanInterface:    mapping.WANInterface,
		wanNextHop:      mapping.WANNextHop,
	}
}

func natReturnGuardForStaticMapping(mapping nat.StaticMapping) natReturnGuard {
	return natReturnGuard{
		resource:        mapping.ID,
		externalAddress: mapping.ExternalAddress,
		internalAddress: mapping.InternalAddress,
		protocol:        "any",
		wanInterface:    mapping.WANInterface,
		wanNextHop:      mapping.WANNextHop,
	}
}

func (guard natReturnGuard) identity() string {
	return "nat-return-" + strings.TrimSpace(guard.resource)
}

func (guard natReturnGuard) policyID() int {
	return stableID("nat-return-abf:"+guard.resource, 20000, 8999)
}

func (guard natReturnGuard) tag() string {
	return "ly-route-" + safeTag(guard.identity())
}

func (guard natReturnGuard) sourcePrefix() (string, error) {
	address, err := netip.ParseAddr(strings.TrimSpace(guard.externalAddress))
	if err != nil || !address.Is4() {
		return "", snapshotDecodeError("NAT return guard %q has invalid external IPv4 address %q", guard.resource, guard.externalAddress)
	}
	return netip.PrefixFrom(address, 32).String(), nil
}

func (guard natReturnGuard) via() string {
	via := routePathVia(guard.wanInterface, guard.wanNextHop, "")
	if strings.TrimSpace(via) == "" {
		return "ip4-lookup-in-table 0"
	}
	return via
}

func isNATReturnGuardDrift(err error) bool {
	return errors.Is(err, errNATReturnGuardDrift)
}

func natReturnGuardDrift(err error) error {
	return fmt.Errorf("%w: %v", errNATReturnGuardDrift, err)
}

func nat44SnapshotCommands(request SnapshotRequest) []string {
	commands := []string{"show nat44 static mappings"}
	if request.NATBehavior == nat.BehaviorFullCone {
		// NAT44-EI has its own inventory namespace. Return-path ACL guards are
		// still checked below, but the mapping readback must use the EI table.
		commands = []string{"show nat44 ei static mappings", "show nat44 ei interfaces", "show nat44 ei sessions"}
	}
	if !request.VerifyNATReturnGuards {
		return commands
	}
	commands = append(commands, "show acl-plugin acl")
	commands = append(commands, "show ly-route pre-nat-route")
	for _, mapping := range request.Candidates.NATStaticMappings {
		commands = appendUnique(commands, fmt.Sprintf("show abf policy %d", natReturnGuardForStaticMapping(mapping).policyID()))
	}
	for _, mapping := range request.Candidates.NATPortMappings {
		commands = appendUnique(commands, fmt.Sprintf("show abf policy %d", natReturnGuardForPortMapping(mapping).policyID()))
	}
	ingress := strings.TrimSpace(request.NATIngressVPPInterface)
	if ingress == "" {
		ingress = configuredLANVPPInterface()
	}
	if ingress != "" {
		commands = appendUnique(commands, "show abf attach "+ingress)
	}
	return commands
}

func verifyNATReturnGuardReadback(results []VPPCTLCommandResult, guard natReturnGuard) error {
	ingress := guard.ingress()
	if ingress == "" {
		return snapshotDecodeError("NAT return guard %q has no resolved LAN VPP interface", guard.resource)
	}
	aclOutput, aclID, found, err := taggedACLOutput(results, guard.tag())
	if err != nil {
		return err
	}
	if !found {
		return natReturnGuardDrift(snapshotDecodeError("NAT return guard %q ACL is missing", guard.resource))
	}
	prefix, err := guard.sourcePrefix()
	if err != nil {
		return err
	}
	match := trafficpolicy.Match{Sources: []string{prefix}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}, SourcePorts: []string{"any"}, DestPorts: []string{"any"}}
	if err := verifyACLOutput(aclOutput, aclCandidateProof{numericID: aclID, id: guard.identity(), action: "permit", match: match}); err != nil {
		return natReturnGuardDrift(err)
	}
	policyCommand := fmt.Sprintf("show abf policy %d", guard.policyID())
	policyOutput, err := commandOutputAllowEmpty(results, policyCommand)
	if err != nil {
		return err
	}
	observedACL, present := observedABFACLID(policyOutput)
	if !present {
		return natReturnGuardDrift(snapshotDecodeError("NAT return guard %q ABF policy is missing", guard.resource))
	}
	if observedACL != aclID {
		return natReturnGuardDrift(snapshotDecodeError("NAT return guard %q ABF policy references ACL %d, want %d", guard.resource, observedACL, aclID))
	}
	attachOutput, err := commandOutputAllowEmpty(results, "show abf attach "+ingress)
	if err != nil {
		return err
	}
	if !routePolicyAttached(attachOutput, guard.policyID()) {
		return natReturnGuardDrift(snapshotDecodeError("NAT return guard %q attachment is missing", guard.resource))
	}
	preNATOutput, err := commandOutputAllowEmpty(results, "show ly-route pre-nat-route")
	if err != nil {
		return err
	}
	if err := verifyNATPreRouteBypass(preNATOutput, guard); err != nil {
		return err
	}
	return nil
}

func (channel vppctlChannel) doNAT44MappingLifecycle(ctx context.Context, operation Operation, guard natReturnGuard) (Reply, error) {
	mappingReply, err := channel.doCommands(ctx, operation)
	if err != nil {
		return mappingReply, err
	}
	results := vppctlCommandResults(mappingReply)
	if nat44MappingOperationDeletes(operation) {
		removed, removeErr := channel.removeNATReturnGuard(ctx, operation, guard)
		results = append(results, removed...)
		return routePolicyLifecycleReply(operation, results), removeErr
	}
	guardResults, guardErr := channel.replaceNATReturnGuard(ctx, operation, guard)
	results = append(results, guardResults...)
	return routePolicyLifecycleReply(operation, results), guardErr
}

func nat44MappingOperationDeletes(operation Operation) bool {
	if strings.HasSuffix(operation.Name, ".rollback-delete") {
		return true
	}
	for _, raw := range operation.VPPCtlCommands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		// Both NAT44-ED and NAT44-EI use a static-mapping command.  The
		// return-path guard must be installed for either mode; otherwise a
		// full-cone port mapping would be mistaken for a delete operation and
		// its inbound guard would be removed immediately after the mapping was
		// created.
		if (strings.HasPrefix(command, "nat44 add static mapping ") || strings.HasPrefix(command, "nat44 ei add static mapping ")) && !strings.HasSuffix(command, " del") {
			return false
		}
	}
	return true
}

func vppctlCommandResults(reply Reply) []VPPCTLCommandResult {
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok {
		return nil
	}
	return append([]VPPCTLCommandResult(nil), payload.CommandResults...)
}

func (channel vppctlChannel) replaceNATReturnGuard(ctx context.Context, operation Operation, guard natReturnGuard) ([]VPPCTLCommandResult, error) {
	removed, err := channel.removeNATReturnGuard(ctx, operation, guard)
	if err != nil {
		return removed, err
	}
	prefix, err := guard.sourcePrefix()
	if err != nil {
		return removed, err
	}
	ingress := guard.ingress()
	if ingress == "" {
		return removed, snapshotDecodeError("NAT return guard %q has no resolved LAN VPP interface", guard.resource)
	}
	create := fmt.Sprintf("set acl-plugin acl permit src %s dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535 tag %s", prefix, guard.tag())
	created, err := channel.runServiceChainCommands(ctx, operation, create)
	if err != nil {
		return append(removed, created...), err
	}
	aclID, err := allocatedServiceChainACLID(created)
	if err != nil {
		return append(removed, created...), err
	}
	bypass, err := guard.preNATBypassCommand()
	if err != nil {
		return append(removed, created...), err
	}
	applied, err := channel.runServiceChainCommands(ctx, operation,
		guard.preNATBypassDeleteCommand(),
		bypass,
		fmt.Sprintf("abf policy add id %d acl %d via %s", guard.policyID(), aclID, guard.via()),
		fmt.Sprintf("abf attach ip4 policy %d priority %d %s", guard.policyID(), natReturnGuardPriority, ingress),
		fmt.Sprintf("show acl-plugin acl index %d", aclID),
		fmt.Sprintf("show abf policy %d", guard.policyID()),
		"show abf attach "+ingress,
		"show ly-route pre-nat-route")
	results := append(append(removed, created...), applied...)
	if err != nil {
		return results, err
	}
	policyOutput := resultStdoutLast(results, fmt.Sprintf("show abf policy %d", guard.policyID()))
	observedACL, present := observedABFACLID(policyOutput)
	if !present || observedACL != aclID {
		return results, snapshotDecodeError("NAT return guard %q ABF policy does not reference allocated ACL %d", guard.resource, aclID)
	}
	match := trafficpolicy.Match{Sources: []string{prefix}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"any"}, SourcePorts: []string{"any"}, DestPorts: []string{"any"}}
	if err := verifyACLOutput(resultStdoutLast(results, fmt.Sprintf("show acl-plugin acl index %d", aclID)), aclCandidateProof{numericID: aclID, id: guard.identity(), action: "permit", match: match}); err != nil {
		return results, err
	}
	if !routePolicyAttached(resultStdoutLast(results, "show abf attach "+ingress), guard.policyID()) {
		return results, snapshotDecodeError("NAT return guard %q is not attached to LAN", guard.resource)
	}
	if err := verifyNATPreRouteBypass(resultStdoutLast(results, "show ly-route pre-nat-route"), guard); err != nil {
		return results, err
	}
	return results, nil
}

func (channel vppctlChannel) removeNATReturnGuard(ctx context.Context, operation Operation, guard natReturnGuard) ([]VPPCTLCommandResult, error) {
	ingress := configuredLANVPPInterface()
	if ingress == "" {
		return nil, snapshotDecodeError("NAT return guard %q has no resolved LAN VPP interface", guard.resource)
	}
	results, err := channel.runServiceChainCommands(ctx, operation,
		guard.preNATBypassDeleteCommand(),
		fmt.Sprintf("show abf policy %d", guard.policyID()),
		"show ip fib summary",
		"show acl-plugin acl")
	if err != nil {
		return results, err
	}
	policyCommand := fmt.Sprintf("show abf policy %d", guard.policyID())
	policyOutput := resultStdoutLast(results, policyCommand)
	if aclID, present := observedABFACLID(policyOutput); present {
		via := routePolicyABFDeleteVia(routePolicyVPPCTLSpec{via: guard.via()}, policyOutput, resultStdoutLast(results, "show ip fib summary"))
		if via == "" {
			return results, snapshotDecodeError("NAT return guard %q live ABF path cannot be removed safely", guard.resource)
		}
		detached, detachErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("abf attach ip4 del policy %d %s", guard.policyID(), ingress),
			"?show abf attach "+ingress)
		results = append(results, detached...)
		if detachErr != nil {
			return results, detachErr
		}
		if routePolicyAttached(resultStdoutLast(results, "show abf attach "+ingress), guard.policyID()) {
			return results, snapshotDecodeError("NAT return guard %q remains attached", guard.resource)
		}
		deleted, deleteErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("abf policy del id %d acl %d via %s", guard.policyID(), aclID, via),
			policyCommand,
			fmt.Sprintf("delete acl-plugin acl index %d", aclID))
		results = append(results, deleted...)
		if deleteErr != nil {
			return results, deleteErr
		}
		if _, present := observedABFACLID(resultStdoutLast(results, policyCommand)); present {
			return results, snapshotDecodeError("NAT return guard %q policy remains after deletion", guard.resource)
		}
	}
	for _, aclID := range taggedServiceChainACLIDs(resultStdoutLast(results, "show acl-plugin acl"), guard.tag()) {
		deleted, deleteErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("delete acl-plugin acl index %d", aclID))
		results = append(results, deleted...)
		if deleteErr != nil {
			return results, deleteErr
		}
	}
	verified, err := channel.runServiceChainCommands(ctx, operation, policyCommand, "show acl-plugin acl", "?show abf attach "+ingress)
	results = append(results, verified...)
	if err != nil {
		return results, err
	}
	if _, present := observedABFACLID(resultStdoutLast(results, policyCommand)); present {
		return results, snapshotDecodeError("NAT return guard %q policy remains after cleanup", guard.resource)
	}
	if len(taggedServiceChainACLIDs(resultStdoutLast(results, "show acl-plugin acl"), guard.tag())) != 0 {
		return results, snapshotDecodeError("NAT return guard %q ACL remains after cleanup", guard.resource)
	}
	if routePolicyAttached(resultStdoutLast(results, "show abf attach "+ingress), guard.policyID()) {
		return results, snapshotDecodeError("NAT return guard %q remains attached after cleanup", guard.resource)
	}
	return results, nil
}
