package orchestrator

import (
	"fmt"
	"strings"
	"unicode"
)

const maxNameLength = 63

func ParseTopology(input TopologyInput) (Topology, error) {
	if input.SchemaVersion != SchemaVersion {
		return Topology{}, fmt.Errorf("%w: %d", ErrInvalidSchemaVersion, input.SchemaVersion)
	}
	management, err := parseName(input.ManagementInterface)
	if err != nil {
		return Topology{}, fmt.Errorf("management interface: %w", err)
	}
	interfaces, err := parseLogicalInterfaces(input.Interfaces)
	if err != nil {
		return Topology{}, err
	}
	groups := make([]Group, 0, len(input.Groups))
	for _, item := range input.Groups {
		group, parseErr := ParseGroup(item)
		if parseErr != nil {
			return Topology{}, parseErr
		}
		groups = append(groups, group)
	}

	topology := Topology{management: management, managementShared: input.ManagementShared, lan: interfaces[RoleLAN], wan: interfaces[RoleWAN], groups: groups}
	if err := validateOwnership(topology); err != nil {
		return Topology{}, err
	}
	return topology, nil
}

func ParseGroup(input GroupInput) (Group, error) {
	name, err := parseGroupName(input.Name)
	if err != nil {
		return Group{}, fmt.Errorf("orchestration group name: %w", err)
	}
	if len(input.Ports) != 2 {
		return Group{}, fmt.Errorf("%w: %q has %d", ErrGroupSize, name, len(input.Ports))
	}
	var ports [2]DirectedPort
	directions := map[Direction]bool{}
	interfaces := map[string]bool{}
	for index, item := range input.Ports {
		hasInterface := strings.TrimSpace(item.Interface) != ""
		hasBond := item.Bond != nil
		if hasInterface == hasBond {
			return Group{}, fmt.Errorf("%w: orchestration group %q port requires exactly one interface or bond", ErrInvalidInterface, name)
		}
		var interfaceName InterfaceName
		var bond Bond
		var parseErr error
		if hasBond {
			bond, parseErr = parseBond(*item.Bond)
			if parseErr != nil {
				return Group{}, fmt.Errorf("orchestration group %q port: %w", name, parseErr)
			}
			interfaceName = bond.name
		} else {
			interfaceName, parseErr = parseName(item.Interface)
			if parseErr != nil {
				return Group{}, fmt.Errorf("orchestration group %q port: %w", name, parseErr)
			}
		}
		if item.Direction != DirectionLANFacing && item.Direction != DirectionWANFacing {
			return Group{}, fmt.Errorf("%w: %q has %q", ErrGroupDirection, name, item.Direction)
		}
		if interfaces[interfaceName.String()] {
			return Group{}, fmt.Errorf("%w: %q", ErrSharedInterface, interfaceName)
		}
		interfaces[interfaceName.String()] = true
		directions[item.Direction] = true
		ports[index] = DirectedPort{interfaceName: interfaceName, direction: item.Direction, bond: bond, hasBond: hasBond}
	}
	if len(directions) != 2 {
		return Group{}, fmt.Errorf("%w: %q", ErrGroupDirection, name)
	}
	return Group{name: name, ports: ports}, nil
}

func parseLogicalInterfaces(inputs []InterfaceInput) (map[Role]LogicalInterface, error) {
	counts := map[Role]int{}
	for _, input := range inputs {
		counts[input.Role]++
	}
	if counts[RoleLAN] == 0 {
		return nil, ErrMissingLAN
	}
	if counts[RoleLAN] > 1 {
		return nil, ErrDuplicateLAN
	}
	if counts[RoleWAN] == 0 {
		return nil, ErrMissingWAN
	}
	if counts[RoleWAN] > 1 {
		return nil, ErrDuplicateWAN
	}
	if len(inputs) != 2 {
		return nil, fmt.Errorf("%w: unsupported logical interface role", ErrInvalidInterface)
	}

	interfaces := make(map[Role]LogicalInterface, 2)
	for _, input := range inputs {
		parsed, err := parseLogicalInterface(input)
		if err != nil {
			return nil, err
		}
		interfaces[parsed.role] = parsed
	}
	return interfaces, nil
}

func parseLogicalInterface(input InterfaceInput) (LogicalInterface, error) {
	name, err := parseName(input.Name)
	if err != nil {
		return LogicalInterface{}, fmt.Errorf("logical interface: %w", err)
	}
	hasPort := strings.TrimSpace(input.Port) != ""
	hasBond := input.Bond != nil
	if hasPort == hasBond {
		return LogicalInterface{}, fmt.Errorf("%w: %q requires exactly one port or bond", ErrInvalidInterface, name)
	}
	logical := LogicalInterface{name: name, role: input.Role}
	if hasPort {
		logical.port, err = parseName(input.Port)
		if err != nil {
			return LogicalInterface{}, fmt.Errorf("logical interface %q port: %w", name, err)
		}
		return logical, nil
	}
	logical.bond, err = parseBond(*input.Bond)
	if err != nil {
		return LogicalInterface{}, fmt.Errorf("logical interface %q: %w", name, err)
	}
	logical.hasBond = true
	return logical, nil
}

func parseBond(input BondInput) (Bond, error) {
	name, err := parseName(input.Name)
	if err != nil {
		return Bond{}, fmt.Errorf("%w: %v", ErrInvalidBond, err)
	}
	if len(input.Members) < 2 {
		return Bond{}, fmt.Errorf("%w: %q requires at least two members", ErrInvalidBond, name)
	}
	members := make([]InterfaceName, 0, len(input.Members))
	for _, raw := range input.Members {
		member, parseErr := parseName(raw)
		if parseErr != nil {
			return Bond{}, fmt.Errorf("%w: bond %q member: %v", ErrInvalidBond, name, parseErr)
		}
		members = append(members, member)
	}
	return Bond{name: name, members: members}, nil
}

func parseName(raw string) (InterfaceName, error) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > maxNameLength {
		return InterfaceName{}, ErrInvalidName
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._:-", character)) {
			return InterfaceName{}, fmt.Errorf("%w: %q", ErrInvalidName, value)
		}
	}
	return InterfaceName{value: value}, nil
}

func parseGroupName(raw string) (InterfaceName, error) {
	value := strings.TrimSpace(raw)
	if value == "" || len([]rune(value)) > maxNameLength {
		return InterfaceName{}, ErrInvalidName
	}
	for _, character := range value {
		if !(unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("._:-", character)) {
			return InterfaceName{}, fmt.Errorf("%w: %q", ErrInvalidName, value)
		}
	}
	return InterfaceName{value: value}, nil
}
