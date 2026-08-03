package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/apply"
	serviceRuntime "ly-route/backend/internal/runtime/service"
	"ly-route/backend/internal/runtime/vpp"
)

func TestGatewayLifecycleCharacterization_bond_preview_has_no_apply_commands(t *testing.T) {
	// Given
	server, cookie := gatewayLifecycleBondServer(t)
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interface-bonds", `{"id":"bond0","members":["eth1","eth2"]}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create bond status = %d: %s", created.Code, created.Body.String())
	}

	// When
	preview := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/runtime/preview", "", cookie)

	// Then
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", preview.Code, preview.Body.String())
	}
	var body struct {
		Plan RuntimePlan `json:"plan"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var operation *vpp.Operation
	for index := range body.Plan.VppOperations {
		if body.Plan.VppOperations[index].Name == "vpp.interface-bond" {
			operation = &body.Plan.VppOperations[index]
			break
		}
	}
	if operation == nil {
		t.Fatalf("VPP operations = %#v, want bond operation", body.Plan.VppOperations)
	}
	if operation.Resource != "bond0" || len(operation.VPPCtlCommands) != 0 {
		t.Fatalf("bond operation = %#v, want commandless vpp.interface-bond/bond0", operation)
	}
}

func TestGatewayLifecycleCharacterization_bond_delete_reports_desired_only(t *testing.T) {
	// Given
	server, cookie := gatewayLifecycleBondServer(t)
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interface-bonds", `{"id":"bond0","members":["eth1","eth2"]}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create bond status = %d: %s", created.Code, created.Body.String())
	}

	// When
	deleted := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/interface-bonds/bond0", "", cookie)

	// Then
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete bond status = %d: %s", deleted.Code, deleted.Body.String())
	}
	var body struct {
		Deleted      bool   `json:"deleted"`
		ID           string `json:"id"`
		RuntimeState string `json:"runtime_state"`
	}
	if err := json.Unmarshal(deleted.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Deleted || body.ID != "bond0" || body.RuntimeState != "desired_not_applied" {
		t.Fatalf("delete response = %#v, want desired-only deletion", body)
	}
}

func TestGatewayLifecycleCharacterization_receipt_readback_maps_only_policy_nat_and_wan(t *testing.T) {
	// Given
	receiptPath := filepath.Join(t.TempDir(), "vpp-apply-receipt.json")
	receipt := `{"status":"applied","dry_run":false,"operations":[{"name":"vpp.route-policy","resource":"route-office","results":[{"command":"show ip fib table 1","status":"applied"}]},{"name":"vpp.security-acl","resource":"acl-guest","results":[{"command":"show acl-plugin acl index 2","status":"applied"}]},{"name":"vpp.nat44-ed.static-mapping","resource":"port-web","results":[{"command":"show nat44 static mappings","status":"applied"}]},{"name":"vpp.pbr.next-hop-group","resource":"wan-primary","results":[{"command":"show ip fib table 3","status":"applied"}]},{"name":"vpp.qos.classify","resource":"classify-voice","results":[{"command":"show qos record eth1","status":"applied"}]}]}`
	if err := os.WriteFile(receiptPath, []byte(receipt), 0o600); err != nil {
		t.Fatal(err)
	}
	server := New(WithVPPReceiptPath(receiptPath), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]

	// When
	response := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/policy-hits", "", cookie)

	// Then
	if response.Code != http.StatusOK {
		t.Fatalf("policy readback status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			Operation     string `json:"operation"`
			ReadbackState string `json:"readback_state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(body.Data))
	for _, item := range body.Data {
		got = append(got, item.Operation+":"+item.ID+":"+item.ReadbackState)
	}
	want := []string{
		"vpp.route-policy:route-office:applied",
		"vpp.security-acl:acl-guest:applied",
		"vpp.nat44-ed.static-mapping:port-web:applied",
		"vpp.pbr.next-hop-group:wan-primary:applied",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt readback items = %#v, want %#v", got, want)
	}
}

func TestGatewayConfigApplyTransactionFailureNeverReportsCommittedOrRunning(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-gateway-transaction-failure?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	saveExplicitDataInterface(t, store, "eth1")
	useVPPProof(t, "eth0")
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}), WithGatewayTransaction(failingGatewayTransaction{}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/apply", `{}`, login.Result().Cookies()[0])

	// Then
	if response.Code != http.StatusAccepted || strings.Contains(response.Body.String(), `"status":"committed"`) || strings.Contains(response.Body.String(), `"runtime_state":"running"`) {
		t.Fatalf("failed gateway apply response = %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayRuntimeApplyFailureAuditsRollbackReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-gateway-runtime-apply-failure?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	saveExplicitDataInterface(t, store, "eth1")
	useVPPProof(t, "eth0")
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}}
	server := New(
		WithStore(store),
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}),
		WithGatewayTransaction(failingGatewayTransaction{}),
		WithClock(fixedClock()),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}

	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/runtime/apply", `{}`, login.Result().Cookies()[0])
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"apply_failed"`) {
		t.Fatalf("runtime apply failure response = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"runtime_state":"running"`) || !strings.Contains(response.Body.String(), `"rollback_receipt"`) {
		t.Fatalf("runtime apply failure lost rollback evidence: %s", response.Body.String())
	}

	events, err := store.AuditEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var runtimeFailure, rollback bool
	for _, event := range events {
		if event.Resource == "/api/v1/runtime/apply" && event.Action == "apply" && event.Status == "failure" {
			runtimeFailure = true
		}
		if event.Action == "rollback" && event.Status == "rollback" {
			rollback = true
		}
	}
	if !runtimeFailure || !rollback {
		t.Fatalf("runtime apply failure audit events = %#v", events)
	}
	if path := strings.TrimSpace(os.Getenv("LY_ROUTE_RUNTIME_APPLY_FAILURE_EVIDENCE")); path != "" {
		evidence, err := json.MarshalIndent(map[string]any{
			"response":     json.RawMessage(response.Body.Bytes()),
			"audit_events": events,
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(evidence, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type failingGatewayTransaction struct{}

func (failingGatewayTransaction) Run(context.Context, apply.Plan) (apply.GatewayTransactionResult, error) {
	return apply.GatewayTransactionResult{}, errors.New("middle gateway resource failed")
}

func (failingGatewayTransaction) Rollback(context.Context, apply.Plan) error { return nil }

func gatewayLifecycleBondServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "eth0")
	store, err := persistence.Open(context.Background(), "file:gateway-lifecycle-bond?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	saveExplicitDataInterface(t, store, "eth1")
	useVPPProof(t, "eth0")
	server := New(
		WithStore(store),
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{
			{"id": "eth1", "name": "eth1", "work_mode": "vpp", "speed": "10G"},
			{"id": "eth2", "name": "eth2", "work_mode": "vpp", "speed": "10G"},
		}}),
		WithClock(fixedClock()),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}
	return server, login.Result().Cookies()[0]
}
