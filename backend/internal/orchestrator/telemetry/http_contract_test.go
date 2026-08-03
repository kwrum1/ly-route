package telemetry_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/orchestrator/telemetry"
)

func TestSnapshot_HTTP_JSON_exposes_Task17_seam_without_DHCP_or_TopDomains(t *testing.T) {
	// Given
	observedAt := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	item := observation(observedAt, []telemetry.InterfaceCounter{
		{Name: "wan0", RXBytes: 1_000, TXBytes: 500, LinkUp: true},
		{Name: "lan0", RXBytes: 490, TXBytes: 970, LinkUp: true},
		{Name: "east-lan", RXBytes: 970, TXBytes: 490, LinkUp: true},
		{Name: "east-wan", RXBytes: 500, TXBytes: 1_000, LinkUp: true},
		{Name: "west-lan", RXBytes: 970, TXBytes: 490, LinkUp: true},
		{Name: "west-wan", RXBytes: 500, TXBytes: 1_000, LinkUp: true},
	})
	collector := telemetry.NewCollector(mustTopology(t, true), &sequenceSource{observations: []telemetry.Observation{item}}, &fakeClock{now: observedAt})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(collector.Collect(r.Context())); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/task17-telemetry-seam", nil))

	// Then
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, required := range []string{
		`"state":"available"`,
		`"observed_at":"2026-07-29T16:00:00Z"`,
		`"wan_to_lan"`,
		`"lan_to_wan"`,
		`"additive":false`,
		`"online_users"`,
		`"top_connections"`,
		`"policy_hits"`,
		`"window_seconds":86400`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("HTTP body missing %s: %s", required, body)
		}
	}
	for _, forbidden := range []string{"top_domains", "dhcp", "leases"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("HTTP body contains forbidden %q: %s", forbidden, body)
		}
	}
}
