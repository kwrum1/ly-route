package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	orchestratortelemetry "ly-route/backend/internal/orchestrator/telemetry"
	"ly-route/backend/internal/product"
)

type fixedOrchestratorTelemetry struct {
	snapshot orchestratortelemetry.Snapshot
}

func (collector fixedOrchestratorTelemetry) Collect(context.Context) orchestratortelemetry.Snapshot {
	return collector.snapshot
}

type panicDHCPLeases struct{}

func (panicDHCPLeases) Leases(context.Context) ([]map[string]any, error) {
	panic("orchestrator telemetry must not read DHCP leases")
}

func TestOrchestratorTelemetryUsesOnlyDedicatedVPPCollector(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	snapshot := orchestratortelemetry.Snapshot{
		Status: orchestratortelemetry.Status{State: orchestratortelemetry.StateAvailable, Fresh: true, CollectedAt: now, ObservedAt: now},
		Components: orchestratortelemetry.ComponentStatuses{
			Interfaces:  orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateAvailable},
			PolicyHits:  orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateAvailable},
			Neighbors:   orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateAvailable},
			Connections: orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateAvailable},
		},
		Groups:         []orchestratortelemetry.GroupTraffic{{Name: "firewall", State: orchestratortelemetry.StateAvailable}},
		PolicyHits:     []orchestratortelemetry.PolicyHit{{PolicyID: "allow-office", Hits: 17, State: orchestratortelemetry.StateAvailable, ObservedAt: now}},
		OnlineUsers:    []orchestratortelemetry.OnlineUser{{IP: "192.0.2.10", MAC: "00:11:22:33:44:55", Interface: "lan0", LastSeen: now}},
		TopConnections: []orchestratortelemetry.TopConnection{{ID: "flow-1", SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: "tcp", DestinationPort: 443, Bytes: 4096, LastSeen: now, Groups: []string{"firewall"}}},
	}
	selection := product.NewSelection()
	if err := selection.Initialize(product.Orchestrator()); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(selection,
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithOrchestratorTelemetry(fixedOrchestratorTelemetry{snapshot: snapshot}),
		WithDHCPLeases(panicDHCPLeases{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	checks := map[string][]string{
		"/api/v1/telemetry/dashboard":    {`"device_mode":"orchestrator"`, `"orchestration_groups"`, `"policy_hits":17`},
		"/api/v1/telemetry/online-users": {`"192.0.2.10"`, `"00:11:22:33:44:55"`},
		"/api/v1/telemetry/top-sessions": {`"flow-1"`, `"firewall"`},
		"/api/v1/telemetry/policy-hits":  {`"allow-office"`, `"hits":17`},
	}
	for path, fragments := range checks {
		response := authenticatedJSONRequest(t, server, http.MethodGet, path, "", cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
		for _, fragment := range fragments {
			if !strings.Contains(response.Body.String(), fragment) {
				t.Fatalf("GET %s missing %s: %s", path, fragment, response.Body.String())
			}
		}
		for _, forbidden := range []string{"gateway\"", "dhcp", "lease", "top_domains"} {
			if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
				t.Fatalf("GET %s leaked %q: %s", path, forbidden, response.Body.String())
			}
		}
	}
}

func TestOrchestratorTelemetryWithoutCollectorIsExplicitlyUnavailable(t *testing.T) {
	selection := product.NewSelection()
	if err := selection.Initialize(product.Orchestrator()); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(selection, WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithDHCPLeases(panicDHCPLeases{}))
	if err != nil {
		t.Fatal(err)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	response := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/online-users", "", login.Result().Cookies()[0])
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"unavailable"`) || !strings.Contains(response.Body.String(), "orchestrator VPP telemetry collector is not configured") {
		t.Fatalf("unexpected unavailable response: %d %s", response.Code, response.Body.String())
	}
}
