package orchestratorapi

import "ly-route/backend/internal/orchestrator"

type testTopologyDTO struct {
	SchemaVersion       int                `json:"schema_version"`
	ManagementInterface string             `json:"management_interface"`
	Interfaces          []testInterfaceDTO `json:"interfaces"`
	Groups              []testGroupDTO     `json:"orchestration_groups"`
}

type testInterfaceDTO struct {
	Name string                 `json:"name"`
	Role orchestrator.Role      `json:"role"`
	Port string                 `json:"port,omitempty"`
	Bond *orchestrator.BondView `json:"bond,omitempty"`
}

type testGroupDTO struct {
	Name  string                          `json:"name"`
	Ports []orchestrator.DirectedPortView `json:"ports"`
}

type invalidTopologyCase struct {
	name    string
	mutate  func(testTopologyDTO) testTopologyDTO
	wantErr error
}

func invalidTopologyCases() []invalidTopologyCase {
	return []invalidTopologyCase{
		{
			name: "missing LAN",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Interfaces = input.Interfaces[1:]
				return input
			},
			wantErr: orchestrator.ErrMissingLAN,
		},
		{
			name: "duplicate LAN",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Interfaces = append(input.Interfaces, testInterfaceDTO{Name: "lan-backup", Role: orchestrator.RoleLAN, Port: "eth8"})
				return input
			},
			wantErr: orchestrator.ErrDuplicateLAN,
		},
		{
			name: "missing WAN",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Interfaces = input.Interfaces[:1]
				return input
			},
			wantErr: orchestrator.ErrMissingWAN,
		},
		{
			name: "duplicate WAN",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Interfaces = append(input.Interfaces, testInterfaceDTO{Name: "wan-backup", Role: orchestrator.RoleWAN, Port: "eth8"})
				return input
			},
			wantErr: orchestrator.ErrDuplicateWAN,
		},
		{
			name: "shared LAN WAN port",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Interfaces[1].Port = "eth1"
				return input
			},
			wantErr: orchestrator.ErrSharedInterface,
		},
		{
			name: "shared bond member",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Interfaces[1] = testInterfaceDTO{Name: "wan", Role: orchestrator.RoleWAN, Bond: &orchestrator.BondView{Name: "bond-wan", Members: []string{"eth2", "eth3"}}}
				return input
			},
			wantErr: orchestrator.ErrSharedInterface,
		},
		{
			name: "management assigned to LAN",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Interfaces[0] = testInterfaceDTO{Name: "lan", Role: orchestrator.RoleLAN, Port: "eth0"}
				return input
			},
			wantErr: orchestrator.ErrManagementMembership,
		},
		{
			name: "group bond",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports[0].Interface = "bond-lan"
				return input
			},
			wantErr: orchestrator.ErrGroupBond,
		},
		{
			name: "duplicate direction",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports[1].Direction = orchestrator.DirectionLANFacing
				return input
			},
			wantErr: orchestrator.ErrGroupDirection,
		},
		{
			name: "group size one",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports = input.Groups[0].Ports[:1]
				return input
			},
			wantErr: orchestrator.ErrGroupSize,
		},
		{
			name: "group size three",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports = append(input.Groups[0].Ports, orchestrator.DirectedPortView{Interface: "eth8", Direction: orchestrator.DirectionWANFacing})
				return input
			},
			wantErr: orchestrator.ErrGroupSize,
		},
		{
			name: "management in group",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports[0].Interface = "eth0"
				return input
			},
			wantErr: orchestrator.ErrManagementMembership,
		},
		{
			name: "logical LAN in group",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports[0].Interface = "lan"
				return input
			},
			wantErr: orchestrator.ErrLogicalMembership,
		},
		{
			name: "logical WAN in group",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports[0].Interface = "wan"
				return input
			},
			wantErr: orchestrator.ErrLogicalMembership,
		},
		{
			name: "logical physical port in group",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports[0].Interface = "eth3"
				return input
			},
			wantErr: orchestrator.ErrSharedInterface,
		},
		{
			name: "groups share port",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[1].Ports[0].Interface = "eth4"
				return input
			},
			wantErr: orchestrator.ErrSharedInterface,
		},
		{
			name: "group repeats port",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[0].Ports[1].Interface = input.Groups[0].Ports[0].Interface
				return input
			},
			wantErr: orchestrator.ErrSharedInterface,
		},
		{
			name: "duplicate group name",
			mutate: func(input testTopologyDTO) testTopologyDTO {
				input.Groups[1].Name = input.Groups[0].Name
				return input
			},
			wantErr: orchestrator.ErrDuplicateGroup,
		},
	}
}

func cloneTestTopology(input testTopologyDTO) testTopologyDTO {
	clone := input
	clone.Interfaces = append([]testInterfaceDTO(nil), input.Interfaces...)
	for index, item := range clone.Interfaces {
		if item.Bond != nil {
			bond := *item.Bond
			bond.Members = append([]string(nil), item.Bond.Members...)
			clone.Interfaces[index].Bond = &bond
		}
	}
	clone.Groups = make([]testGroupDTO, len(input.Groups))
	for index, group := range input.Groups {
		clone.Groups[index] = testGroupDTO{Name: group.Name, Ports: append([]orchestrator.DirectedPortView(nil), group.Ports...)}
	}
	return clone
}
