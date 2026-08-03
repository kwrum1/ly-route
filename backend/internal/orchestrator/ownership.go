package orchestrator

import "fmt"

func validateOwnership(topology Topology) error {
	owners := map[string]string{}
	if !topology.managementShared {
		owners[topology.management.String()] = "linux-management"
	}
	bonds := map[string]bool{}
	logical := map[string]bool{}
	for _, item := range []LogicalInterface{topology.lan, topology.wan} {
		logical[item.name.String()] = true
		if err := claimBaseInterface(owners, topology.management, item.name, string(item.role)); err != nil {
			return err
		}
		if item.hasBond {
			bonds[item.bond.name.String()] = true
			if err := claimBaseInterface(owners, topology.management, item.bond.name, string(item.role)+" bond"); err != nil {
				return err
			}
			for _, member := range item.bond.members {
				if err := claimBaseInterface(owners, topology.management, member, string(item.role)+" bond member"); err != nil {
					return err
				}
			}
			continue
		}
		if topology.managementShared && item.role == RoleLAN && item.port.String() == topology.management.String() {
			owners[item.port.String()] = "lan port with shared management"
			continue
		}
		if err := claimBaseInterface(owners, topology.management, item.port, string(item.role)+" port"); err != nil {
			return err
		}
	}

	groupNames := map[string]bool{}
	for _, group := range topology.groups {
		if groupNames[group.Name()] {
			return fmt.Errorf("%w: %q", ErrDuplicateGroup, group.Name())
		}
		groupNames[group.Name()] = true
		for _, port := range group.ports {
			name := port.interfaceName.String()
			switch {
			case name == topology.management.String():
				return fmt.Errorf("%w: %q", ErrManagementMembership, name)
			case !port.hasBond && bonds[name]:
				return fmt.Errorf("%w: %q is already owned by a logical interface", ErrGroupBond, name)
			case logical[name]:
				return fmt.Errorf("%w: %q", ErrLogicalMembership, name)
			}
			if port.hasBond {
				if bonds[name] {
					return fmt.Errorf("%w: %q belongs to an existing logical bond", ErrSharedInterface, name)
				}
				if err := claimBaseInterface(owners, topology.management, port.bond.name, "orchestration group "+group.Name()+" bond"); err != nil {
					return err
				}
				for _, member := range port.bond.members {
					if err := claimBaseInterface(owners, topology.management, member, "orchestration group "+group.Name()+" bond member"); err != nil {
						return err
					}
				}
				continue
			}
			if current := owners[name]; current != "" {
				return fmt.Errorf("%w: %q belongs to %s", ErrSharedInterface, name, current)
			}
			owners[name] = "orchestration group " + group.Name()
		}
	}
	return nil
}

func claimBaseInterface(owners map[string]string, management, name InterfaceName, owner string) error {
	if name.String() == management.String() {
		return fmt.Errorf("%w: %q", ErrManagementMembership, name)
	}
	if current := owners[name.String()]; current != "" {
		return fmt.Errorf("%w: %q belongs to %s", ErrSharedInterface, name, current)
	}
	owners[name.String()] = owner
	return nil
}
