package vpp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"ly-route/backend/internal/runtime/nat"
)

type NAT44Readback struct {
	StaticMappings []nat.StaticMapping `json:"static_mappings"`
	PortMappings   []nat.PortMapping   `json:"port_mappings"`
	Behavior       nat.Behavior        `json:"behavior,omitempty"`
}

type NAT44Plan struct {
	TransactionID          string
	Behavior               nat.Behavior
	IngressVPPInterface    string
	StaticMappings         []nat.StaticMapping
	PortMappings           []nat.PortMapping
	ReadbackStaticMappings []nat.StaticMapping
	ReadbackPortMappings   []nat.PortMapping
	DeleteStaticMappings   []string
	DeletePortMappings     []string
}

type NAT44LifecycleError struct {
	Operation      string
	Cause          error
	Rollback       error
	RollbackResult RollbackResult
}

func (err *NAT44LifecycleError) Error() string {
	if err.Rollback != nil {
		return fmt.Sprintf("NAT44 operation %s failed: %v; rollback %s: %v", err.Operation, err.Cause, err.RollbackResult, err.Rollback)
	}
	return fmt.Sprintf("NAT44 operation %s failed: %v; rollback %s", err.Operation, err.Cause, err.RollbackResult)
}

func (err *NAT44LifecycleError) Unwrap() error { return errors.Join(err.Cause, err.Rollback) }

type NAT44ApplyResult struct {
	Receipt  Receipt
	Readback Snapshot
}

func (a Adapter) ApplyNAT44(ctx context.Context, plan NAT44Plan, prior Snapshot, attempted ...NAT44Plan) (NAT44ApplyResult, error) {
	plan.ReadbackStaticMappings = appendMappingsByID(plan.ReadbackStaticMappings, plan.DeleteStaticMappings, prior.NAT.StaticMappings)
	plan.ReadbackPortMappings = appendMappingsByID(plan.ReadbackPortMappings, plan.DeletePortMappings, prior.NAT.PortMappings)
	rollbackPlan := plan
	if len(attempted) > 0 {
		rollbackPlan = attempted[0]
	}
	operations, err := BuildNAT44Operations(plan)
	if err != nil {
		return NAT44ApplyResult{}, err
	}
	if a.Client == nil {
		return NAT44ApplyResult{}, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return NAT44ApplyResult{}, fmt.Errorf("%w: open NAT44 channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	if ingress, ok := channel.(interface{ setNATReturnGuardIngress(string) }); ok {
		ingress.setNATReturnGuardIngress(plan.IngressVPPInterface)
	}
	for _, operation := range operations {
		if _, err := doOperation(ctx, channel, operation); err != nil {
			return NAT44ApplyResult{}, a.nat44Failure(ctx, channel, plan.TransactionID, operation.Name, err, prior, rollbackPlan)
		}
	}
	readback, err := a.snapshotOnChannel(ctx, channel, nat44SnapshotRequestForPlan(plan))
	if err != nil {
		return NAT44ApplyResult{}, a.nat44Failure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	if readback.NAT.Behavior == "" {
		readback.NAT.Behavior = plan.Behavior
	}
	if plan.Behavior != "" && readback.NAT.Behavior != plan.Behavior {
		return NAT44ApplyResult{}, a.nat44Failure(ctx, channel, plan.TransactionID, "readback", fmt.Errorf("NAT behavior readback = %q, want %q", readback.NAT.Behavior, plan.Behavior), prior, rollbackPlan)
	}
	if err := verifyNAT44Readback(readback.NAT, plan); err != nil {
		return NAT44ApplyResult{}, a.nat44Failure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	return NAT44ApplyResult{Receipt: Receipt{RequestID: plan.TransactionID, Operations: operations}, Readback: readback}, nil
}

func BuildNAT44Operations(plan NAT44Plan) ([]Operation, error) {
	transactionID := strings.TrimSpace(plan.TransactionID)
	if transactionID == "" {
		return nil, fmt.Errorf("%w: transaction ID is required", ErrSnapshotIncomplete)
	}
	operations := make([]Operation, 0, len(plan.StaticMappings)+len(plan.PortMappings)+len(plan.DeleteStaticMappings)+len(plan.DeletePortMappings))
	for _, mapping := range plan.StaticMappings {
		operations = append(operations, Operation{Name: "vpp.nat44-ed.static-mapping", RequestID: transactionID, Resource: mapping.ID, Payload: mapping, VPPCtlCommands: resolveNATIngressCommands(natStaticMappingCommandsForBehavior(plan.Behavior, mapping), plan.IngressVPPInterface)})
	}
	for _, mapping := range plan.PortMappings {
		operations = append(operations, Operation{Name: "vpp.nat44-ed.port-map", RequestID: transactionID, Resource: mapping.ID, Payload: mapping, VPPCtlCommands: resolveNATIngressCommands(natPortMappingCommandsForBehavior(plan.Behavior, mapping), plan.IngressVPPInterface)})
	}
	for _, id := range plan.DeleteStaticMappings {
		mapping, found := staticMappingByID(append(plan.ReadbackStaticMappings, plan.StaticMappings...), id)
		if !found {
			return nil, fmt.Errorf("%w: prior NAT static mapping %q is required for deletion", ErrSnapshotIncomplete, id)
		}
		operations = append(operations, Operation{Name: "vpp.nat44-ed.static-mapping", RequestID: transactionID, Resource: id, Payload: mapping, VPPCtlCommands: resolveNATIngressCommands(deleteNATStaticCommandsForBehavior(plan.Behavior, mapping), plan.IngressVPPInterface)})
	}
	for _, id := range plan.DeletePortMappings {
		mapping, found := portMappingByID(append(plan.ReadbackPortMappings, plan.PortMappings...), id)
		if !found {
			return nil, fmt.Errorf("%w: prior NAT port mapping %q is required for deletion", ErrSnapshotIncomplete, id)
		}
		operations = append(operations, Operation{Name: "vpp.nat44-ed.port-map", RequestID: transactionID, Resource: id, Payload: mapping, VPPCtlCommands: resolveNATIngressCommands(deleteNATPortCommandsForBehavior(plan.Behavior, mapping), plan.IngressVPPInterface)})
	}
	return operations, nil
}

// NAT mapping command helpers are shared with the static product plan and use
// a symbolic LAN interface. Runtime gateway application already resolves that
// interface from the active LAN assignment, so never send the symbolic value
// literally to vppctl.
func resolveNATIngressCommands(commands []string, ingress string) []string {
	ingress = strings.TrimSpace(ingress)
	if ingress == "" {
		return append([]string(nil), commands...)
	}
	resolved := make([]string, len(commands))
	for index, command := range commands {
		resolved[index] = strings.ReplaceAll(command, "lyroute-$LY_ROUTE_LAN_INTERFACE", ingress)
	}
	return resolved
}

func (a Adapter) nat44Failure(ctx context.Context, channel Channel, transactionID, operation string, cause error, prior Snapshot, plan NAT44Plan) error {
	rollbackErr := errors.Join(cleanupNAT44(ctx, channel, transactionID, plan), applyNAT44Snapshot(ctx, channel, transactionID, prior))
	readback, err := a.snapshotOnChannel(ctx, channel, nat44SnapshotRequest(transactionID, prior.NAT, plan.IngressVPPInterface))
	if err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	} else if !reflect.DeepEqual(readback.NAT, prior.NAT) {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback readback does not match prior snapshot"))
	}
	result := RollbackSucceeded
	if rollbackErr != nil {
		result = RollbackFailed
	}
	return &NAT44LifecycleError{Operation: operation, Cause: cause, Rollback: rollbackErr, RollbackResult: result}
}

func cleanupNAT44(ctx context.Context, channel Channel, transactionID string, plan NAT44Plan) error {
	var cleanup []error
	for _, mapping := range plan.PortMappings {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.nat44-ed.port-map.rollback-delete", RequestID: transactionID, Resource: mapping.ID, Payload: mapping, VPPCtlCommands: resolveNATIngressCommands(deleteNATPortCommandsForBehavior(plan.Behavior, mapping), plan.IngressVPPInterface)}); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	for _, mapping := range plan.StaticMappings {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.nat44-ed.static-mapping.rollback-delete", RequestID: transactionID, Resource: mapping.ID, Payload: mapping, VPPCtlCommands: resolveNATIngressCommands(deleteNATStaticCommandsForBehavior(plan.Behavior, mapping), plan.IngressVPPInterface)}); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	return errors.Join(cleanup...)
}

func applyNAT44Snapshot(ctx context.Context, channel Channel, transactionID string, snapshot Snapshot) error {
	var replay []error
	for _, mapping := range snapshot.NAT.StaticMappings {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.nat44-ed.static-mapping.rollback", RequestID: transactionID, Resource: mapping.ID, Payload: mapping, VPPCtlCommands: natStaticMappingCommandsForBehavior(snapshot.NAT.Behavior, mapping)}); err != nil {
			replay = append(replay, err)
		}
	}
	for _, mapping := range snapshot.NAT.PortMappings {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.nat44-ed.port-map.rollback", RequestID: transactionID, Resource: mapping.ID, Payload: mapping, VPPCtlCommands: natPortMappingCommandsForBehavior(snapshot.NAT.Behavior, mapping)}); err != nil {
			replay = append(replay, err)
		}
	}
	return errors.Join(replay...)
}

func nat44SnapshotRequest(transactionID string, config nat.CompiledConfig, ingress ...string) SnapshotRequest {
	request := SnapshotRequest{TransactionID: transactionID, NATBehavior: config.Behavior, Capabilities: []SnapshotCapability{SnapshotCapabilityNAT44}, VerifyNATReturnGuards: len(config.StaticMappings)+len(config.PortMappings) > 0}
	if len(ingress) > 0 {
		request.NATIngressVPPInterface = strings.TrimSpace(ingress[0])
	}
	for _, mapping := range config.StaticMappings {
		request.NATStaticMappings = append(request.NATStaticMappings, mapping.ID)
	}
	request.Candidates.NATStaticMappings = append(request.Candidates.NATStaticMappings, config.StaticMappings...)
	for _, mapping := range config.PortMappings {
		request.NATPortMappings = append(request.NATPortMappings, mapping.ID)
	}
	request.Candidates.NATPortMappings = append(request.Candidates.NATPortMappings, config.PortMappings...)
	return request
}

func nat44SnapshotRequestForPlan(plan NAT44Plan) SnapshotRequest {
	staticMappings := append([]nat.StaticMapping(nil), plan.StaticMappings...)
	staticMappings = appendUniqueMappings(staticMappings, plan.ReadbackStaticMappings)
	portMappings := append([]nat.PortMapping(nil), plan.PortMappings...)
	portMappings = appendUniqueMappings(portMappings, plan.ReadbackPortMappings)
	request := nat44SnapshotRequest(plan.TransactionID, nat.CompiledConfig{StaticMappings: staticMappings, PortMappings: portMappings, Behavior: plan.Behavior}, plan.IngressVPPInterface)
	request.NATStaticMappings = withoutIDs(request.NATStaticMappings, plan.DeleteStaticMappings)
	request.NATPortMappings = withoutIDs(request.NATPortMappings, plan.DeletePortMappings)
	request.AbsentNATStatic = append([]string(nil), plan.DeleteStaticMappings...)
	request.AbsentNATPort = append([]string(nil), plan.DeletePortMappings...)
	if len(plan.StaticMappings)+len(plan.PortMappings)+len(plan.DeleteStaticMappings)+len(plan.DeletePortMappings) > 0 {
		request.Capabilities = []SnapshotCapability{SnapshotCapabilityNAT44}
	}
	return request
}

func verifyNAT44Deletes(config nat.CompiledConfig, plan NAT44Plan) error {
	for _, id := range plan.DeleteStaticMappings {
		for _, mapping := range config.StaticMappings {
			if mapping.ID == id {
				return fmt.Errorf("%w: deleted NAT static mapping %q is still present", ErrSnapshotIncomplete, id)
			}
		}
	}
	for _, id := range plan.DeletePortMappings {
		for _, mapping := range config.PortMappings {
			if mapping.ID == id {
				return fmt.Errorf("%w: deleted NAT port mapping %q is still present", ErrSnapshotIncomplete, id)
			}
		}
	}
	return nil
}

func verifyNAT44Readback(config nat.CompiledConfig, plan NAT44Plan) error {
	if err := verifyNAT44Mappings(config.StaticMappings, plan.StaticMappings, "static mapping"); err != nil {
		return err
	}
	if err := verifyNAT44Mappings(config.PortMappings, plan.PortMappings, "port mapping"); err != nil {
		return err
	}
	return verifyNAT44Deletes(config, plan)
}

func verifyNAT44Mappings[T nat.StaticMapping | nat.PortMapping](actual, expected []T, label string) error {
	byID := make(map[string]T, len(actual))
	for _, mapping := range actual {
		byID[mappingID(mapping)] = mapping
	}
	for _, wanted := range expected {
		got, ok := byID[mappingID(wanted)]
		if !ok || !reflect.DeepEqual(got, wanted) {
			return fmt.Errorf("%w: NAT44 %s %q payload does not match requested state", ErrSnapshotIncomplete, label, mappingID(wanted))
		}
	}
	return nil
}
