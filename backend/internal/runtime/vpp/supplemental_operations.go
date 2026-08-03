package vpp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

type SupplementalOwner string

const (
	SupplementalInterfaces SupplementalOwner = "interfaces"
	SupplementalRoutes     SupplementalOwner = "routes"
	SupplementalSecurity   SupplementalOwner = "security"
	SupplementalQoS        SupplementalOwner = "qos"
)

type SupplementalOperationReadback struct {
	Name        string                `json:"name"`
	Resource    string                `json:"resource"`
	PayloadHash string                `json:"payload_hash"`
	Shows       []VPPCTLCommandResult `json:"shows"`
}

func HasSupplementalOperations(plan Plan, owner SupplementalOwner) bool {
	operations, err := supplementalOperations(plan, owner)
	return err == nil && len(operations) > 0
}

func (a Adapter) ApplySupplemental(ctx context.Context, plan Plan, owner SupplementalOwner) ([]SupplementalOperationReadback, error) {
	operations, err := supplementalOperations(plan, owner)
	if err != nil || len(operations) == 0 {
		return nil, err
	}
	if a.Client == nil {
		return nil, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: open supplemental channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	readbacks := make([]SupplementalOperationReadback, 0, len(operations))
	for _, operation := range operations {
		reply, operationErr := doOperation(ctx, channel, operation)
		if operationErr != nil {
			return nil, operationErr
		}
		envelope, ok := reply.Payload.(VPPCTLReplyPayload)
		if !ok {
			return nil, fmt.Errorf("%w: supplemental operation %q returned untyped payload %T", ErrSnapshotIncomplete, operation.Name, reply.Payload)
		}
		readback := SupplementalOperationReadback{Name: operation.Name, Resource: operation.Resource}
		for _, result := range envelope.CommandResults {
			if strings.HasPrefix(result.Command, "show ") {
				if strings.TrimSpace(result.Stdout) == "" {
					return nil, fmt.Errorf("%w: supplemental operation %q show %q returned empty output", ErrSnapshotIncomplete, operation.Name, result.Command)
				}
				readback.Shows = append(readback.Shows, result)
			}
		}
		if len(readback.Shows) == 0 {
			return nil, fmt.Errorf("%w: supplemental operation %q has no typed show readback", ErrSnapshotIncomplete, operation.Name)
		}
		if err := verifySupplementalOperation(operation, envelope.CommandResults); err != nil {
			return nil, err
		}
		readback.PayloadHash, err = supplementalOperationHash(operation)
		if err != nil {
			return nil, err
		}
		readbacks = append(readbacks, readback)
	}
	return readbacks, nil
}

func supplementalOperations(plan Plan, owner SupplementalOwner) ([]Operation, error) {
	operations, err := BuildOperations(plan)
	if err != nil {
		return nil, err
	}
	selected := make([]Operation, 0)
	for _, operation := range operations {
		if target, direct := operation.Payload.(flow.Target); direct && targetOwnedByGroup(plan.Flow.VPPGroups, target) {
			continue
		}
		if supplementalOperationOwner(operation) == owner {
			selected = append(selected, operation)
		}
	}
	return selected, nil
}

func targetOwnedByGroup(groups []flow.VPPObjectGroup, target flow.Target) bool {
	for _, group := range groups {
		if group.Kind != target.Kind {
			continue
		}
		for _, object := range group.Objects {
			if object.RuleID == target.RuleID {
				return true
			}
		}
	}
	return false
}

func ValidateSupplementalReadback(plan Plan, owner SupplementalOwner, actual []SupplementalOperationReadback, referenceTime time.Time) error {
	evidencePlan := plan
	evidencePlan.NativePath.Now = referenceTime
	operations, err := supplementalOperations(evidencePlan, owner)
	if err != nil {
		return err
	}
	if len(operations) != len(actual) {
		return fmt.Errorf("%w: supplemental %s evidence count is %d, want %d", ErrSnapshotIncomplete, owner, len(actual), len(operations))
	}
	for index, operation := range operations {
		hash, hashErr := supplementalOperationHash(operation)
		if hashErr != nil {
			return hashErr
		}
		if actual[index].Name != operation.Name || actual[index].Resource != operation.Resource || actual[index].PayloadHash != hash {
			return fmt.Errorf("%w: supplemental %s evidence does not match desired operation %q", ErrSnapshotIncomplete, owner, operation.Name)
		}
		if verifyErr := verifySupplementalOperation(operation, actual[index].Shows); verifyErr != nil {
			return verifyErr
		}
	}
	return nil
}

func supplementalOperationOwner(operation Operation) SupplementalOwner {
	switch operation.Name {
	case "vpp.dataplane.attach":
		return SupplementalInterfaces
	case "vpp.abf.policy", "vpp.pbr.policy", "vpp.service-chain.egress-binding":
		return SupplementalRoutes
	case "vpp.smart-qos":
		return SupplementalQoS
	case "vpp.security-generation":
		return SupplementalSecurity
	case "vpp.acl.drop", "vpp.behavior.rate", "vpp.qos.classify", "vpp.qos.record", "vpp.qos.store", "vpp.qos.egress-map", "vpp.qos.mark", "vpp.policer":
		if _, direct := operation.Payload.(flow.Target); direct {
			return SupplementalQoS
		}
	}
	return ""
}

func SupplementalCleanupOperations(plan Plan, owner SupplementalOwner) ([]Operation, error) {
	operations, err := supplementalOperations(plan, owner)
	if err != nil {
		return nil, err
	}
	cleanup := make([]Operation, 0, len(operations))
	for _, operation := range operations {
		if attachment, ok := operation.Payload.(NativeAttachment); ok && attachment.Tier == DataplaneTierDPDK {
			// PCI/VFIO restoration belongs to the privileged dataplane transaction,
			// never to an invented VPP CLI detach command.
			continue
		}
		commands, commandErr := supplementalCleanupCommands(operation)
		if commandErr != nil {
			return nil, commandErr
		}
		cleanup = append(cleanup, Operation{Name: operation.Name + ".rollback-delete", RequestID: plan.RequestID, Resource: operation.Resource, Payload: operation.Payload, VPPCtlCommands: commands})
	}
	return cleanup, nil
}

func supplementalCleanupCommands(operation Operation) ([]string, error) {
	switch payload := operation.Payload.(type) {
	case NativeAttachment:
		switch payload.Hook {
		case NativeHookAFXDP:
			return []string{fmt.Sprintf("?delete interface af_xdp %s", payload.VPPInterface), "show interface"}, nil
		case NativeHookRDMA:
			return []string{fmt.Sprintf("?delete interface rdma %s", payload.VPPInterface), "show interface"}, nil
		default:
			return nil, &UnsupportedOperationError{Name: operation.Name, Resource: operation.Resource}
		}
	case proxy.VPPSteeringInstruction:
		return proxySteeringDeleteCommands(payload), nil
	case flow.Target:
		return flowTargetDeleteCommands(payload), nil
	case SmartQoSInterface:
		return []string{
			fmt.Sprintf("set ly-route smart-qos interface %s disable", payload.VPPInterface),
			"show ly-route smart-qos",
		}, nil
	case SecurityGeneration:
		return []string{"?show acl-plugin acl", "?show acl-plugin interface", "?show acl-plugin macip acl", "?show acl-plugin macip interface", "?show policer"}, nil
	default:
		return nil, &UnsupportedOperationError{Name: operation.Name, Resource: operation.Resource}
	}
}
