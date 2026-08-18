package vpp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

type InterfaceBondPlan struct {
	TransactionID        string
	ManagementInterface  string
	Interfaces           []InterfaceState
	Bonds                []BondState
	DeleteInterfaces     []string
	DeleteBonds          []string
	DeleteInterfaceState []InterfaceState
	DeleteBondState      []BondState
}

type RollbackResult string

const (
	RollbackSucceeded RollbackResult = "succeeded"
	RollbackFailed    RollbackResult = "failed"
)

type InterfaceBondLifecycleError struct {
	Operation      string
	Cause          error
	Rollback       error
	RollbackResult RollbackResult
}

func (err *InterfaceBondLifecycleError) Error() string {
	if err.Rollback != nil {
		return fmt.Sprintf("interface/bond operation %s failed: %v; rollback %s: %v", err.Operation, err.Cause, err.RollbackResult, err.Rollback)
	}
	return fmt.Sprintf("interface/bond operation %s failed: %v; rollback %s", err.Operation, err.Cause, err.RollbackResult)
}

func (err *InterfaceBondLifecycleError) Unwrap() error { return errors.Join(err.Cause, err.Rollback) }

type InterfaceBondApplyResult struct {
	Receipt  Receipt
	Readback Snapshot
}

func BuildInterfaceBondOperations(plan InterfaceBondPlan) ([]Operation, error) {
	transactionID := strings.TrimSpace(plan.TransactionID)
	if transactionID == "" {
		return nil, fmt.Errorf("%w: transaction ID is required", ErrSnapshotIncomplete)
	}
	management := strings.TrimSpace(plan.ManagementInterface)
	operations := make([]Operation, 0, len(plan.Interfaces)+len(plan.Bonds)+len(plan.DeleteInterfaces)+len(plan.DeleteBonds))
	for _, state := range plan.Interfaces {
		if strings.TrimSpace(state.Name) == management && management != "" {
			return nil, interfaceBondManagementLock(management, "vpp.interface.address")
		}
		commands := []string{fmt.Sprintf("set interface state %s %s", state.Name, state.AdminState)}
		for _, address := range state.Addresses {
			commands = append(commands, fmt.Sprintf("set interface ip address %s %s", state.Name, address))
		}
		operations = append(operations, Operation{Name: "vpp.interface.address", RequestID: transactionID, Resource: state.Name, Payload: state, VPPCtlCommands: commands})
	}
	for _, name := range plan.DeleteInterfaces {
		if strings.TrimSpace(name) == management && management != "" {
			return nil, interfaceBondManagementLock(management, "vpp.interface.address")
		}
		state, found := interfaceStateByName(plan.DeleteInterfaceState, name)
		if !found {
			state, found = interfaceStateByName(plan.Interfaces, name)
		}
		if !found {
			return nil, fmt.Errorf("%w: prior interface state %q is required for deletion", ErrSnapshotIncomplete, name)
		}
		operations = append(operations, Operation{Name: "vpp.interface.address", RequestID: transactionID, Resource: name, VPPCtlCommands: deleteInterfaceCommands(state)})
	}
	bondIDs := make(map[int]string, len(plan.Bonds))
	for _, bond := range plan.Bonds {
		for _, member := range bond.Members {
			if strings.TrimSpace(member) == management && management != "" {
				return nil, interfaceBondManagementLock(management, "vpp.interface-bond")
			}
		}
		bondID, _ := vppBondIdentity(bond.Name)
		if existing, collision := bondIDs[bondID]; collision && existing != bond.Name {
			return nil, fmt.Errorf("bond names %q and %q resolve to the same VPP interface id %d", existing, bond.Name, bondID)
		}
		bondIDs[bondID] = bond.Name
		commands := createBondCommands(bond)
		operations = append(operations, Operation{Name: "vpp.interface-bond", RequestID: transactionID, Resource: bond.Name, Payload: bond, VPPCtlCommands: commands})
	}
	for _, name := range plan.DeleteBonds {
		operations = append(operations, Operation{Name: "vpp.interface-bond", RequestID: transactionID, Resource: name, VPPCtlCommands: []string{fmt.Sprintf("delete bond %s", name)}})
	}
	return operations, nil
}

func interfaceBondManagementLock(management, operation string) error {
	return &DataplaneLockedError{Prerequisites: []PrerequisiteResult{{Name: "management_excluded_from_operations", Interface: management, Passed: false, Reason: "operation " + operation + " references the management interface"}}}
}

func (a Adapter) ApplyInterfaceBond(ctx context.Context, plan InterfaceBondPlan, prior Snapshot, attempted ...InterfaceBondPlan) (InterfaceBondApplyResult, error) {
	plan.DeleteInterfaceState = appendInterfaceDeleteState(plan.DeleteInterfaceState, plan.DeleteInterfaces, prior.Interfaces)
	plan.DeleteBondState = appendBondDeleteState(plan.DeleteBondState, plan.DeleteBonds, prior.Bonds)
	rollbackPlan := plan
	if len(attempted) > 0 {
		rollbackPlan = attempted[0]
	}
	operations, err := BuildInterfaceBondOperations(plan)
	if err != nil {
		return InterfaceBondApplyResult{}, err
	}
	if a.Client == nil {
		return InterfaceBondApplyResult{}, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return InterfaceBondApplyResult{}, fmt.Errorf("%w: open apply channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	for _, operation := range operations {
		if _, err := doOperation(ctx, channel, operation); err != nil {
			return InterfaceBondApplyResult{}, a.interfaceBondFailure(ctx, channel, plan.TransactionID, operation.Name, err, prior, rollbackPlan)
		}
	}
	readback, err := a.Snapshot(ctx, lifecycleSnapshotRequest(plan.TransactionID, plan))
	if err != nil {
		return InterfaceBondApplyResult{}, a.interfaceBondFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	return InterfaceBondApplyResult{Receipt: Receipt{RequestID: plan.TransactionID, Operations: operations}, Readback: readback}, nil
}

func (a Adapter) interfaceBondFailure(ctx context.Context, channel Channel, transactionID, operation string, cause error, prior Snapshot, current InterfaceBondPlan) error {
	rollbackErr := errors.Join(cleanupInterfaceBond(ctx, channel, transactionID, current), applySnapshot(ctx, channel, transactionID, prior))
	readback, err := a.Snapshot(ctx, lifecycleSnapshotRequest(transactionID, InterfaceBondPlan{Interfaces: prior.Interfaces, Bonds: prior.Bonds}))
	if err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	} else if !reflect.DeepEqual(readback.Interfaces, prior.Interfaces) || !reflect.DeepEqual(readback.Bonds, prior.Bonds) {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback readback does not match prior snapshot"))
	}
	result := RollbackSucceeded
	if rollbackErr != nil {
		result = RollbackFailed
	}
	return &InterfaceBondLifecycleError{Operation: operation, Cause: cause, Rollback: rollbackErr, RollbackResult: result}
}

func cleanupInterfaceBond(ctx context.Context, channel Channel, transactionID string, plan InterfaceBondPlan) error {
	var cleanup []error
	for _, bond := range plan.Bonds {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.interface-bond.rollback-delete", RequestID: transactionID, Resource: bond.Name, VPPCtlCommands: []string{fmt.Sprintf("delete bond %s", bond.Name)}}); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	for _, state := range plan.Interfaces {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.interface.address.rollback-delete", RequestID: transactionID, Resource: state.Name, VPPCtlCommands: deleteInterfaceCommands(state)}); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	return errors.Join(cleanup...)
}

func deleteInterfaceCommands(state InterfaceState) []string {
	commands := []string{fmt.Sprintf("set interface state %s down", state.Name)}
	for _, cidr := range state.Addresses {
		commands = append(commands, fmt.Sprintf("set interface ip address del %s %s", state.Name, cidr))
	}
	return commands
}

func interfaceStateByName(states []InterfaceState, name string) (InterfaceState, bool) {
	for _, state := range states {
		if state.Name == name {
			return state, true
		}
	}
	return InterfaceState{}, false
}

func appendInterfaceDeleteState(existing []InterfaceState, ids []string, prior []InterfaceState) []InterfaceState {
	states := append([]InterfaceState(nil), existing...)
	for _, id := range ids {
		if _, found := interfaceStateByName(states, id); found {
			continue
		}
		if state, found := interfaceStateByName(prior, id); found {
			states = append(states, state)
		}
	}
	return states
}

func applySnapshot(ctx context.Context, channel Channel, transactionID string, snapshot Snapshot) error {
	var replay []error
	for _, operation := range snapshotOperations(transactionID, snapshot) {
		if _, err := doOperation(ctx, channel, operation); err != nil {
			replay = append(replay, err)
		}
	}
	return errors.Join(replay...)
}

func doOperation(ctx context.Context, channel Channel, operation Operation) (Reply, error) {
	reply, err := channel.Do(ctx, operation)
	if err != nil {
		return Reply{}, &VPPError{Operation: operation.Name, RequestID: operation.RequestID, Err: err}
	}
	if err := NormalizeRetval(operation.Name, operation.RequestID, reply.Retval); err != nil {
		return Reply{}, err
	}
	return reply, nil
}

func snapshotOperations(transactionID string, snapshot Snapshot) []Operation {
	operations := make([]Operation, 0, len(snapshot.Interfaces)+len(snapshot.Bonds))
	for _, state := range snapshot.Interfaces {
		commands := []string{fmt.Sprintf("set interface state %s %s", state.Name, state.AdminState)}
		for _, address := range state.Addresses {
			commands = append(commands, fmt.Sprintf("set interface ip address %s %s", state.Name, address))
		}
		operations = append(operations, Operation{Name: "vpp.interface.address.rollback", RequestID: transactionID, Resource: state.Name, VPPCtlCommands: commands})
	}
	for _, bond := range snapshot.Bonds {
		commands := createBondCommands(bond)
		operations = append(operations, Operation{Name: "vpp.interface-bond.rollback", RequestID: transactionID, Resource: bond.Name, VPPCtlCommands: commands})
	}
	return operations
}

func interfaceNames(states []InterfaceState) []string {
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, state.Name)
	}
	return names
}

func bondNames(states []BondState) []string {
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, state.Name)
	}
	return names
}

func lifecycleSnapshotRequest(transactionID string, plan InterfaceBondPlan) SnapshotRequest {
	interfaces := append([]InterfaceState(nil), plan.Interfaces...)
	bonds := append([]BondState(nil), plan.Bonds...)
	capabilities := make([]SnapshotCapability, 0, 2)
	if len(interfaces) > 0 || len(plan.DeleteInterfaces) > 0 {
		capabilities = append(capabilities, SnapshotCapabilityInterfaces)
	}
	if len(bonds) > 0 || len(plan.DeleteBonds) > 0 {
		capabilities = append(capabilities, SnapshotCapabilityBonds)
	}
	return SnapshotRequest{
		TransactionID:    transactionID,
		Interfaces:       interfaceNames(interfaces),
		AbsentInterfaces: append([]string(nil), plan.DeleteInterfaces...),
		Bonds:            bondNames(bonds),
		AbsentBonds:      append([]string(nil), plan.DeleteBonds...),
		Capabilities:     capabilities,
		Candidates: SnapshotCandidates{
			Interfaces: append(append([]InterfaceState(nil), interfaces...), plan.DeleteInterfaceState...),
			Bonds:      append(append([]BondState(nil), bonds...), plan.DeleteBondState...),
		},
	}
}
