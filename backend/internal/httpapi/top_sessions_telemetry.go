package httpapi

import (
	"net/http"

	controlapi "ly-route/backend/internal/api"
)

func (server *Server) handleTopSessionsTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if _, ok := server.sessionFromRequest(r); !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	payload, capabilities := server.telemetryPayload(r.Context(), "top_sessions")
	normalizeTopSessionConnectionCounts(payload)
	writeJSON(w, http.StatusOK, map[string]any{"data": payload, "capabilities": capabilities, "request_id": requestID(r)})
}

func (server *Server) handleActiveSessionsTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if _, ok := server.sessionFromRequest(r); !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	items, state, reason := server.gatewayActiveSessions(r.Context())
	available := state == "available"
	capability := controlapi.CapabilityState{Name: "active_sessions", Available: available, State: controlapi.CapabilityAvailable, Reason: reason}
	if !available {
		capability.State = controlapi.CapabilityDegraded
	}
	payload := map[string]any{"items": normalizeTopTelemetry(items, state, !available, reason), "runtime_state": state, "state": state, "degraded": !available, "degraded_reason": reason}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload, "capabilities": []controlapi.CapabilityState{capability}, "request_id": requestID(r)})
}

func normalizeTopSessionConnectionCounts(payload any) {
	container, ok := payload.(map[string]any)
	if !ok {
		return
	}
	items, ok := container["items"].([]map[string]any)
	if !ok {
		return
	}
	for _, item := range items {
		if count, found := numberField(item, "connection_count", "connections", "sessions"); found {
			item["connection_count"] = count
			continue
		}
		// A detailed top-session row represents one live connection. Bytes remain
		// traffic volume and must never be exposed as a connection count.
		item["connection_count"] = 1
	}
}
