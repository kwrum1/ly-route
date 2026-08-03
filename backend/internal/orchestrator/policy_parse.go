package orchestrator

import (
	"fmt"
	"math/big"
	"net/netip"
	"slices"
	"strings"
)

func ParsePolicy(topology Topology, input PolicyInput) (Policy, error) {
	if input.SchemaVersion != PolicySchemaVersion {
		return Policy{}, fmt.Errorf("%w: %d", ErrInvalidPolicyVersion, input.SchemaVersion)
	}
	objects, objectIndex, err := parsePolicyIPObjects(input.IPObjects)
	if err != nil {
		return Policy{}, err
	}
	topologyGroups := make(map[string]InterfaceName, len(topology.groups))
	for _, group := range topology.groupValues() {
		topologyGroups[group.Name()] = group.name
	}
	groups, err := parsePolicyGroups(input.Groups, objectIndex, topologyGroups)
	if err != nil {
		return Policy{}, err
	}
	defaultAction, err := parsePolicyAction(input.Default, topologyGroups)
	if input.Default.Kind == "" {
		return Policy{}, ErrEmptyPolicyDefault
	}
	if err != nil {
		return Policy{}, fmt.Errorf("default: %w", err)
	}
	if defaultAction.kind == ActionVia {
		return Policy{}, fmt.Errorf("default: %w: via is not terminal", ErrInvalidPolicyAction)
	}
	return Policy{ipObjects: objects, groups: groups, defaultAction: defaultAction, parsed: true}, nil
}

func parsePolicyIPObjects(inputs []IPObjectInput) ([]policyIPObject, map[string]policyIPObject, error) {
	objects := make([]policyIPObject, 0, len(inputs))
	index := make(map[string]policyIPObject, len(inputs))
	for _, input := range inputs {
		id, err := parsePolicyName(input.ID)
		if err != nil || len(input.Prefixes) == 0 {
			return nil, nil, fmt.Errorf("%w: %q", ErrInvalidIPObject, input.ID)
		}
		if _, exists := index[id.String()]; exists {
			return nil, nil, fmt.Errorf("%w: %q", ErrDuplicateIPObject, id)
		}
		prefixes, err := parsePrefixes(input.Prefixes)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %q: %v", ErrInvalidIPObject, id, err)
		}
		object := policyIPObject{id: id, prefixes: prefixes}
		objects = append(objects, object)
		index[id.String()] = object
	}
	slices.SortFunc(objects, func(left, right policyIPObject) int { return strings.Compare(left.id.String(), right.id.String()) })
	return objects, index, nil
}

func parsePrefixes(raw []string) ([]netip.Prefix, error) {
	unique := make(map[netip.Prefix]struct{}, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if strings.Contains(value, "-") {
			start, end, ok := parsePolicyIPRange(value)
			if !ok {
				return nil, fmt.Errorf("invalid IP range %q", value)
			}
			for _, prefix := range policyAddressRangePrefixes(start, end) {
				unique[prefix.Masked()] = struct{}{}
			}
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil {
				return nil, err
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		unique[prefix.Masked()] = struct{}{}
	}
	prefixes := make([]netip.Prefix, 0, len(unique))
	for prefix := range unique {
		prefixes = append(prefixes, prefix)
	}
	slices.SortFunc(prefixes, func(left, right netip.Prefix) int { return strings.Compare(left.String(), right.String()) })
	return prefixes, nil
}

func parsePolicyIPRange(value string) (netip.Addr, netip.Addr, bool) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return netip.Addr{}, netip.Addr{}, false
	}
	start, startErr := netip.ParseAddr(strings.TrimSpace(parts[0]))
	end, endErr := netip.ParseAddr(strings.TrimSpace(parts[1]))
	if startErr != nil || endErr != nil || start.BitLen() != end.BitLen() || start.Compare(end) > 0 {
		return netip.Addr{}, netip.Addr{}, false
	}
	return start, end, true
}

func policyAddressRangePrefixes(start, end netip.Addr) []netip.Prefix {
	bitLen := start.BitLen()
	current := policyAddressInteger(start)
	last := policyAddressInteger(end)
	one := big.NewInt(1)
	result := []netip.Prefix{}
	for current.Cmp(last) <= 0 {
		alignmentBits := 0
		for alignmentBits < bitLen && current.Bit(alignmentBits) == 0 {
			alignmentBits++
		}
		remaining := new(big.Int).Sub(last, current)
		remaining.Add(remaining, one)
		blockBits := alignmentBits
		if sizeBits := remaining.BitLen() - 1; sizeBits < blockBits {
			blockBits = sizeBits
		}
		result = append(result, netip.PrefixFrom(policyIntegerAddress(current, bitLen), bitLen-blockBits))
		current.Add(current, new(big.Int).Lsh(new(big.Int).Set(one), uint(blockBits)))
	}
	return result
}

func policyAddressInteger(addr netip.Addr) *big.Int {
	if addr.Is4() {
		raw := addr.As4()
		return new(big.Int).SetBytes(raw[:])
	}
	raw := addr.As16()
	return new(big.Int).SetBytes(raw[:])
}

func policyIntegerAddress(value *big.Int, bitLen int) netip.Addr {
	if bitLen == 32 {
		var raw [4]byte
		value.FillBytes(raw[:])
		return netip.AddrFrom4(raw)
	}
	var raw [16]byte
	value.FillBytes(raw[:])
	return netip.AddrFrom16(raw)
}

func parsePolicyGroups(inputs []PolicyGroupInput, objects map[string]policyIPObject, topologyGroups map[string]InterfaceName) ([]policyGroup, error) {
	groups := make([]policyGroup, 0, len(inputs))
	ids := make(map[string]struct{}, len(inputs))
	positions := make(map[int]struct{}, len(inputs))
	targetOwners := make(map[string]string)
	for _, input := range inputs {
		id, err := parsePolicyName(input.ID)
		if err != nil || len(input.Rules) == 0 {
			return nil, fmt.Errorf("%w: %q", ErrInvalidPolicyGroup, input.ID)
		}
		if _, exists := ids[id.String()]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicatePolicyGroup, id)
		}
		if input.Position <= 0 {
			return nil, fmt.Errorf("%w: %q", ErrInvalidPolicyPosition, id)
		}
		if _, exists := positions[input.Position]; exists {
			return nil, fmt.Errorf("%w: %d", ErrInvalidPolicyPosition, input.Position)
		}
		rules, targets, err := parsePolicyRules(input.Rules, objects, topologyGroups)
		if err != nil {
			return nil, fmt.Errorf("policy group %q: %w", id, err)
		}
		for target := range targets {
			if owner := targetOwners[target]; owner != "" && owner != id.String() {
				return nil, fmt.Errorf("%w: %q in %q and %q", ErrPolicyLoop, target, owner, id)
			}
			targetOwners[target] = id.String()
		}
		ids[id.String()] = struct{}{}
		positions[input.Position] = struct{}{}
		groups = append(groups, policyGroup{id: id, position: input.Position, rules: rules})
	}
	slices.SortFunc(groups, func(left, right policyGroup) int { return left.position - right.position })
	return groups, nil
}

func parsePolicyRules(inputs []PolicyRuleInput, objects map[string]policyIPObject, topologyGroups map[string]InterfaceName) ([]policyRule, map[string]struct{}, error) {
	rules := make([]policyRule, 0, len(inputs))
	ids := make(map[string]struct{}, len(inputs))
	sequences := make(map[int]struct{}, len(inputs))
	targets := make(map[string]struct{})
	for _, input := range inputs {
		id, err := parsePolicyName(input.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: ID %q: %v", ErrInvalidPolicyRule, input.ID, err)
		}
		if _, exists := ids[id.String()]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate ID %q", ErrInvalidPolicyRule, id)
		}
		if input.Sequence <= 0 {
			return nil, nil, fmt.Errorf("%w: %q", ErrInvalidRuleSequence, id)
		}
		if _, exists := sequences[input.Sequence]; exists {
			return nil, nil, fmt.Errorf("%w: %d", ErrDuplicateRuleSequence, input.Sequence)
		}
		match, err := parsePolicyMatch(input.Match, objects)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %q: %w", id, err)
		}
		action, err := parsePolicyAction(input.Action, topologyGroups)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %q: %w", id, err)
		}
		if action.kind == ActionVia {
			targets[action.group.String()] = struct{}{}
		}
		ids[id.String()] = struct{}{}
		sequences[input.Sequence] = struct{}{}
		rules = append(rules, policyRule{id: id, sequence: input.Sequence, match: match, action: action})
	}
	slices.SortFunc(rules, func(left, right policyRule) int { return left.sequence - right.sequence })
	return rules, targets, nil
}

func parsePolicyName(raw string) (policyName, error) {
	name, err := parseName(raw)
	if err != nil {
		return policyName{}, err
	}
	return policyName{value: name.String()}, nil
}
