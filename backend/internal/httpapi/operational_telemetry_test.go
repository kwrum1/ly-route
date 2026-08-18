package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDashboardSummaryIncludesOperationalFacts(t *testing.T) {
	now := time.Date(2026, 8, 14, 9, 10, 11, 0, time.FixedZone("CST", 8*60*60))
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(func() time.Time { return now }),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", "{\"username\":\"admin\",\"password\":\"secret\"}")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}
	response := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/dashboard/summary", "", login.Result().Cookies()[0])
	if response.Code != http.StatusOK {
		t.Fatalf("summary status = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	system, ok := body["system"].(map[string]any)
	if !ok {
		t.Fatalf("system = %#v", body["system"])
	}
	if system["system_time"] != now.Format(time.RFC3339) {
		t.Fatalf("system_time = %#v, want %q", system["system_time"], now.Format(time.RFC3339))
	}
	if _, exists := system["uptime_seconds"]; !exists {
		t.Fatalf("system missing uptime_seconds: %#v", system)
	}
	platform, ok := system["platform"].(string)
	if !ok || strings.TrimSpace(platform) == "" {
		t.Fatalf("platform = %#v, want non-empty", system["platform"])
	}
}

func TestReadSystemOperationalFactsParsesLinuxUptimeAndPlatform(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	readFile := func(path string) ([]byte, error) {
		switch path {
		case "/proc/uptime":
			return []byte("93784.42 1234.00\n"), nil
		case "/sys/devices/virtual/dmi/id/product_name":
			return []byte("VMware Virtual Platform\n"), nil
		default:
			return nil, errors.New("not found")
		}
	}
	facts, capability := readSystemOperationalFacts(now, readFile)
	if !capability.Available || facts["uptime_seconds"] != int64(93784) || facts["system_time"] != now.Format(time.RFC3339) || facts["platform"] != "VMware Virtual Platform" {
		t.Fatalf("facts = %#v, capability = %#v", facts, capability)
	}
}

func TestTopSessionsUseConnectionCountWithoutFallingBackToBytes(t *testing.T) {
	observedAt := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(func() time.Time { return observedAt }),
		WithGatewayTelemetry(&scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{{
			ObservedAt: observedAt,
			Connections: []GatewayConnection{
				{SourceIP: "192.168.88.10", DestinationIP: "8.8.8.8", Protocol: "udp", Bytes: 8192},
				{SourceIP: "192.168.88.20", ConnectionCount: 7, Bytes: 65536},
			},
		}}}),
	)
	api := authenticatedHTTPClient(t, server)
	result := api.telemetryData(t, "/api/v1/telemetry/top-sessions")
	items := mapItems(t, result["items"])
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	counts := map[string]float64{}
	for _, item := range items {
		counts[item["src_ip"].(string)] = item["connection_count"].(float64)
	}
	if counts["192.168.88.10"] != 1 || counts["192.168.88.20"] != 7 {
		t.Fatalf("connection counts = %#v, want detailed=1 aggregate=7", counts)
	}
	if counts["192.168.88.10"] == 8192 || counts["192.168.88.20"] == 65536 {
		t.Fatalf("connection count fell back to bytes: %#v", counts)
	}
}

func TestLegacyTopSessionCollectorGetsCanonicalConnectionCount(t *testing.T) {
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithTopTelemetry(fakeTopTelemetry{sessions: []map[string]any{{
			"src_ip": "192.168.88.30", "dst_ip": "1.1.1.1", "bytes": 4096,
		}}}),
	)
	api := authenticatedHTTPClient(t, server)
	result := api.telemetryData(t, "/api/v1/telemetry/top-sessions")
	items := mapItems(t, result["items"])
	if len(items) != 1 || items[0]["connection_count"] != float64(1) || items[0]["bytes"] != float64(4096) {
		t.Fatalf("legacy top session = %#v", items)
	}
}
