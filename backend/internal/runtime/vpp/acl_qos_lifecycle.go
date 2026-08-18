package vpp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

type ACLQoSPlan struct {
	TransactionID          string
	IngressVPPInterface    string
	ACLs                   []trafficpolicy.SecurityACL
	QoS                    []flow.VPPObjectGroup
	DeleteACLs             []string
	DeleteQoS              []string
	DeleteACLState         []trafficpolicy.SecurityACL
	DeleteQoSState         []flow.VPPObjectGroup
}

type ACLQoSLifecycleError struct {
	Operation      string
	Cause          error
	Rollback       error
	RollbackResult RollbackResult
}

func (err *ACLQoSLifecycleError) Error() string {
	if err.Rollback != nil {
		return fmt.Sprintf("ACL/QoS operation %s failed: %v; rollback %s: %v", err.Operation, err.Cause, err.RollbackResult, err.Rollback)
	}
	return fmt.Sprintf("ACL/QoS operation %s failed: %v; rollback %s", err.Operation, err.Cause, err.RollbackResult)
}

func (err *ACLQoSLifecycleError) Unwrap() error { return errors.Join(err.Cause, err.Rollback) }

type ACLQoSApplyResult struct {
	Receipt  Receipt
	Readback Snapshot
}

func (a Adapter) ApplyACLQoS(ctx context.Context, plan ACLQoSPlan, prior Snapshot, attempted ...ACLQoSPlan) (ACLQoSApplyResult, error) {
	plan.DeleteACLState = appendACLsByID(plan.DeleteACLState, plan.DeleteACLs, prior.ACLs)
	plan.DeleteQoSState = appendQoSByID(plan.DeleteQoSState, plan.DeleteQoS, prior.QoS)
	rollbackPlan := plan
	if len(attempted) > 0 {
		rollbackPlan = attempted[0]
		if rollbackPlan.IngressVPPInterface == "" {
			rollbackPlan.IngressVPPInterface = plan.IngressVPPInterface
		}
	}
	operations, err := BuildACLQoSOperations(plan)
	if err != nil {
		return ACLQoSApplyResult{}, err
	}
	if a.Client == nil {
		return ACLQoSApplyResult{}, fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return ACLQoSApplyResult{}, fmt.Errorf("%w: open ACL/QoS channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	if ingress, ok := channel.(interface{ setLANVPPInterface(string) }); ok {
		ingress.setLANVPPInterface(plan.IngressVPPInterface)
	}
	for _, operation := range operations {
		if _, err := doOperation(ctx, channel, operation); err != nil {
			return ACLQoSApplyResult{}, a.aclQoSFailure(ctx, channel, plan.TransactionID, operation.Name, err, prior, rollbackPlan)
		}
	}
	readback, err := a.Snapshot(ctx, aclQoSSnapshotRequestForPlan(plan))
	if err != nil {
		return ACLQoSApplyResult{}, a.aclQoSFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	if err := verifyACLQoSDeletes(readback, plan); err != nil {
		return ACLQoSApplyResult{}, a.aclQoSFailure(ctx, channel, plan.TransactionID, "readback", err, prior, rollbackPlan)
	}
	return ACLQoSApplyResult{Receipt: Receipt{RequestID: plan.TransactionID, Operations: operations}, Readback: readback}, nil
}

func BuildACLQoSOperations(plan ACLQoSPlan) ([]Operation, error) {
	transactionID := strings.TrimSpace(plan.TransactionID)
	if transactionID == "" {
		return nil, fmt.Errorf("%w: transaction ID is required", ErrSnapshotIncomplete)
	}
	operations := make([]Operation, 0, len(plan.ACLs)+len(plan.QoS)+len(plan.DeleteACLs)+len(plan.DeleteQoS))
	// Delete before create/update. A replacement keeps the same ACL/policer
	// identity; creating first would let the old delete remove the new object.
	for _, id := range plan.DeleteACLs {
		acl, found := aclByID(plan.DeleteACLState, id)
		if !found {
			acl, found = aclByID(plan.ACLs, id)
		}
		if !found {
			return nil, fmt.Errorf("%w: prior ACL state %q is required for deletion", ErrSnapshotIncomplete, id)
		}
		operations = append(operations, Operation{Name: "vpp.security-acl", RequestID: transactionID, Resource: id, Payload: acl, VPPCtlCommands: deleteACLCommands(acl)})
	}
	for _, id := range plan.DeleteQoS {
		group, found := qosGroupByKind(plan.DeleteQoSState, id)
		if !found {
			group, found = qosGroupByKind(plan.QoS, id)
		}
		if !found {
			operations = append(operations, Operation{Name: "vpp.qos", RequestID: transactionID, Resource: id, VPPCtlCommands: deleteQoSCommands(id)})
			continue
		}
		operations = append(operations, Operation{Name: "vpp.qos", RequestID: transactionID, Resource: id, Payload: group, VPPCtlCommands: flowGroupDeleteCommands(group)})
	}
	for _, acl := range plan.ACLs {
		operations = append(operations, Operation{Name: "vpp.security-acl", RequestID: transactionID, Resource: acl.ID, Payload: acl, VPPCtlCommands: securityACLCommands(acl)})
	}
	for _, group := range plan.QoS {
		operations = append(operations, Operation{Name: "vpp.qos", RequestID: transactionID, Resource: group.Kind, Payload: group, VPPCtlCommands: flowGroupCommands(group)})
	}
	return operations, nil
}

func (a Adapter) aclQoSFailure(ctx context.Context, channel Channel, transactionID, operation string, cause error, prior Snapshot, current ACLQoSPlan) error {
	rollbackErr := errors.Join(cleanupACLQoS(ctx, channel, transactionID, current), applyACLQoSSnapshot(ctx, channel, transactionID, prior))
	request := aclQoSSnapshotRequest(transactionID, prior)
	request.LANVPPInterface = current.IngressVPPInterface
	readback, err := a.Snapshot(ctx, request)
	if err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	} else if !reflect.DeepEqual(readback.ACLs, prior.ACLs) || !reflect.DeepEqual(readback.QoS, prior.QoS) {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback readback does not match prior snapshot"))
	}
	result := RollbackSucceeded
	if rollbackErr != nil {
		result = RollbackFailed
	}
	return &ACLQoSLifecycleError{Operation: operation, Cause: cause, Rollback: rollbackErr, RollbackResult: result}
}

func cleanupACLQoS(ctx context.Context, channel Channel, transactionID string, plan ACLQoSPlan) error {
	var cleanup []error
	for _, group := range plan.QoS {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.qos.rollback-delete", RequestID: transactionID, Resource: group.Kind, Payload: group, VPPCtlCommands: flowGroupDeleteCommands(group)}); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	for _, acl := range plan.ACLs {
		if _, err := doOperation(ctx, channel, Operation{Name: "vpp.security-acl.rollback-delete", RequestID: transactionID, Resource: acl.ID, Payload: acl, VPPCtlCommands: deleteACLCommands(acl)}); err != nil {
			cleanup = append(cleanup, err)
		}
	}
	return errors.Join(cleanup...)
}

func applyACLQoSSnapshot(ctx context.Context, channel Channel, transactionID string, snapshot Snapshot) error {
	var replay []error
	for _, acl := range snapshot.ACLs {
		operation := Operation{Name: "vpp.security-acl.rollback", RequestID: transactionID, Resource: acl.ID, Payload: acl, VPPCtlCommands: securityACLCommands(acl)}
		if _, err := doOperation(ctx, channel, operation); err != nil {
			replay = append(replay, err)
		}
	}
	for _, group := range snapshot.QoS {
		// Keep the typed object on rollback so dynamic ACL/QoS groups use the
		// lifecycle adapter instead of replaying an unindexed create command.
		operation := Operation{Name: "vpp.qos.rollback", RequestID: transactionID, Resource: group.Kind, Payload: group, VPPCtlCommands: flowGroupCommands(group)}
		if _, err := doOperation(ctx, channel, operation); err != nil {
			replay = append(replay, err)
		}
	}
	return errors.Join(replay...)
}

func aclQoSSnapshotRequest(transactionID string, snapshot Snapshot) SnapshotRequest {
	request := SnapshotRequest{TransactionID: transactionID, Capabilities: []SnapshotCapability{SnapshotCapabilityACLs, SnapshotCapabilityQoS}}
	for _, acl := range snapshot.ACLs {
		request.ACLs = append(request.ACLs, acl.ID)
	}
	request.Candidates.ACLs = append(request.Candidates.ACLs, snapshot.ACLs...)
	for _, group := range snapshot.QoS {
		request.QoS = append(request.QoS, group.Kind)
	}
	request.Candidates.QoS = append(request.Candidates.QoS, snapshot.QoS...)
	return request
}

func aclQoSSnapshotRequestForPlan(plan ACLQoSPlan) SnapshotRequest {
	request := SnapshotRequest{TransactionID: plan.TransactionID, LANVPPInterface: plan.IngressVPPInterface, Capabilities: []SnapshotCapability{SnapshotCapabilityACLs, SnapshotCapabilityQoS}}
	for _, acl := range plan.ACLs {
		request.ACLs = append(request.ACLs, acl.ID)
	}
	request.Candidates.ACLs = append(request.Candidates.ACLs, plan.ACLs...)
	request.AbsentACLs = append([]string(nil), plan.DeleteACLs...)
	request.Candidates.ACLs = appendACLsByID(request.Candidates.ACLs, plan.DeleteACLs, plan.DeleteACLState)
	for _, group := range plan.QoS {
		request.QoS = append(request.QoS, group.Kind)
	}
	request.Candidates.QoS = append(request.Candidates.QoS, plan.QoS...)
	request.AbsentQoS = append([]string(nil), plan.DeleteQoS...)
	request.Candidates.QoS = appendQoSByID(request.Candidates.QoS, plan.DeleteQoS, plan.DeleteQoSState)
	return request
}

func verifyACLQoSDeletes(snapshot Snapshot, plan ACLQoSPlan) error {
	for _, id := range plan.DeleteACLs {
		for _, acl := range snapshot.ACLs {
			if acl.ID == id {
				return fmt.Errorf("%w: deleted ACL %q is still present", ErrSnapshotIncomplete, id)
			}
		}
	}
	for _, id := range plan.DeleteQoS {
		for _, group := range snapshot.QoS {
			if group.Kind == id {
				return fmt.Errorf("%w: deleted QoS group %q is still present", ErrSnapshotIncomplete, id)
			}
		}
	}
	return nil
}

func deleteACLCommands(acl trafficpolicy.SecurityACL) []string {
	aclID := stableID("security-acl:"+acl.ID, 50000, 49999)
	commands := make([]string, 0, 3+len(securityDirections(acl.Match.Direction)))
	for _, direction := range securityDirections(acl.Match.Direction) {
		commands = append(commands, fmt.Sprintf("?set interface %s acl intfc lyroute-$LY_ROUTE_LAN_INTERFACE ip4-table %d del", direction, aclID))
	}
	commands = append(commands, fmt.Sprintf("?delete acl-plugin acl index %d", aclID), "show interface lyroute-$LY_ROUTE_LAN_INTERFACE", fmt.Sprintf("show acl-plugin acl index %d", aclID))
	return commands
}

func deleteQoSCommands(id string) []string {
	return []string{fmt.Sprintf("?qos delete %s", id), fmt.Sprintf("show qos %s", id)}
}

func aclByID(acls []trafficpolicy.SecurityACL, id string) (trafficpolicy.SecurityACL, bool) {
	for _, acl := range acls {
		if acl.ID == id {
			return acl, true
		}
	}
	return trafficpolicy.SecurityACL{}, false
}

func appendACLsByID(existing []trafficpolicy.SecurityACL, ids []string, prior []trafficpolicy.SecurityACL) []trafficpolicy.SecurityACL {
	result := append([]trafficpolicy.SecurityACL(nil), existing...)
	for _, id := range ids {
		if _, found := aclByID(result, id); found {
			continue
		}
		if acl, found := aclByID(prior, id); found {
			result = append(result, acl)
		}
	}
	return result
}

func appendQoSByID(existing []flow.VPPObjectGroup, ids []string, prior []flow.VPPObjectGroup) []flow.VPPObjectGroup {
	result := append([]flow.VPPObjectGroup(nil), existing...)
	seen := make(map[string]struct{}, len(result))
	for _, group := range result {
		seen[group.Kind] = struct{}{}
	}
	for _, id := range ids {
		if _, found := seen[id]; found {
			continue
		}
		for _, group := range prior {
			if group.Kind == id {
				result = append(result, group)
				seen[id] = struct{}{}
				break
			}
		}
	}
	return result
}
