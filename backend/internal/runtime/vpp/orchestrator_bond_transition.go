package vpp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"ly-route/backend/internal/orchestrator"
)

// TransparentOrchestratorBonds translates the product topology into the
// logical VPP bonds consumed by the transparent orchestrator plugin. The
// product currently exposes one deterministic, low-surprise aggregation mode:
// active-backup. Orchestration groups themselves are deliberately never bonds.
func TransparentOrchestratorBonds(topology orchestrator.TopologyView) []BondState {
	bonds := make([]BondState, 0, len(topology.Interfaces))
	for _, logical := range topology.Interfaces {
		if logical.Bond == nil {
			continue
		}
		members := make([]string, 0, len(logical.Bond.Members))
		for _, member := range logical.Bond.Members {
			members = append(members, transparentVPPInterface(member))
		}
		sort.Strings(members)
		bonds = append(bonds, BondState{Name: transparentVPPInterface(logical.Bond.Name), Mode: "active-backup", Members: members})
	}
	sort.Slice(bonds, func(i, j int) bool { return bonds[i].Name < bonds[j].Name })
	return bonds
}

type TransparentBondTransitionResult struct {
	Previous   []BondState `json:"previous,omitempty"`
	Current    []BondState `json:"current,omitempty"`
	Operations []Operation `json:"operations,omitempty"`
}

type TransparentBondTransitionError struct {
	Cause    error
	Rollback error
}

func (err *TransparentBondTransitionError) Error() string {
	if err.Rollback != nil {
		return fmt.Sprintf("transparent orchestrator bond transition failed: %v; rollback failed: %v", err.Cause, err.Rollback)
	}
	return fmt.Sprintf("transparent orchestrator bond transition failed: %v; rollback succeeded", err.Cause)
}

func (err *TransparentBondTransitionError) Unwrap() error {
	return errors.Join(err.Cause, err.Rollback)
}

// ApplyTransparentBondTransition reconciles only the product-owned bonds named
// by previous or desired. It reads the enforcing VPP state first, makes the
// smallest deterministic change, verifies semantic readback, and restores the
// observed pre-state when any command or verification fails.
func (adapter Adapter) ApplyTransparentBondTransition(ctx context.Context, transactionID string, previous, desired []BondState) (TransparentBondTransitionResult, error) {
	transactionID = strings.TrimSpace(transactionID)
	if transactionID == "" {
		return TransparentBondTransitionResult{}, fmt.Errorf("%w: transaction ID is required", ErrSnapshotIncomplete)
	}
	known, err := mergeTransparentBondStates(previous, desired)
	if err != nil {
		return TransparentBondTransitionResult{}, err
	}
	observed, err := adapter.transparentBondInventory(ctx, transactionID+"-before", known)
	if err != nil {
		return TransparentBondTransitionResult{}, err
	}
	operations, err := BuildTransparentBondTransitionOperations(transactionID, observed, desired)
	if err != nil {
		return TransparentBondTransitionResult{}, err
	}
	if err := adapter.ExecuteOperations(ctx, operations); err != nil {
		rollbackErr := adapter.restoreTransparentBonds(ctx, transactionID+"-rollback", known, observed)
		return TransparentBondTransitionResult{}, &TransparentBondTransitionError{Cause: err, Rollback: rollbackErr}
	}
	readback, err := adapter.transparentBondInventory(ctx, transactionID+"-after", known)
	if err == nil {
		err = verifyTransparentBondTransition(readback, observed, desired)
	}
	if err != nil {
		rollbackErr := adapter.restoreTransparentBonds(ctx, transactionID+"-rollback", known, observed)
		return TransparentBondTransitionResult{}, &TransparentBondTransitionError{Cause: err, Rollback: rollbackErr}
	}
	return TransparentBondTransitionResult{Previous: observed, Current: readback, Operations: operations}, nil
}

func (adapter Adapter) restoreTransparentBonds(ctx context.Context, transactionID string, known, target []BondState) error {
	current, err := adapter.transparentBondInventory(ctx, transactionID+"-before", known)
	if err != nil {
		return err
	}
	operations, err := BuildTransparentBondTransitionOperations(transactionID, current, target)
	if err != nil {
		return err
	}
	if err := adapter.ExecuteOperations(ctx, operations); err != nil {
		return err
	}
	readback, err := adapter.transparentBondInventory(ctx, transactionID+"-after", known)
	if err != nil {
		return err
	}
	return verifyTransparentBondTransition(readback, current, target)
}

func BuildTransparentBondTransitionOperations(transactionID string, current, desired []BondState) ([]Operation, error) {
	currentIndex, err := indexTransparentBonds(current)
	if err != nil {
		return nil, err
	}
	desiredIndex, err := indexTransparentBonds(desired)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(currentIndex)+len(desiredIndex))
	for name := range currentIndex {
		names = append(names, name)
	}
	for name := range desiredIndex {
		if _, exists := currentIndex[name]; !exists {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	operations := make([]Operation, 0, len(names)*2)
	for _, name := range names {
		currentState, exists := currentIndex[name]
		desiredState, wanted := desiredIndex[name]
		if exists && (!wanted || !sameTransparentBond(currentState, desiredState)) {
			operations = append(operations, Operation{Name: "vpp.transparent-orchestrator.bond-delete", RequestID: transactionID, Resource: name, Payload: currentState, VPPCtlCommands: []string{"delete bond " + name}})
		}
	}
	for _, name := range names {
		currentState, exists := currentIndex[name]
		desiredState, wanted := desiredIndex[name]
		if wanted && (!exists || !sameTransparentBond(currentState, desiredState)) {
			commands := append(createBondCommands(desiredState), "set interface state "+desiredState.Name+" up")
			operations = append(operations, Operation{Name: "vpp.transparent-orchestrator.bond-create", RequestID: transactionID, Resource: name, Payload: desiredState, VPPCtlCommands: commands})
		}
	}
	return operations, nil
}

func (adapter Adapter) transparentBondInventory(ctx context.Context, transactionID string, known []BondState) ([]BondState, error) {
	if adapter.Client == nil {
		return nil, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := adapter.Client.OpenChannel(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: open bond inventory channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	operation := Operation{Name: "vpp.transparent-orchestrator.bond-inventory", RequestID: transactionID, Resource: "orchestrator-bonds", VPPCtlCommands: []string{"show bond details"}}
	reply, err := channel.Do(ctx, operation)
	if err != nil {
		return nil, err
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok || len(payload.CommandResults) != 1 || payload.CommandResults[0].Command != "show bond details" {
		return nil, fmt.Errorf("%w: transparent bond inventory readback is missing", ErrSnapshotIncomplete)
	}
	return parseTransparentBondInventory(payload.CommandResults[0].Stdout, known)
}

func parseTransparentBondInventory(output string, known []BondState) ([]BondState, error) {
	knownByID := make(map[int]BondState, len(known))
	knownByName := make(map[string]BondState, len(known))
	for _, state := range known {
		id, _ := vppBondIdentity(state.Name)
		if prior, exists := knownByID[id]; exists && prior.Name != state.Name {
			return nil, fmt.Errorf("bond names %q and %q resolve to VPP interface id %d", prior.Name, state.Name, id)
		}
		knownByID[id] = state
		knownByName[state.Name] = state
	}
	type parsedBond struct {
		state          BondState
		interfaceID    int
		hasInterfaceID bool
		expected       int
		readingMembers bool
	}
	var parsed []parsedBond
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		unindented := len(raw) > 0 && !unicode.IsSpace(rune(raw[0]))
		if unindented && !strings.Contains(line, ":") {
			parsed = append(parsed, parsedBond{state: BondState{Name: line}, interfaceID: -1, expected: -1})
			continue
		}
		if len(parsed) == 0 {
			return nil, fmt.Errorf("%w: bond inventory row has no object: %q", ErrSnapshotIncomplete, line)
		}
		item := &parsed[len(parsed)-1]
		switch {
		case strings.HasPrefix(line, "mode:"):
			item.state.Mode = strings.TrimSpace(strings.TrimPrefix(line, "mode:"))
			item.readingMembers = false
		case strings.HasPrefix(line, "number of members:"):
			memberCount, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "number of members:")))
			if parseErr != nil || memberCount < 0 {
				return nil, fmt.Errorf("%w: malformed bond member count %q", ErrSnapshotIncomplete, line)
			}
			item.expected = memberCount
			item.readingMembers = true
		case strings.HasPrefix(line, "number of active members:"), strings.HasPrefix(line, "load balance:"):
			item.readingMembers = false
		case strings.HasPrefix(line, "interface id:"):
			interfaceID, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "interface id:")))
			if parseErr != nil || interfaceID < 0 {
				return nil, fmt.Errorf("%w: malformed bond interface id %q", ErrSnapshotIncomplete, line)
			}
			item.interfaceID = interfaceID
			item.hasInterfaceID = true
			item.readingMembers = false
		case strings.HasPrefix(line, "weight:"), strings.HasPrefix(line, "last xmit member index:"), strings.HasPrefix(line, "device instance:"), strings.HasPrefix(line, "sw_if_index:"), strings.HasPrefix(line, "hw_if_index:"), line == "gso enable":
			item.readingMembers = false
		case item.readingMembers:
			if len(strings.Fields(line)) != 1 {
				return nil, fmt.Errorf("%w: malformed bond member %q", ErrSnapshotIncomplete, line)
			}
			item.state.Members = append(item.state.Members, line)
		}
	}
	result := []BondState{}
	for _, item := range parsed {
		if item.state.Mode == "" || item.expected < 0 || len(item.state.Members) != item.expected {
			return nil, fmt.Errorf("%w: bond %q inventory is incomplete", ErrSnapshotIncomplete, item.state.Name)
		}
		if mapped, found := knownByName[item.state.Name]; found {
			item.state.Name = mapped.Name
		} else if item.hasInterfaceID {
			if mapped, found := knownByID[item.interfaceID]; found {
				item.state.Name = mapped.Name
			} else {
				continue
			}
		} else {
			continue
		}
		sort.Strings(item.state.Members)
		result = append(result, item.state)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func verifyTransparentBondTransition(readback, previous, desired []BondState) error {
	readbackIndex, err := indexTransparentBonds(readback)
	if err != nil {
		return err
	}
	desiredIndex, err := indexTransparentBonds(desired)
	if err != nil {
		return err
	}
	for name, expected := range desiredIndex {
		observed, found := readbackIndex[name]
		if !found || !sameTransparentBond(observed, expected) {
			return fmt.Errorf("%w: bond %q readback does not match desired state", ErrSnapshotIncomplete, name)
		}
	}
	for _, old := range previous {
		if _, wanted := desiredIndex[old.Name]; wanted {
			continue
		}
		if _, found := readbackIndex[old.Name]; found {
			return fmt.Errorf("%w: removed bond %q remains active", ErrSnapshotIncomplete, old.Name)
		}
	}
	return nil
}

func mergeTransparentBondStates(groups ...[]BondState) ([]BondState, error) {
	merged := map[string]BondState{}
	for _, states := range groups {
		for _, state := range states {
			normalized := normalizeTransparentBond(state)
			if prior, exists := merged[normalized.Name]; exists && !sameTransparentBond(prior, normalized) {
				// The desired definition is authoritative for name-to-id decoding;
				// either definition still maps the same stable logical name.
			}
			merged[normalized.Name] = normalized
		}
	}
	result := make([]BondState, 0, len(merged))
	for _, state := range merged {
		result = append(result, state)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	_, err := indexTransparentBonds(result)
	return result, err
}

func indexTransparentBonds(states []BondState) (map[string]BondState, error) {
	index := make(map[string]BondState, len(states))
	ids := map[int]string{}
	for _, raw := range states {
		state := normalizeTransparentBond(raw)
		if state.Name == "" || state.Mode == "" || len(state.Members) < 2 {
			return nil, fmt.Errorf("invalid transparent orchestrator bond %#v", raw)
		}
		if _, duplicate := index[state.Name]; duplicate {
			return nil, fmt.Errorf("duplicate transparent orchestrator bond %q", state.Name)
		}
		id, _ := vppBondIdentity(state.Name)
		if other, collision := ids[id]; collision && other != state.Name {
			return nil, fmt.Errorf("bond names %q and %q resolve to VPP interface id %d", other, state.Name, id)
		}
		ids[id] = state.Name
		index[state.Name] = state
	}
	return index, nil
}

func normalizeTransparentBond(state BondState) BondState {
	state.Name = strings.TrimSpace(state.Name)
	state.Mode = strings.TrimSpace(state.Mode)
	if state.Mode == "lacp" {
		state.Mode = "802.3ad"
	}
	for index := range state.Members {
		state.Members[index] = strings.TrimSpace(state.Members[index])
	}
	sort.Strings(state.Members)
	return state
}

func sameTransparentBond(left, right BondState) bool {
	return reflect.DeepEqual(normalizeTransparentBond(left), normalizeTransparentBond(right))
}
