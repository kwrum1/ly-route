package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/apply"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestGatewayProductionComposition_literalHTTPSuccessAndFailurePreserveGeneration(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-gateway-production-composition?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "eth0")
	saveGatewayApplyInterface(t, store)
	useVPPProof(t, "eth0")
	fixture := newProcessGatewayFixture(t)
	clock := fixedClock()
	transaction := fixture.transaction(clock)
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}}
	server := New(WithStore(store), WithClock(clock), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}), WithGatewayTransaction(transaction))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]

	// When
	success := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/apply", `{}`, cookie)

	// Then
	if success.Code != http.StatusOK || !strings.Contains(success.Body.String(), `"status":"committed"`) {
		t.Fatalf("success = %d %s", success.Code, success.Body.String())
	}
	previous := snapshotBytesForTest(t, ctx, store, "snapshot-"+extractTransactionID(t, success.Body.String()))
	fixture.setFailure("qos")
	if err := os.WriteFile(fixture.logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	failure := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/apply", `{}`, cookie)

	// Then
	if failure.Code != http.StatusAccepted || strings.Contains(failure.Body.String(), `"status":"committed"`) || strings.Contains(failure.Body.String(), `"runtime_state":"running"`) {
		t.Fatalf("failure = %d %s", failure.Code, failure.Body.String())
	}
	assertSnapshotBytesForTest(t, ctx, store, previous)
	trace, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatal(err)
	}
	wantTrace := "apply interfaces\napply bonds\napply wan-groups\napply routes\napply acls\napply qos\nrollback acls\nrollback routes\nrollback wan-groups\nrollback bonds\nrollback interfaces\n"
	if string(trace) != wantTrace {
		t.Fatalf("rollback process trace = %q, want %q", trace, wantTrace)
	}
	if strings.Count(string(trace), "apply wan-groups\n") != 1 {
		t.Fatalf("WAN-group apply count = %d, want one", strings.Count(string(trace), "apply wan-groups\n"))
	}
	if len(controller.rolledBackArtifacts[serviceRuntime.VPP]) != 0 {
		t.Fatalf("service rollback received VPP artifacts: %#v", controller.rolledBackArtifacts[serviceRuntime.VPP])
	}
}

func TestConfigApplySnapshotsBeforeSingleVPPMutation(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-config-single-vpp?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "eth0")
	saveGatewayApplyInterface(t, store)
	useVPPProof(t, "eth0")
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}}
	trace := []string{}
	transaction := &traceGatewayTransaction{trace: &trace}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}), WithGatewayTransaction(transaction), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/apply", `{}`, login.Result().Cookies()[0])

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("config apply = %d: %s", response.Code, response.Body.String())
	}
	if len(controller.appliedArtifacts[serviceRuntime.VPP]) != 0 || len(controller.receiptArtifacts) != 0 && hasServiceArtifact(controller.receiptArtifacts, serviceRuntime.VPP) || len(controller.readbackArtifacts) != 0 && hasServiceArtifact(controller.readbackArtifacts, serviceRuntime.VPP) {
		t.Fatalf("service VPP ownership leaked: applied=%#v receipt=%#v readback=%#v", controller.appliedArtifacts, controller.receiptArtifacts, controller.readbackArtifacts)
	}
	if len(trace) < 2 || trace[0] != "snapshot" || trace[1] != "mutate" {
		t.Fatalf("VPP trace = %#v, want snapshot before mutation", trace)
	}
	if len(trace) != 1+transaction.mutations || transaction.mutations == 0 {
		t.Fatalf("VPP mutation count = %d, want one per compiled operation", len(trace)-1)
	}
}

func TestConfigApplyWithoutGatewayPreservesVPPServiceRuntime(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-config-fallback-vpp?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "eth0")
	saveGatewayApplyInterface(t, store)
	useVPPProof(t, "eth0")
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/apply", `{}`, login.Result().Cookies()[0])

	// Then
	if response.Code != http.StatusOK || len(controller.appliedArtifacts[serviceRuntime.VPP]) != 1 || !hasServiceArtifact(controller.receiptArtifacts, serviceRuntime.VPP) || !hasServiceArtifact(controller.readbackArtifacts, serviceRuntime.VPP) {
		t.Fatalf("fallback response=%d service artifacts=%#v receipt=%#v readback=%#v", response.Code, controller.appliedArtifacts, controller.receiptArtifacts, controller.readbackArtifacts)
	}
}

func hasServiceArtifact(artifacts []serviceRuntime.RenderedArtifact, service serviceRuntime.ServiceName) bool {
	for _, artifact := range artifacts {
		if artifact.Service == service {
			return true
		}
	}
	return false
}

type traceGatewayTransaction struct {
	trace     *[]string
	mutations int
}

func (transaction *traceGatewayTransaction) Run(_ context.Context, plan apply.Plan) (apply.GatewayTransactionResult, error) {
	*transaction.trace = append(*transaction.trace, "snapshot")
	for range plan.GatewayPlan.Proxy.VPPSteering {
		*transaction.trace = append(*transaction.trace, "mutate")
		transaction.mutations++
	}
	if len(*transaction.trace) == 1 {
		*transaction.trace = append(*transaction.trace, "mutate")
		transaction.mutations++
	}
	now := fixedClock()()
	return apply.GatewayTransactionResult{Order: []string{"interfaces"}, Receipts: []apply.ApplyReceipt{{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Status: apply.ReceiptApplied, AppliedAt: now}}, Readbacks: []apply.Readback{{TransactionID: plan.Request.TransactionID, Capability: plan.Request.Resource, Timestamp: now, Fresh: true}}}, nil
}

func (traceGatewayTransaction) Rollback(context.Context, apply.Plan) error { return nil }

type processGatewayFixture struct {
	command string
	logPath string
	failAt  string
}

func newProcessGatewayFixture(t *testing.T) *processGatewayFixture {
	t.Helper()
	directory := t.TempDir()
	command := filepath.Join(directory, "gateway-fixture.sh")
	content := "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" >> \"$QA_GATEWAY_LOG\"\nif [ \"$1\" = apply ] && [ \"$2\" = \"$QA_GATEWAY_FAIL\" ]; then exit 17; fi\n"
	if err := os.WriteFile(command, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(directory, "trace.log")
	t.Setenv("QA_GATEWAY_LOG", logPath)
	return &processGatewayFixture{command: command, logPath: logPath}
}

func (fixture *processGatewayFixture) setFailure(name string) { fixture.failAt = name }

func (fixture *processGatewayFixture) transaction(clock apply.Clock) *apply.GatewayMultiResourceTransaction {
	adapters := make([]apply.GatewayResourceAdapter, 0, len(gatewayResourceNames))
	for _, name := range gatewayResourceNames {
		adapters = append(adapters, processGatewayAdapter{fixture: fixture, name: name, clock: clock})
	}
	return &apply.GatewayMultiResourceTransaction{Adapters: adapters, Now: clock}
}

type processGatewayAdapter struct {
	fixture *processGatewayFixture
	name    string
	clock   apply.Clock
}

func (adapter processGatewayAdapter) Name() string { return adapter.name }

func (adapter processGatewayAdapter) Apply(ctx context.Context, plan apply.Plan) (apply.GatewayResourceResult, error) {
	if err := adapter.fixture.run(ctx, "apply", adapter.name); err != nil {
		return apply.GatewayResourceResult{}, err
	}
	now := adapter.clock()
	return apply.GatewayResourceResult{Receipt: apply.ApplyReceipt{TransactionID: plan.Request.TransactionID, Capability: adapter.name, Status: apply.ReceiptApplied, AppliedAt: now}, Readback: apply.Readback{TransactionID: plan.Request.TransactionID, Capability: adapter.name, Timestamp: now, Fresh: true}}, nil
}

func (adapter processGatewayAdapter) Rollback(ctx context.Context, _ apply.Plan) error {
	return adapter.fixture.run(ctx, "rollback", adapter.name)
}

func (fixture *processGatewayFixture) run(ctx context.Context, action, resource string) error {
	command := exec.CommandContext(ctx, fixture.command, action, resource)
	command.Env = append(os.Environ(), "QA_GATEWAY_FAIL="+fixture.failAt)
	output, err := command.CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(output)) + ": " + err.Error())
	}
	return nil
}

var gatewayResourceNames = []string{"interfaces", "bonds", "wan-groups", "routes", "acls", "qos", "nat44", "port-maps"}

type testSnapshotBytes struct {
	payload []byte
	hash    string
}

func snapshotBytesForTest(t *testing.T, ctx context.Context, store *persistence.Store, id string) testSnapshotBytes {
	t.Helper()
	snapshot, err := store.RuntimeSnapshot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return testSnapshotBytes{payload: append([]byte(nil), snapshot.Payload...), hash: snapshot.PayloadHash}
}

func extractTransactionID(t *testing.T, body string) string {
	t.Helper()
	var result struct {
		TransactionID string `json:"transaction_id"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	return result.TransactionID
}

func assertSnapshotBytesForTest(t *testing.T, ctx context.Context, store *persistence.Store, previous testSnapshotBytes) {
	t.Helper()
	snapshots, err := store.RuntimeSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || !reflect.DeepEqual([]byte(snapshots[0].Payload), previous.payload) || snapshots[0].PayloadHash != previous.hash {
		t.Fatalf("runtime generations = %#v, want one byte/hash-identical prior generation", snapshots)
	}
}

func saveGatewayApplyInterface(t *testing.T, store *persistence.Store) {
	t.Helper()
	saveExplicitDataInterface(t, store, "eth1")
	document := configDocument(t, "interface", "eth1", map[string]any{"id": "eth1", "interface_id": "eth1", "gateway_role": "lan", "cidr": "192.0.2.2/24"}, time.Now().UTC())
	if err := store.SaveConfig(context.Background(), document); err != nil {
		t.Fatal(err)
	}
}
