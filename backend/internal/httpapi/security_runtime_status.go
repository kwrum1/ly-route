package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

// SecurityGuardRuntimeObserver reads the protocol-aware VPP guard's observed
// state. It is deliberately independent of configuration persistence: a rule
// is reported running only after the VPP dataplane has read it back.
type SecurityGuardRuntimeObserver interface {
	SecurityGuardRules(context.Context) ([]SecurityGuardRuntimeRule, error)
}

type SecurityGuardRuntimeRule struct {
	ID           string    `json:"id"`
	Interface    string    `json:"interface"`
	Family       int       `json:"family"`
	Enabled      bool      `json:"enabled"`
	ThresholdPPS int       `json:"threshold_pps"`
	BurstPackets int       `json:"burst_packets"`
	Matched      uint64    `json:"matched"`
	Conform      uint64    `json:"conform"`
	Exceeded     uint64    `json:"exceeded"`
	Alerts       uint64    `json:"alerts"`
	Dropped      uint64    `json:"dropped"`
	ObservedAt   time.Time `json:"observed_at"`
}

func (server *Server) handleSecurityRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "security runtime status is read-only")
		return
	}
	state := "locked"
	reason := "no active VPP security-guard readback is available"
	rules := []SecurityGuardRuntimeRule{}
	if server.securityGuardObserver != nil {
		observed, err := server.securityGuardObserver.SecurityGuardRules(r.Context())
		if err != nil {
			state = "degraded"
			reason = "VPP security-guard runtime readback failed: " + strings.TrimSpace(err.Error())
		} else {
			rules = append(rules, observed...)
			sort.Slice(rules, func(left, right int) bool { return rules[left].ID < rules[right].ID })
			state = "running"
			reason = "VPP security-guard runtime readback verified"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item": map[string]any{
			"id":             "vpp-security-guard",
			"implementation": "ly_route_security_guard",
			"runtime_state":  state,
			"reason":         reason,
			"rules":          rules,
		},
		"request_id": requestID(r),
	})
}
