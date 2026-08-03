package apply

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

func TestProductionGatewayDesiredLiveDiffMutatesChangedRemovedAndNewInDependencyOrder(t *testing.T) {
	// Given
	prior, desired, live := productionGatewayPlans()
	client := &desiredLiveClient{state: live}
	clock := deterministicClock(time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC))
	transaction := NewProductionGatewayTransaction(vpp.Adapter{Client: client}, clock)
	plan := productionReconciliationPlan("txn-desired-live", prior, desired)

	// When
	result, err := transaction.Run(context.Background(), plan)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	mutations := client.mutations()
	if len(mutations) != 32 {
		t.Fatalf("mutation count = %d, want 32", len(mutations))
	}
	wantOrder := []string{"port-maps", "port-maps", "nat44", "nat44", "qos", "qos", "acls", "acls", "routes", "routes", "wan-groups", "wan-groups", "bonds", "bonds", "interfaces", "interfaces", "interfaces", "interfaces", "bonds", "bonds", "wan-groups", "wan-groups", "routes", "routes", "acls", "acls", "qos", "qos", "nat44", "nat44", "port-maps", "port-maps"}
	if got := mutationClasses(mutations); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("mutation order = %#v, want %#v", got, wantOrder)
	}
	for _, operation := range mutations {
		if strings.Contains(operation.Resource, "unchanged") {
			t.Fatalf("unchanged object was mutated: %#v", operation)
		}
	}
	for _, name := range []string{"interfaces", "bonds", "wan-groups", "routes", "acls", "qos", "nat44", "port-maps"} {
		if !result.Deletions[name] {
			t.Fatalf("deletion evidence for %s = false, want true", name)
		}
	}
}

func TestProductionGatewayDeleteOnlyTransitionRemainsApplicable(t *testing.T) {
	// Given
	prior, _, live := productionGatewayPlans()
	desired := unchangedGatewayPlan(prior)
	client := &desiredLiveClient{state: live}
	clock := deterministicClock(time.Date(2026, 7, 25, 15, 0, 0, 0, time.UTC))
	transaction := NewProductionGatewayTransaction(vpp.Adapter{Client: client}, clock)

	// When
	result, err := transaction.Run(context.Background(), productionReconciliationPlan("txn-delete-only", prior, desired))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	mutations := client.mutations()
	if len(mutations) != 16 {
		t.Fatalf("delete-only mutation count = %d, want 16", len(mutations))
	}
	wantOrder := []string{"port-maps", "port-maps", "nat44", "nat44", "qos", "qos", "acls", "acls", "routes", "routes", "wan-groups", "wan-groups", "bonds", "bonds", "interfaces", "interfaces"}
	if got := mutationClasses(mutations); !reflect.DeepEqual(got, wantOrder) || len(result.Order) != 8 {
		t.Fatalf("delete-only order = %#v, result = %#v", got, result.Order)
	}
}

func TestProductionGatewayIncompleteLiveStateFailsBeforeMutation(t *testing.T) {
	// Given
	prior, desired, live := productionGatewayPlans()
	live.RoutePolicies = live.RoutePolicies[1:]
	client := &desiredLiveClient{state: live}
	transaction := NewProductionGatewayTransaction(vpp.Adapter{Client: client}, deterministicClock(time.Date(2026, 7, 25, 16, 0, 0, 0, time.UTC)))

	// When
	_, err := transaction.Run(context.Background(), productionReconciliationPlan("txn-incomplete-live", prior, desired))

	// Then
	if err == nil {
		t.Fatal("incomplete live state was accepted")
	}
	if mutations := client.mutations(); len(mutations) != 0 {
		t.Fatalf("mutations = %#v, want none before complete live readback", mutations)
	}
}

func TestProductionGatewayLegacyGenerationMissingTypedPriorFailsBeforeMutation(t *testing.T) {
	// Given
	_, desired, live := productionGatewayPlans()
	client := &desiredLiveClient{state: live}
	transaction := NewProductionGatewayTransaction(vpp.Adapter{Client: client}, deterministicClock(time.Date(2026, 7, 25, 17, 0, 0, 0, time.UTC)))
	plan := Plan{Request: Request{TransactionID: "txn-missing-prior", PreviousSnapshotID: "snapshot-legacy", Resource: "/api/v1/config/apply"}, Previous: PreviousState{Available: true}, GatewayPlan: desired}

	// When
	_, err := transaction.Run(context.Background(), plan)

	// Then
	if err == nil {
		t.Fatal("missing typed prior plan was accepted")
	}
	if len(client.operations) != 0 {
		t.Fatalf("operations = %#v, want none without typed prior plan", client.operations)
	}
}

func productionReconciliationPlan(transactionID string, prior, desired vpp.Plan) Plan {
	return Plan{
		Request:     Request{TransactionID: transactionID, Resource: "/api/v1/config/apply"},
		Previous:    PreviousState{Available: true, GatewayPlan: &prior},
		GatewayPlan: desired,
	}
}
