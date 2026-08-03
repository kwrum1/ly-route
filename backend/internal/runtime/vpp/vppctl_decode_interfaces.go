package vpp

import (
	"net/netip"
	"strconv"
	"strings"
)

func decodeVPPCTLInterfaces(request SnapshotRequest, results []VPPCTLCommandResult) (InterfaceReadback, error) {
	if err := requireInterfaceCandidates(request); err != nil {
		return InterfaceReadback{}, err
	}
	output, err := commandOutput(results, "show interface address")
	if err != nil {
		return InterfaceReadback{}, err
	}
	states := make([]InterfaceState, 0, len(request.Candidates.Interfaces)+1)
	seen := make(map[string]struct{})
	current := -1
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "):") {
			open := strings.LastIndex(line, " (")
			if open < 1 {
				return InterfaceReadback{}, snapshotDecodeError("unknown interface grammar %q", line)
			}
			name := strings.TrimSpace(line[:open])
			state, err := interfaceState(strings.TrimSuffix(line[open+2:], "):"))
			if err != nil {
				return InterfaceReadback{}, err
			}
			if _, duplicate := seen[name]; duplicate {
				return InterfaceReadback{}, snapshotDecodeError("interface %q is ambiguous", name)
			}
			seen[name] = struct{}{}
			states = append(states, InterfaceState{Name: name, AdminState: state, LinkState: state})
			current = len(states) - 1
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "L3" || current < 0 {
			return InterfaceReadback{}, snapshotDecodeError("unknown interface address grammar %q", line)
		}
		prefix, parseErr := netip.ParsePrefix(fields[1])
		if parseErr != nil {
			return InterfaceReadback{}, snapshotDecodeError("malformed interface address %q", fields[1])
		}
		states[current].Addresses = append(states[current].Addresses, prefix.String())
	}
	if len(states) == 0 {
		return InterfaceReadback{}, snapshotDecodeError("interface output contained no rows")
	}
	selected := make([]InterfaceState, 0, len(request.Interfaces))
	for _, name := range request.Interfaces {
		found := false
		for _, state := range states {
			if state.Name == strings.TrimSpace(name) {
				selected = append(selected, state)
				found = true
				break
			}
		}
		if !found {
			return InterfaceReadback{}, snapshotDecodeError("interface %q was not returned", name)
		}
	}
	return InterfaceReadback{Interfaces: selected}, nil
}

func interfaceState(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "up":
		return "up", nil
	case "dn", "down":
		return "down", nil
	default:
		return "", snapshotDecodeError("unknown interface state %q", value)
	}
}

func requireInterfaceCandidates(request SnapshotRequest) error {
	candidates := make(map[string]struct{}, len(request.Candidates.Interfaces))
	for _, candidate := range request.Candidates.Interfaces {
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			return snapshotDecodeError("interface candidate name is empty")
		}
		if _, duplicate := candidates[name]; duplicate {
			return snapshotDecodeError("interface candidate %q is ambiguous", name)
		}
		candidates[name] = struct{}{}
	}
	return requireCandidateNames(request.Interfaces, candidates, "interface")
}

func decodeVPPCTLBonds(request SnapshotRequest, results []VPPCTLCommandResult) (BondReadback, error) {
	candidates := make(map[string]BondState, len(request.Candidates.Bonds))
	candidateByID := make(map[int]string, len(request.Candidates.Bonds))
	for _, candidate := range request.Candidates.Bonds {
		if _, duplicate := candidates[candidate.Name]; duplicate {
			return BondReadback{}, snapshotDecodeError("bond candidate %q is ambiguous", candidate.Name)
		}
		candidates[candidate.Name] = candidate
		id, _ := vppBondIdentity(candidate.Name)
		if existing, duplicate := candidateByID[id]; duplicate && existing != candidate.Name {
			return BondReadback{}, snapshotDecodeError("bond candidates %q and %q share VPP interface id %d", existing, candidate.Name, id)
		}
		candidateByID[id] = candidate.Name
	}
	if err := requireBondCandidateNames(request.Bonds, candidates); err != nil {
		return BondReadback{}, err
	}
	output, err := commandOutput(results, "show bond details")
	if err != nil {
		return BondReadback{}, err
	}
	var bonds []BondState
	expectedMembers := -1
	readingActiveMembers := false
	readingMembers := false
	for _, line := range nonBlankLines(output) {
		if line == "" {
			continue
		}
		current := len(bonds) - 1
		switch {
		case strings.HasPrefix(line, "mode:"):
			if current < 0 || bonds[current].Mode != "" {
				return BondReadback{}, snapshotDecodeError("ambiguous bond mode %q", line)
			}
			bonds[current].Mode = strings.TrimSpace(strings.TrimPrefix(line, "mode:"))
		case strings.HasPrefix(line, "number of members:"):
			if current < 0 {
				return BondReadback{}, snapshotDecodeError("bond member count has no object")
			}
			expectedMembers, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "number of members:")))
			if err != nil || expectedMembers < 0 {
				return BondReadback{}, snapshotDecodeError("malformed bond member count %q", line)
			}
			readingActiveMembers = false
			readingMembers = true
		case strings.HasPrefix(line, "number of active members:"):
			readingActiveMembers = true
			readingMembers = false
		case strings.HasPrefix(line, "load balance:"), strings.HasPrefix(line, "weight:"), strings.HasPrefix(line, "last xmit member index:"), line == "gso enable":
			readingMembers = false
		case strings.HasPrefix(line, "interface id:"):
			if current < 0 {
				return BondReadback{}, snapshotDecodeError("bond interface id has no object")
			}
			id, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "interface id:")))
			if parseErr != nil || id < 0 {
				return BondReadback{}, snapshotDecodeError("malformed bond interface id %q", line)
			}
			if candidateName, found := candidateByID[id]; found {
				bonds[current].Name = candidateName
			}
			readingActiveMembers = false
			readingMembers = false
		case strings.HasPrefix(line, "device instance:"), strings.HasPrefix(line, "sw_if_index:"), strings.HasPrefix(line, "hw_if_index:"):
			readingActiveMembers = false
			readingMembers = false
		case readingActiveMembers:
			if len(strings.Fields(line)) != 1 {
				return BondReadback{}, snapshotDecodeError("unknown active bond member grammar %q", line)
			}
		case readingMembers:
			if current < 0 || len(strings.Fields(line)) != 1 {
				return BondReadback{}, snapshotDecodeError("unknown bond member grammar %q", line)
			}
			bonds[current].Members = append(bonds[current].Members, line)
		default:
			if current >= 0 && (bonds[current].Mode == "" || expectedMembers < 0 || len(bonds[current].Members) != expectedMembers) {
				return BondReadback{}, snapshotDecodeError("bond %q output is incomplete", bonds[current].Name)
			}
			if len(strings.Fields(line)) != 1 {
				return BondReadback{}, snapshotDecodeError("unknown bond grammar %q", line)
			}
			if _, duplicate := bondByName(bonds, line); duplicate {
				return BondReadback{}, snapshotDecodeError("bond %q is ambiguous", line)
			}
			bonds = append(bonds, BondState{Name: line})
			expectedMembers = -1
			readingActiveMembers = false
			readingMembers = false
		}
	}
	if len(bonds) == 0 || bonds[len(bonds)-1].Mode == "" || expectedMembers < 0 || len(bonds[len(bonds)-1].Members) != expectedMembers {
		return BondReadback{}, snapshotDecodeError("bond output is incomplete")
	}
	selected := make([]BondState, 0, len(request.Bonds))
	for _, name := range request.Bonds {
		bond, found := bondByName(bonds, strings.TrimSpace(name))
		if !found {
			return BondReadback{}, snapshotDecodeError("bond %q was not returned", name)
		}
		if strings.TrimSpace(candidates[bond.Name].Mode) != bond.Mode {
			return BondReadback{}, snapshotDecodeError("bond %q mode does not match candidate", bond.Name)
		}
		selected = append(selected, bond)
	}
	return BondReadback{Bonds: selected}, nil
}

func requireCandidateNames(names []string, candidates map[string]struct{}, kind string) error {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, ok := candidates[name]; !ok {
			return snapshotDecodeError("%s %q has no candidate", kind, name)
		}
	}
	return nil
}

func requireBondCandidateNames(names []string, candidates map[string]BondState) error {
	plain := make(map[string]struct{}, len(candidates))
	for name := range candidates {
		plain[name] = struct{}{}
	}
	return requireCandidateNames(names, plain, "bond")
}

func bondByName(bonds []BondState, name string) (BondState, bool) {
	for _, bond := range bonds {
		if bond.Name == name {
			return bond, true
		}
	}
	return BondState{}, false
}
