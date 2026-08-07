package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

type SmartQoSRuntimeObserver interface {
	VerifySmartQoS(context.Context, vpp.NativePath) error
}

func (server *Server) handleSmartQoSStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "built-in smart QoS is read-only")
		return
	}

	proofPath := strings.TrimSpace(os.Getenv("LY_ROUTE_VPP_CAPABILITY_PROOF"))
	if proofPath == "" {
		proofPath = "/var/lib/ly-route/vpp-native-capabilities.json"
	}
	request := vpp.LoadNativePathRequestWithSharedManagement(
		proofPath,
		server.managementInterfaceID(r.Context()),
		server.runtimeDataInterfaces(r.Context()),
		server.now().UTC(),
		server.managementNetworkShared(r.Context()),
	)
	request.RequireSmartQoS = true
	path, selectionErr := vpp.SelectNativePath(request)
	// Runtime proofs are intentionally short-lived and gate configuration
	// changes. For read-only status, an expired proof may identify the applied
	// path only when live VPP readback is available to verify it.
	if selectionErr != nil && server.smartQoSObserver != nil {
		observationRequest := request
		if observedAt := latestCapabilityObservation(request); !observedAt.IsZero() {
			observationRequest.Now = observedAt
			if observedPath, err := vpp.SelectNativePath(observationRequest); err == nil {
				path = observedPath
				selectionErr = nil
				request = observationRequest
			}
		}
	}

	state := "locked"
	reason := "no device-wide production VPP smart-QoS path is qualified"
	selectedTier := ""
	prerequisites := request.ReportPrerequisites
	candidates := []vpp.CandidateEvaluation{}
	if selectionErr == nil {
		selectedTier = string(path.Tier)
		prerequisites = path.Prerequisites
		candidates = path.Candidates
		state = "adapter_pending"
		reason = "the VPP smart-QoS plugin is qualified but no active runtime readback is available"
		if server.smartQoSObserver != nil {
			if observeErr := server.smartQoSObserver.VerifySmartQoS(r.Context(), path); observeErr == nil {
				state = "running"
				reason = "device-wide VPP smart QoS is active and verified"
			} else {
				reason = "the VPP smart-QoS plugin is qualified but runtime verification failed: " + observeErr.Error()
			}
		}
	} else {
		var locked *vpp.DataplaneLockedError
		if errors.As(selectionErr, &locked) {
			prerequisites = locked.Prerequisites
			candidates = locked.Candidates
			reason = selectionErr.Error()
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"item": map[string]any{
			"id":                      "builtin-smart-qos",
			"enabled":                 true,
			"mutable":                 false,
			"configuration_mode":      "built_in",
			"implementation":          "ly_route_vpp_smart_qos",
			"runtime_state":           state,
			"reason":                  reason,
			"selected_dataplane_tier": selectedTier,
			"low_level_controls":      []string{},
			"prerequisites":           prerequisites,
			"candidates":              candidates,
		},
		"request_id": requestID(r),
	})
}

func latestCapabilityObservation(request vpp.NativePathRequest) time.Time {
	var latest time.Time
	for _, assignment := range request.Assignments {
		proofs := assignment.Candidates
		if len(proofs) == 0 {
			proofs = []vpp.CapabilityProof{assignment.Proof}
		}
		for _, proof := range proofs {
			if proof.ObservedAt.After(latest) {
				latest = proof.ObservedAt
			}
		}
	}
	return latest
}
