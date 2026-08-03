package orchestrator

import (
	"errors"
	"testing"
)

func TestParseTopology_rejects_invalid_interface_ownership(t *testing.T) {
	valid := validTopologyInput()
	tests := []struct {
		name    string
		mutate  func(TopologyInput) TopologyInput
		wantErr error
	}{
		{
			name: "missing LAN",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces = input.Interfaces[1:]
				return input
			},
			wantErr: ErrMissingLAN,
		},
		{
			name: "duplicate LAN",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces = append(input.Interfaces, InterfaceInput{Name: "lan-backup", Role: RoleLAN, Port: "eth8"})
				return input
			},
			wantErr: ErrDuplicateLAN,
		},
		{
			name: "missing WAN",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces = input.Interfaces[:1]
				return input
			},
			wantErr: ErrMissingWAN,
		},
		{
			name: "duplicate WAN",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces = append(input.Interfaces, InterfaceInput{Name: "wan-backup", Role: RoleWAN, Port: "eth8"})
				return input
			},
			wantErr: ErrDuplicateWAN,
		},
		{
			name: "shared logical port",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces[1].Port = "eth1"
				return input
			},
			wantErr: ErrSharedInterface,
		},
		{
			name: "shared bond member",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces[1] = InterfaceInput{Name: "wan", Role: RoleWAN, Bond: &BondInput{Name: "bond-wan", Members: []string{"eth2", "eth3"}}}
				return input
			},
			wantErr: ErrSharedInterface,
		},
		{
			name: "management interface assigned to LAN",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces[0] = InterfaceInput{Name: "lan", Role: RoleLAN, Port: "eth0"}
				return input
			},
			wantErr: ErrManagementMembership,
		},
		{
			name: "group reuses an existing bond",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports[0].Interface = "bond-lan"
				return input
			},
			wantErr: ErrGroupBond,
		},
		{
			name: "group has duplicate direction",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports[1].Direction = DirectionLANFacing
				return input
			},
			wantErr: ErrGroupDirection,
		},
		{
			name: "group has one port",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports = input.Groups[0].Ports[:1]
				return input
			},
			wantErr: ErrGroupSize,
		},
		{
			name: "group has three ports",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports = append(input.Groups[0].Ports, DirectedPortInput{Interface: "eth8", Direction: DirectionWANFacing})
				return input
			},
			wantErr: ErrGroupSize,
		},
		{
			name: "group contains management interface",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports[0].Interface = "eth0"
				return input
			},
			wantErr: ErrManagementMembership,
		},
		{
			name: "group contains logical LAN",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports[0].Interface = "lan"
				return input
			},
			wantErr: ErrLogicalMembership,
		},
		{
			name: "group contains logical WAN",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports[0].Interface = "wan"
				return input
			},
			wantErr: ErrLogicalMembership,
		},
		{
			name: "group reuses logical interface physical port",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports[0].Interface = "eth3"
				return input
			},
			wantErr: ErrSharedInterface,
		},
		{
			name: "groups share a physical port",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups = append(input.Groups, GroupInput{Name: "inline-west", Ports: []DirectedPortInput{{Interface: "eth4", Direction: DirectionLANFacing}, {Interface: "eth8", Direction: DirectionWANFacing}}})
				return input
			},
			wantErr: ErrSharedInterface,
		},
		{
			name: "group repeats one physical port",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups[0].Ports[1].Interface = input.Groups[0].Ports[0].Interface
				return input
			},
			wantErr: ErrSharedInterface,
		},
		{
			name: "group name is duplicated",
			mutate: func(input TopologyInput) TopologyInput {
				input.Groups = append(input.Groups, GroupInput{Name: "inline-east", Ports: []DirectedPortInput{{Interface: "eth8", Direction: DirectionLANFacing}, {Interface: "eth9", Direction: DirectionWANFacing}}})
				return input
			},
			wantErr: ErrDuplicateGroup,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			input := cloneTopologyInput(valid)

			// When
			_, err := ParseTopology(test.mutate(input))

			// Then
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ParseTopology error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestParseTopology_acceptsIndependentBondEndpoints(t *testing.T) {
	input := validTopologyInput()
	input.Groups[0].Ports[0] = DirectedPortInput{
		Direction: DirectionLANFacing,
		Bond:      &BondInput{Name: "bond-inline-lan", Members: []string{"eth8", "eth9"}},
	}
	input.Groups[0].Ports[1] = DirectedPortInput{Interface: "eth10", Direction: DirectionWANFacing}

	topology, err := ParseTopology(input)
	if err != nil {
		t.Fatal(err)
	}
	group := topology.groups[0]
	if !group.ports[0].hasBond || group.ports[0].interfaceName.String() != "bond-inline-lan" {
		t.Fatalf("group bond endpoint = %#v", group.ports[0])
	}
	view := group.View()
	if view.Ports[0].Bond == nil || len(view.Ports[0].Bond.Members) != 2 {
		t.Fatalf("group bond view = %#v", view.Ports[0])
	}
}

func TestParseTopology_allowsManagementInterfaceOnlyAsSharedLANPort(t *testing.T) {
	input := validTopologyInput()
	input.ManagementShared = true
	input.Interfaces[0] = InterfaceInput{Name: "lan", Role: RoleLAN, Port: "eth0"}

	topology, err := ParseTopology(input)
	if err != nil {
		t.Fatal(err)
	}
	if !topology.View().ManagementShared || topology.View().Interfaces[0].Port != "eth0" {
		t.Fatalf("shared topology view = %#v", topology.View())
	}

	input.Interfaces[0] = InterfaceInput{Name: "lan", Role: RoleLAN, Port: "eth1"}
	input.Interfaces[1] = InterfaceInput{Name: "wan", Role: RoleWAN, Port: "eth0"}
	if _, err := ParseTopology(input); !errors.Is(err, ErrManagementMembership) {
		t.Fatalf("shared management WAN error = %v, want %v", err, ErrManagementMembership)
	}
}

func validTopologyInput() TopologyInput {
	return TopologyInput{
		SchemaVersion:       SchemaVersion,
		ManagementInterface: "eth0",
		Interfaces: []InterfaceInput{
			{Name: "lan", Role: RoleLAN, Bond: &BondInput{Name: "bond-lan", Members: []string{"eth1", "eth2"}}},
			{Name: "wan", Role: RoleWAN, Port: "eth3"},
		},
		Groups: []GroupInput{{
			Name: "inline-east",
			Ports: []DirectedPortInput{
				{Interface: "eth4", Direction: DirectionLANFacing},
				{Interface: "eth5", Direction: DirectionWANFacing},
			},
		}},
	}
}

func cloneTopologyInput(input TopologyInput) TopologyInput {
	clone := TopologyInput{SchemaVersion: input.SchemaVersion, ManagementInterface: input.ManagementInterface, ManagementShared: input.ManagementShared}
	clone.Interfaces = make([]InterfaceInput, len(input.Interfaces))
	for index, item := range input.Interfaces {
		clone.Interfaces[index] = item
		if item.Bond != nil {
			bond := *item.Bond
			bond.Members = append([]string(nil), item.Bond.Members...)
			clone.Interfaces[index].Bond = &bond
		}
	}
	clone.Groups = make([]GroupInput, len(input.Groups))
	for index, group := range input.Groups {
		clone.Groups[index] = GroupInput{Name: group.Name, Ports: append([]DirectedPortInput(nil), group.Ports...)}
	}
	return clone
}
