package vpp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

func supplementalOperationHash(operation Operation) (string, error) {
	payload, err := json.Marshal(struct {
		Name     string `json:"name"`
		Resource string `json:"resource"`
		Payload  any    `json:"payload"`
	}{Name: operation.Name, Resource: operation.Resource, Payload: operation.Payload})
	if err != nil {
		return "", fmt.Errorf("hash supplemental operation %q: %w", operation.Name, err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func verifySupplementalOperation(operation Operation, results []VPPCTLCommandResult) error {
	switch payload := operation.Payload.(type) {
	case NativeAttachment:
		if err := requireSupplementalIdentity(results, "show interface "+payload.VPPInterface, payload.VPPInterface); err != nil {
			return err
		}
		hardware, err := commandOutput(results, "show hardware-interfaces "+payload.VPPInterface)
		if err != nil {
			return err
		}
		if !strings.Contains(hardware, payload.VPPInterface) {
			return snapshotDecodeError("native attachment hardware readback does not identify %q", payload.VPPInterface)
		}
		if payload.Hook == NativeHookAFXDP && !strings.Contains(hardware, "netdev "+payload.LinuxInterface) {
			return snapshotDecodeError("AF_XDP attachment %q does not identify Linux netdev %q", payload.VPPInterface, payload.LinuxInterface)
		}
		if payload.Tier == DataplaneTierDPDK && payload.PCIAddress != "" && !strings.Contains(strings.ToLower(hardware), strings.ToLower(payload.PCIAddress)) {
			return snapshotDecodeError("DPDK attachment %q does not identify PCI device %q", payload.VPPInterface, payload.PCIAddress)
		}
		return nil
	case proxy.VPPSteeringInstruction:
		return verifyProxySteeringReadback(payload, results)
	case DNSServiceNetwork:
		return verifyDNSServiceNetworkReadback(payload, results)
	case flow.Target:
		return verifyFlowTargetReadback(payload, results)
	case SmartQoSInterface:
		output, err := commandOutput(results, "show ly-route smart-qos")
		if err != nil {
			return err
		}
		return validateSmartQoSReadback(payload, output)
	case ManagementLCP:
		output, err := commandOutputLast(results, "show lcp")
		if err != nil {
			return err
		}
		present := managementLCPPresent(output, payload.VPPInterface, payload.HostInterface)
		if payload.Enabled && !present {
			return snapshotDecodeError("Linux control-plane LCP pair %s/%s is absent from supplemental readback", payload.VPPInterface, payload.HostInterface)
		}
		if !payload.Enabled && len(managementLCPPairs(output, payload.HostInterface)) != 0 {
			return snapshotDecodeError("exclusive management left LCP host interface %s attached", payload.HostInterface)
		}
		if payload.Enabled && payload.IPv4BroadcastLocal {
			broadcast, broadcastErr := commandOutputLast(results, "show ip fib 255.255.255.255")
			if broadcastErr != nil {
				return broadcastErr
			}
			if !ipv4BroadcastLocalRoutePresent(broadcast) {
				return snapshotDecodeError("IPv4 limited broadcast route is absent from supplemental readback")
			}
		}
		return nil
	case DNSTransparentInterception:
		return verifyDNSTransparentReadback(payload, results)
	case SecurityGeneration:
		return verifySecurityGenerationReadback(payload, results)
	default:
		return &UnsupportedOperationError{Name: operation.Name, Resource: operation.Resource}
	}
}

func verifyDNSTransparentReadback(interception DNSTransparentInterception, results []VPPCTLCommandResult) error {
	interceptOutput, err := commandOutputLast(results, "show ly-route dns-intercept")
	if err != nil {
		return err
	}
	if !strings.Contains(interceptOutput, "enabled 1") || !strings.Contains(interceptOutput, interception.LANInterface) {
		return snapshotDecodeError("transparent DNS pre-NAT interception is absent for %s", interception.LANInterface)
	}
	v4FIB, err := commandOutputLast(results, fmt.Sprintf("show ip fib table %d", dnsIPv4TableID))
	if err != nil {
		return err
	}
	for _, prefix := range append([]string{"0.0.0.0/0"}, interception.IPv4Prefixes...) {
		prefix = canonicalFIBPrefix(prefix)
		if !strings.Contains(v4FIB, prefix) {
			return snapshotDecodeError("transparent DNS IPv4 route %s is absent", prefix)
		}
	}
	return nil
}

func canonicalFIBPrefix(value string) string {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil {
		return strings.TrimSpace(value)
	}
	return prefix.Masked().String()
}

func verifyDNSServiceNetworkReadback(network DNSServiceNetwork, results []VPPCTLCommandResult) error {
	if err := requireSupplementalIdentity(results, "show interface address "+network.VPPInterface, network.VPPInterface); err != nil {
		return err
	}
	if err := requireSupplementalIdentity(results, "show ip fib table "+strconv.Itoa(network.TableID), strconv.Itoa(network.TableID)); err != nil {
		return err
	}
	natInterfaces, natErr := commandOutput(results, "show nat44 interfaces")
	natEIInterfaces, natEIErr := commandOutput(results, "show nat44 ei interfaces")
	if natErr != nil && natEIErr != nil {
		return natErr
	}
	if !strings.Contains(natInterfaces, network.VPPInterface+" in") && !strings.Contains(natEIInterfaces, network.VPPInterface+" in") {
		return snapshotDecodeError("DNS service interface %q is missing its NAT44 input role", network.VPPInterface)
	}
	return requireSupplementalIdentity(results, "show tap", network.VPPInterface)
}

func commandOutputLast(results []VPPCTLCommandResult, command string) (string, error) {
	for index := len(results) - 1; index >= 0; index-- {
		if results[index].Command == command {
			if strings.TrimSpace(results[index].Stdout) == "" {
				return "", snapshotDecodeError("supplemental command %q returned empty output", command)
			}
			return results[index].Stdout, nil
		}
	}
	return "", snapshotDecodeError("supplemental command %q is missing", command)
}

func verifyProxySteeringReadback(steering proxy.VPPSteeringInstruction, results []VPPCTLCommandResult) error {
	resource := steering.EgressID
	if strings.TrimSpace(resource) == "" {
		resource = string(steering.Handoff)
	}
	switch steering.TargetKind {
	case "vpp.abf.policy":
		aclID := strconv.Itoa(stableID("acl:"+resource, 1000, 8999))
		policyID := strconv.Itoa(stableID("abf:"+resource, 1000, 8999))
		if err := requireSupplementalIdentity(results, "show acl-plugin acl index "+aclID, aclID); err != nil {
			return err
		}
		if err := requireSupplementalIdentity(results, "show abf policy "+policyID, policyID); err != nil {
			return err
		}
		return requireSupplementalCommandIdentity(results, "show abf attach ")
	case "vpp.pbr.policy", "vpp.service-chain.egress-binding":
		tableID := strconv.Itoa(stableID("pbr:"+resource, 10000, 49999))
		return requireAnySupplementalIdentity(results, tableID)
	case "vpp.proxy-service.network":
		network := steering.ServiceNetwork
		if strings.TrimSpace(network.EgressID) == "" {
			network = proxy.ServiceNetworkForEgressID(resource)
		}
		if err := requireSupplementalIdentity(results, "show interface address "+network.IngressVPPInterface, network.IngressVPPInterface); err != nil {
			return err
		}
		if err := requireSupplementalIdentity(results, "show interface address "+network.EgressVPPInterface, network.EgressVPPInterface); err != nil {
			return err
		}
		if err := requireSupplementalIdentity(results, "show ip fib table "+strconv.Itoa(network.OutboundTableID), strconv.Itoa(network.OutboundTableID)); err != nil {
			return err
		}
		// The proxy egress is a NAT inside interface in full-cone (NAT44 EI)
		// mode. Endpoint-dependent mode deliberately leaves it detached during
		// the VPP phase because the Linux service artifact attaches it after the
		// gateway transaction. Infer the expected behavior from the typed show
		// command emitted by this operation instead of hard-coding one NAT mode.
		if natEIInterfaces, err := commandOutput(results, "show nat44 ei interfaces"); err == nil {
			if !strings.Contains(natEIInterfaces, network.EgressVPPInterface+" in") {
				return snapshotDecodeError("proxy service egress %q is missing its NAT44 EI input role", network.EgressVPPInterface)
			}
		} else {
			natInterfaces, natErr := commandOutput(results, "show nat44 interfaces")
			if natErr != nil {
				return err
			}
			if strings.Contains(natInterfaces, network.EgressVPPInterface) {
				return snapshotDecodeError("proxy service egress %q must not be a NAT44 input interface during gateway apply", network.EgressVPPInterface)
			}
		}
		return requireSupplementalIdentity(results, "show tap", network.IngressVPPInterface)
	default:
		return &UnsupportedOperationError{Name: steering.TargetKind, Resource: resource}
	}
}

func verifyFlowTargetReadback(target flow.Target, results []VPPCTLCommandResult) error {
	normalized := append([]VPPCTLCommandResult(nil), results...)
	for index := range normalized {
		command := normalized[index].Command
		for _, prefix := range []string{"show qos record ", "show qos store ", "show qos mark "} {
			if strings.HasPrefix(command, prefix) {
				normalized[index].Command = prefix + "lyroute-$LY_ROUTE_LAN_INTERFACE"
			}
		}
	}
	object := flow.VPPObject{RuleID: target.RuleID, Granularity: target.Granularity, Action: target.Action, Class: target.Class, DSCP: target.DSCP, RemarkBehavior: target.RemarkBehavior, Policer: target.Policer, Match: target.Match, Attachments: target.Attachments}
	return verifyQoSObject(normalized, target.Kind, object)
}

func requireSupplementalIdentity(results []VPPCTLCommandResult, command, identity string) error {
	output, err := commandOutput(results, command)
	if err != nil {
		return err
	}
	if !strings.Contains(output, identity) {
		return snapshotDecodeError("supplemental command %q does not identify %q", command, identity)
	}
	return nil
}

func requireAnySupplementalIdentity(results []VPPCTLCommandResult, identity string) error {
	for _, result := range results {
		if strings.HasPrefix(result.Command, "show ") && strings.Contains(result.Stdout, identity) {
			return nil
		}
	}
	return snapshotDecodeError("supplemental readback does not identify %q", identity)
}

func requireSupplementalCommandIdentity(results []VPPCTLCommandResult, prefix string) error {
	for _, result := range results {
		if !strings.HasPrefix(result.Command, prefix) {
			continue
		}
		identity := strings.TrimSpace(strings.TrimPrefix(result.Command, prefix))
		if identity != "" && strings.Contains(result.Stdout, identity) {
			return nil
		}
		return snapshotDecodeError("supplemental command %q does not identify %q", result.Command, identity)
	}
	return snapshotDecodeError("supplemental command prefix %q is missing", prefix)
}
