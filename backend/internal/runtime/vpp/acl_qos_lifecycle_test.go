package vpp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestGatewayACLQoSApplyReadsBackLiveState(t *testing.T) {
	// Given
	acl := trafficpolicy.SecurityACL{ID: "acl-guest", Action: "deny"}
	qos := flow.VPPObjectGroup{Kind: "vpp.qos.classify", Objects: []flow.VPPObject{{RuleID: "voice", Action: flow.ActionClassify, Class: "voice"}}}
	client := &aclQoSClient{replies: map[string]Reply{
		"vpp.security-acl.snapshot": {Payload: ACLReadback{ACLs: []trafficpolicy.SecurityACL{acl}}},
		"vpp.qos.snapshot":          {Payload: QoSReadback{Groups: []flow.VPPObjectGroup{qos}}},
	}}

	// When
	result, err := (Adapter{Client: client}).ApplyACLQoS(context.Background(), ACLQoSPlan{
		TransactionID: "txn-acl-qos-apply",
		ACLs:          []trafficpolicy.SecurityACL{acl},
		QoS:           []flow.VPPObjectGroup{qos},
	}, Snapshot{})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.RequestID != "txn-acl-qos-apply" || len(result.Receipt.Operations) != 2 {
		t.Fatalf("result = %#v, want transaction-bound ACL/QoS receipt", result)
	}
	if len(result.Readback.ACLs) != 1 || len(result.Readback.QoS) != 1 {
		t.Fatalf("readback = %#v, want typed live ACL/QoS state", result.Readback)
	}
	for _, operation := range client.operations {
		if operation.RequestID != "txn-acl-qos-apply" {
			t.Fatalf("operation = %#v, want transaction-bound request", operation)
		}
	}
}

func TestGatewayACLQoSDeleteConfirmsAbsence(t *testing.T) {
	// Given
	client := &aclQoSClient{replies: map[string]Reply{
		"vpp.security-acl.snapshot": {Payload: ACLReadback{ACLs: nil}},
		"vpp.qos.snapshot":          {Payload: QoSReadback{Groups: nil}},
	}}

	// When
	result, err := (Adapter{Client: client}).ApplyACLQoS(context.Background(), ACLQoSPlan{
		TransactionID: "txn-acl-qos-delete",
		DeleteACLs:    []string{"acl-guest"},
		DeleteQoS:     []string{"voice"},
	}, Snapshot{ACLs: []trafficpolicy.SecurityACL{{ID: "acl-guest"}}, QoS: []flow.VPPObjectGroup{{Kind: "vpp.qos.classify", Objects: []flow.VPPObject{{RuleID: "voice"}}}}})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Readback.ACLs) != 0 || len(result.Readback.QoS) != 0 {
		t.Fatalf("readback = %#v, want deleted state absent", result.Readback)
	}
	if !strings.Contains(strings.Join(client.operations[0].VPPCtlCommands, "\n"), "del") || !strings.Contains(strings.Join(client.operations[1].VPPCtlCommands, "\n"), "del") {
		t.Fatalf("operations = %#v, want delete commands", client.operations)
	}
}

func TestBuildACLQoSOperationsDeletesReplacementBeforeCreate(t *testing.T) {
	prior := flow.VPPObjectGroup{Kind: "vpp.behavior.rate", Objects: []flow.VPPObject{{RuleID: "flow-30", Action: flow.ActionPolicer}}}
	desired := flow.VPPObjectGroup{Kind: "vpp.behavior.rate", Objects: []flow.VPPObject{{RuleID: "flow-30", Action: flow.ActionPolicer, Policer: &flow.Policer{RateBPS: 1_000_000}}}}
	operations, err := BuildACLQoSOperations(ACLQoSPlan{
		TransactionID: "txn-qos-replace",
		QoS:           []flow.VPPObjectGroup{desired},
		DeleteQoS:     []string{prior.Kind},
		DeleteQoSState: []flow.VPPObjectGroup{prior},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 {
		t.Fatalf("operations = %#v, want delete and create", operations)
	}
	if !strings.Contains(strings.Join(operations[0].VPPCtlCommands, "\n"), "policer del") {
		t.Fatalf("first operation = %#v, want replacement delete", operations[0])
	}
	if !strings.Contains(strings.Join(operations[1].VPPCtlCommands, "\n"), "policer add") {
		t.Fatalf("second operation = %#v, want replacement create", operations[1])
	}
}

func TestGatewayACLQoSApplyFailureRollsBackPriorSnapshot(t *testing.T) {
	// Given
	priorACL := trafficpolicy.SecurityACL{ID: "acl-prior", Action: "permit"}
	priorQoS := flow.VPPObjectGroup{Kind: "vpp.qos.store", Objects: []flow.VPPObject{{RuleID: "prior"}}}
	applyErr := errors.New("qos command failed")
	client := &aclQoSClient{
		errors: map[string]error{"vpp.qos": applyErr},
		replies: map[string]Reply{
			"vpp.security-acl.snapshot": {Payload: ACLReadback{ACLs: []trafficpolicy.SecurityACL{priorACL}}},
			"vpp.qos.snapshot":          {Payload: QoSReadback{Groups: []flow.VPPObjectGroup{priorQoS}}},
		},
	}

	// When
	_, err := (Adapter{Client: client}).ApplyACLQoS(context.Background(), ACLQoSPlan{
		TransactionID: "txn-acl-qos-failure",
		ACLs:          []trafficpolicy.SecurityACL{{ID: "acl-new", Action: "deny"}},
		QoS:           []flow.VPPObjectGroup{{Kind: "vpp.qos.store", Objects: []flow.VPPObject{{RuleID: "new"}}}},
	}, Snapshot{ACLs: []trafficpolicy.SecurityACL{priorACL}, QoS: []flow.VPPObjectGroup{priorQoS}})

	// Then
	var lifecycleErr *ACLQoSLifecycleError
	if !errors.As(err, &lifecycleErr) || !errors.Is(err, applyErr) || lifecycleErr.RollbackResult != RollbackSucceeded {
		t.Fatalf("error = %T %v, want apply error with successful rollback", err, err)
	}
	if client.rollbackOperations != 2 {
		t.Fatalf("rollback operations = %d, want prior ACL and QoS snapshot replay", client.rollbackOperations)
	}
	foundTypedQoSRollback := false
	for _, operation := range client.operations {
		if operation.Name != "vpp.qos.rollback" {
			continue
		}
		if _, ok := operation.Payload.(flow.VPPObjectGroup); !ok {
			t.Fatalf("rollback QoS operation = %#v, want typed QoS payload", operation)
		}
		foundTypedQoSRollback = true
	}
	if !foundTypedQoSRollback {
		t.Fatalf("operations = %#v, want typed QoS rollback", client.operations)
	}
}

func TestGatewayACLQoSReportsExactRollbackCommandFailure(t *testing.T) {
	// Given
	rollbackErr := errors.New("rollback qos command failed exactly")
	client := &aclQoSClient{errors: map[string]error{
		"vpp.security-acl":          errors.New("acl command failed"),
		"vpp.security-acl.rollback": rollbackErr,
	}}

	// When
	_, err := (Adapter{Client: client}).ApplyACLQoS(context.Background(), ACLQoSPlan{
		TransactionID: "txn-acl-qos-rollback-failure",
		ACLs:          []trafficpolicy.SecurityACL{{ID: "acl-new", Action: "deny"}},
	}, Snapshot{ACLs: []trafficpolicy.SecurityACL{{ID: "acl-prior", Action: "permit"}}})

	// Then
	var lifecycleErr *ACLQoSLifecycleError
	if !errors.As(err, &lifecycleErr) || lifecycleErr.RollbackResult != RollbackFailed || !errors.Is(lifecycleErr.Rollback, rollbackErr) || !strings.Contains(lifecycleErr.Rollback.Error(), rollbackErr.Error()) {
		t.Fatalf("error = %T %v, want exact rollback error", err, err)
	}
}

func TestGatewayACLQoSRejectsIncompleteReadback(t *testing.T) {
	// Given
	client := &aclQoSClient{replies: map[string]Reply{
		"vpp.security-acl.snapshot": {Payload: ACLReadback{}},
	}}

	// When
	_, err := (Adapter{Client: client}).ApplyACLQoS(context.Background(), ACLQoSPlan{
		TransactionID: "txn-acl-qos-incomplete",
		ACLs:          []trafficpolicy.SecurityACL{{ID: "acl-guest", Action: "deny"}},
	}, Snapshot{})

	// Then
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("error = %v, want incomplete readback failure", err)
	}
}

type aclQoSClient struct {
	replies            map[string]Reply
	errors             map[string]error
	operations         []Operation
	rollbackOperations int
}

func (client *aclQoSClient) OpenChannel(context.Context) (Channel, error) {
	return &aclQoSChannel{client: client}, nil
}

type aclQoSChannel struct{ client *aclQoSClient }

func (channel *aclQoSChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	channel.client.operations = append(channel.client.operations, operation)
	if strings.HasSuffix(operation.Name, ".rollback") {
		channel.client.rollbackOperations++
	}
	if err := channel.client.errors[operation.Name]; err != nil {
		return Reply{}, err
	}
	return channel.client.replies[operation.Name], nil
}

func (channel *aclQoSChannel) Close() error { return nil }
