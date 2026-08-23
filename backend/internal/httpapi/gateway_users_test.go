package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGatewayCollectorsBackTopConnectionsAndDHCPNeighborOnlineUsers(t *testing.T) {
	observedAt := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{
		{
			ObservedAt:  observedAt,
			Connections: []GatewayConnection{{SourceIP: "192.168.88.10", DestinationIP: "8.8.8.8", Protocol: "udp", SourcePort: 53000, DestinationPort: 53, Bytes: 4096}},
			Neighbors:   []GatewayNeighbor{{IP: "192.168.88.10", MAC: "00:11:22:33:44:55", LastSeen: observedAt, DownloadBytes: 100_000, UploadBytes: 20_000}},
		},
		{
			ObservedAt:  observedAt,
			Connections: []GatewayConnection{{SourceIP: "192.168.88.10", DestinationIP: "8.8.8.8", Protocol: "udp", SourcePort: 53000, DestinationPort: 53, Bytes: 4096}},
			Neighbors:   []GatewayNeighbor{{IP: "192.168.88.10", MAC: "00:11:22:33:44:55", LastSeen: observedAt, DownloadBytes: 100_000, UploadBytes: 20_000}},
		},
	}}
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(func() time.Time { return observedAt }),
		WithGatewayTelemetry(collector),
		WithDHCPLeases(fakeDHCPLeases{items: []map[string]any{{"ip_address": "192.168.88.10", "mac": "00:11:22:33:44:55", "hostname": "workstation", "lease_end": observedAt.Add(time.Hour).Format(time.RFC3339)}}}),
		WithTopTelemetry(fakeTopTelemetry{domains: []map[string]any{{"domain": "must-not-leak.example", "queries": 9}}}),
	)
	api := authenticatedHTTPClient(t, server)

	connections := api.telemetryData(t, "/api/v1/telemetry/top-sessions")
	users := api.telemetryData(t, "/api/v1/telemetry/online-users")
	domains := api.telemetryData(t, "/api/v1/telemetry/top-domains")

	connectionItems := mapItems(t, connections["items"])
	if len(connectionItems) != 1 || connectionItems[0]["src_ip"] != "192.168.88.10" || connectionItems[0]["source_ip"] != "192.168.88.10" || connectionItems[0]["connection_count"] != float64(1) || connectionItems[0]["bytes"] != float64(4096) {
		t.Fatalf("top connections = %#v, want five-second source aggregation", connectionItems)
	}
	userItems := mapItems(t, users["items"])
	if len(userItems) != 1 || userItems[0]["hostname"] != "workstation" || userItems[0]["neighbor_state"] != "reachable" || userItems[0]["rx_bytes"] != float64(100_000) {
		t.Fatalf("online users = %#v, want DHCP identity enriched by neighbor activity", userItems)
	}
	domainItems := mapItems(t, domains["items"])
	if domains["degraded"] != false || len(domainItems) != 1 || domainItems[0]["domain"] != "must-not-leak.example" {
		t.Fatalf("Gateway top domains = %#v, want SmartDNS collector data", domains)
	}
}

func TestGatewayOnlineUsersIncludesStaticIPNeighborsWithoutDHCPLease(t *testing.T) {
	observedAt := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(func() time.Time { return observedAt }),
		WithGatewayTelemetry(&scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{{
			ObservedAt: observedAt,
			Neighbors:  []GatewayNeighbor{{IP: "192.168.88.20", MAC: "00:11:22:33:44:66", LastSeen: observedAt, DownloadBytes: 2048, UploadBytes: 1024}},
		}}}),
		WithDHCPLeases(fakeDHCPLeases{items: []map[string]any{}}),
	)
	api := authenticatedHTTPClient(t, server)

	result := api.telemetryData(t, "/api/v1/telemetry/online-users")
	items := mapItems(t, result["items"])
	if len(items) != 1 || items[0]["ip"] != "192.168.88.20" || items[0]["online_status"] != "online" || items[0]["rx_bytes"] != float64(2048) {
		t.Fatalf("static neighbor online users = %#v", items)
	}
}

func TestGatewayDashboardUsesLocalGatewayTelemetry(t *testing.T) {
	observedAt := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(func() time.Time { return observedAt }),
		WithGatewayTelemetry(&scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{{
			ObservedAt: observedAt,
			Connections: []GatewayConnection{{
				SourceIP: "192.168.88.20", DestinationIP: "203.0.113.20", Protocol: "tcp", SourcePort: 50000, DestinationPort: 443, Bytes: 4096,
			}},
		}}}),
	)
	api := authenticatedHTTPClient(t, server)

	result := api.telemetryData(t, "/api/v1/telemetry/dashboard")
	if result["sessions"] != float64(1) || result["degraded"] != false || result["active_path"] != "vpp" {
		t.Fatalf("gateway dashboard telemetry = %#v", result)
	}
}

func TestGatewayTopConnectionsPrunesHistoryBeyondFiveSeconds(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	now := start
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{
		{ObservedAt: start, Connections: []GatewayConnection{{SourceIP: "192.168.88.10", DestinationIP: "8.8.8.8", Protocol: "udp", Bytes: 100}}},
		{ObservedAt: start.Add(6 * time.Second), Connections: []GatewayConnection{{SourceIP: "192.168.88.10", DestinationIP: "1.1.1.1", Protocol: "tcp", Bytes: 200}}},
	}}
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(func() time.Time { return now }), WithGatewayTelemetry(collector))
	api := authenticatedHTTPClient(t, server)

	api.telemetryData(t, "/api/v1/telemetry/top-sessions")
	now = start.Add(6 * time.Second)
	result := api.telemetryData(t, "/api/v1/telemetry/top-sessions")

	items := mapItems(t, result["items"])
	if len(items) != 1 || items[0]["src_ip"] != "192.168.88.10" || items[0]["bytes"] != float64(200) {
		t.Fatalf("retained Top Connections = %#v, want only sessions observed in the latest five seconds", items)
	}
}

func TestGatewayOnlineUsersReportsNeighborReadbackFailure(t *testing.T) {
	observedAt := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(func() time.Time { return observedAt }),
		WithGatewayTelemetry(&scriptedGatewayTelemetry{errors: []error{errors.New("neighbor readback failed")}}),
		WithDHCPLeases(fakeDHCPLeases{items: []map[string]any{{"ip_address": "192.168.88.10", "mac": "00:11:22:33:44:55"}}}),
	)
	api := authenticatedHTTPClient(t, server)

	result := api.telemetryData(t, "/api/v1/telemetry/online-users")

	reason, _ := result["degraded_reason"].(string)
	if result["degraded"] != true || !strings.Contains(reason, "neighbor readback failed") {
		t.Fatalf("online users failure = %#v, want explicit neighbor readback reason", result)
	}
}

func TestGatewayOnlineUsersReportsNeighborCollectorUnavailable(t *testing.T) {
	observedAt := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(func() time.Time { return observedAt }),
		WithDHCPLeases(fakeDHCPLeases{items: []map[string]any{{"ip_address": "192.168.88.10", "mac": "00:11:22:33:44:55"}}}),
	)
	api := authenticatedHTTPClient(t, server)

	result := api.telemetryData(t, "/api/v1/telemetry/online-users")

	reason, _ := result["degraded_reason"].(string)
	if result["degraded"] != true || !strings.Contains(reason, "gateway telemetry collector is not configured") {
		t.Fatalf("online users unavailable = %#v, want explicit gateway-neighbor capability reason", result)
	}
}

func (api authenticatedTelemetryAPI) telemetryData(t *testing.T, path string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, api.baseURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(api.cookie)
	res, err := api.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func mapItems(t *testing.T, value any) []map[string]any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("items type = %T, want []any", value)
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item type = %T, want map[string]any", item)
		}
		result = append(result, object)
	}
	return result
}
