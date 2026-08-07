package vpp

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (a Adapter) snapshot(ctx context.Context, request SnapshotRequest) (Snapshot, error) {
	if a.Client == nil {
		return Snapshot{}, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: open snapshot channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	return a.snapshotOnChannel(ctx, channel, request)
}

func (a Adapter) snapshotOnChannel(ctx context.Context, channel Channel, request SnapshotRequest) (Snapshot, error) {
	transactionID := strings.TrimSpace(request.TransactionID)
	if transactionID == "" {
		return Snapshot{}, fmt.Errorf("%w: transaction ID is required", ErrSnapshotIncomplete)
	}
	capabilities := request.Capabilities
	if len(capabilities) == 0 {
		capabilities = []SnapshotCapability{SnapshotCapabilityInterfaces, SnapshotCapabilityBonds}
	}
	snapshot := Snapshot{RequestID: transactionID, TransactionID: transactionID, ReadbackAt: request.ReadbackAt}
	if snapshot.ReadbackAt.IsZero() {
		snapshot.ReadbackAt = time.Now().UTC()
	}
	var err error
	for _, capability := range capabilities {
		switch capability {
		case SnapshotCapabilityInterfaces:
			reply, readErr := channel.Do(ctx, snapshotOperation(request, capability))
			if readErr != nil {
				return Snapshot{}, fmt.Errorf("%w: interface readback command: %w", ErrSnapshotIncomplete, readErr)
			}
			if retvalErr := NormalizeRetval("vpp.interface.snapshot", transactionID, reply.Retval); retvalErr != nil {
				return Snapshot{}, retvalErr
			}
			interfaces, parseErr := parseInterfaceReadback(reply.Payload)
			if parseErr != nil {
				return Snapshot{}, parseErr
			}
			snapshot.Interfaces, err = selectInterfaces(interfaces, request)
		case SnapshotCapabilityBonds:
			reply, readErr := channel.Do(ctx, snapshotOperation(request, capability))
			if readErr != nil {
				return Snapshot{}, fmt.Errorf("%w: bond readback command: %w", ErrSnapshotIncomplete, readErr)
			}
			if retvalErr := NormalizeRetval("vpp.interface-bond.snapshot", transactionID, reply.Retval); retvalErr != nil {
				return Snapshot{}, retvalErr
			}
			bonds, parseErr := parseBondReadback(reply.Payload)
			if parseErr != nil {
				return Snapshot{}, parseErr
			}
			snapshot.Bonds, err = selectBonds(bonds, request)
		case SnapshotCapabilityRoutePolicies:
			reply, readErr := channel.Do(ctx, snapshotOperation(request, capability))
			if readErr != nil {
				return Snapshot{}, fmt.Errorf("%w: route policy readback command: %w", ErrSnapshotIncomplete, readErr)
			}
			if retvalErr := NormalizeRetval("vpp.route-policy.snapshot", transactionID, reply.Retval); retvalErr != nil {
				return Snapshot{}, retvalErr
			}
			policies, parseErr := parseRoutePolicyReadback(reply.Payload)
			if parseErr != nil {
				return Snapshot{}, parseErr
			}
			snapshot.RoutePolicies, err = selectRoutePolicies(policies, request)
		case SnapshotCapabilityWANGroups:
			reply, readErr := channel.Do(ctx, snapshotOperation(request, capability))
			if readErr != nil {
				return Snapshot{}, fmt.Errorf("%w: WAN group readback command: %w", ErrSnapshotIncomplete, readErr)
			}
			if retvalErr := NormalizeRetval("vpp.pbr.next-hop-group.snapshot", transactionID, reply.Retval); retvalErr != nil {
				return Snapshot{}, retvalErr
			}
			groups, parseErr := parseWANGroupReadback(reply.Payload)
			if parseErr != nil {
				return Snapshot{}, parseErr
			}
			snapshot.WANGroups, err = selectWANGroups(groups, request)
		case SnapshotCapabilityACLs:
			reply, readErr := channel.Do(ctx, snapshotOperation(request, capability))
			if readErr != nil {
				return Snapshot{}, fmt.Errorf("%w: ACL readback command: %w", ErrSnapshotIncomplete, readErr)
			}
			if retvalErr := NormalizeRetval("vpp.security-acl.snapshot", transactionID, reply.Retval); retvalErr != nil {
				return Snapshot{}, retvalErr
			}
			acls, parseErr := parseACLReadback(reply.Payload)
			if parseErr != nil {
				return Snapshot{}, parseErr
			}
			snapshot.ACLs, err = selectACLs(acls, request)
		case SnapshotCapabilityQoS:
			reply, readErr := channel.Do(ctx, snapshotOperation(request, capability))
			if readErr != nil {
				return Snapshot{}, fmt.Errorf("%w: QoS readback command: %w", ErrSnapshotIncomplete, readErr)
			}
			if retvalErr := NormalizeRetval("vpp.qos.snapshot", transactionID, reply.Retval); retvalErr != nil {
				return Snapshot{}, retvalErr
			}
			qos, parseErr := parseQoSReadback(reply.Payload)
			if parseErr != nil {
				return Snapshot{}, parseErr
			}
			snapshot.QoS, err = selectQoS(qos, request)
		case SnapshotCapabilityNAT44:
			reply, readErr := channel.Do(ctx, snapshotOperation(request, capability))
			if readErr != nil {
				return Snapshot{}, fmt.Errorf("%w: NAT44 readback command: %w", ErrSnapshotIncomplete, readErr)
			}
			if retvalErr := NormalizeRetval("vpp.nat44-ed.snapshot", transactionID, reply.Retval); retvalErr != nil {
				return Snapshot{}, retvalErr
			}
			natState, parseErr := parseNAT44Readback(reply.Payload)
			if parseErr != nil {
				return Snapshot{}, parseErr
			}
			snapshot.NAT.StaticMappings, snapshot.NAT.PortMappings, err = selectNAT44(natState, request)
		default:
			return Snapshot{}, fmt.Errorf("%w: unsupported capability %q", ErrSnapshotIncomplete, capability)
		}
		if err != nil {
			return Snapshot{}, err
		}
	}
	hash, err := snapshotHash(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Hash = hash
	return snapshot, nil
}

func snapshotOperation(request SnapshotRequest, capability SnapshotCapability) Operation {
	transactionID := strings.TrimSpace(request.TransactionID)
	switch capability {
	case SnapshotCapabilityInterfaces:
		return Operation{Name: "vpp.interface.snapshot", RequestID: transactionID, Resource: string(capability), Payload: request, VPPCtlCommands: []string{"show interface address"}}
	case SnapshotCapabilityBonds:
		return Operation{Name: "vpp.interface-bond.snapshot", RequestID: transactionID, Resource: string(capability), Payload: request, VPPCtlCommands: []string{"show bond details"}}
	case SnapshotCapabilityRoutePolicies:
		return Operation{Name: "vpp.route-policy.snapshot", RequestID: transactionID, Resource: string(capability), Payload: request, VPPCtlCommands: routeSnapshotCommands(request)}
	case SnapshotCapabilityWANGroups:
		return Operation{Name: "vpp.pbr.next-hop-group.snapshot", RequestID: transactionID, Resource: string(capability), Payload: request, VPPCtlCommands: wanGroupSnapshotCommands(request)}
	case SnapshotCapabilityACLs:
		return Operation{Name: "vpp.security-acl.snapshot", RequestID: transactionID, Resource: string(capability), Payload: request, VPPCtlCommands: aclSnapshotCommands(request)}
	case SnapshotCapabilityQoS:
		return Operation{Name: "vpp.qos.snapshot", RequestID: transactionID, Resource: string(capability), Payload: request, VPPCtlCommands: qosSnapshotCommands(request)}
	case SnapshotCapabilityNAT44:
		return Operation{Name: "vpp.nat44-ed.snapshot", RequestID: transactionID, Resource: string(capability), Payload: request, VPPCtlCommands: nat44SnapshotCommands(request)}
	default:
		return Operation{Name: "vpp.snapshot", RequestID: transactionID, Resource: string(capability)}
	}
}
