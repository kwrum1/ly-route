package vpp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	case flow.Target:
		return verifyFlowTargetReadback(payload, results)
	case SmartQoSInterface:
		output, err := commandOutput(results, "show ly-route smart-qos")
		if err != nil {
			return err
		}
		return validateSmartQoSReadback(payload, output)
	case SecurityGeneration:
		return verifySecurityGenerationReadback(payload, results)
	default:
		return &UnsupportedOperationError{Name: operation.Name, Resource: operation.Resource}
	}
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
