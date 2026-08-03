package orchestratorapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ly-route/backend/internal/orchestrator"
	"ly-route/backend/internal/runtime/vpp"
)

type policyCompileRequest struct {
	Flow    orchestrator.FlowInput    `json:"flow"`
	Prelude orchestrator.PreludeInput `json:"prelude"`
}

type serviceChainCompileRequest struct {
	Flow     orchestrator.FlowInput                  `json:"flow"`
	Prelude  orchestrator.PreludeInput               `json:"prelude"`
	Bindings []orchestrator.ServiceChainBindingInput `json:"bindings"`
}

type serviceChainApplyRequest struct {
	Flow        orchestrator.FlowInput                  `json:"flow"`
	Prelude     orchestrator.PreludeInput               `json:"prelude"`
	Bindings    []orchestrator.ServiceChainBindingInput `json:"bindings"`
	Attachments []vpp.NativeAttachment                  `json:"attachments"`
}

func (handler *Handler) policyRepository() (orchestrator.PolicyRepository, bool) {
	repository, ok := handler.repository.(orchestrator.PolicyRepository)
	return repository, ok
}

func (handler *Handler) handlePolicy(writer http.ResponseWriter, request *http.Request) {
	permission := PermissionRead
	if request.Method == http.MethodPut || request.Method == http.MethodDelete {
		permission = PermissionAdminWrite
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPut && request.Method != http.MethodDelete {
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
		return
	}
	if !handler.authorize(writer, request, permission) {
		return
	}
	repository, ok := handler.policyRepository()
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, orchestrator.ErrRepositoryUnavailable)
		return
	}
	switch request.Method {
	case http.MethodGet:
		policy, checksum, err := repository.PolicySnapshot(request.Context())
		if err != nil {
			writePolicyRepositoryError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": policy.View(), "checksum": checksum})
	case http.MethodPut:
		var input orchestrator.PolicyInput
		if err := decodeStrictJSON(writer, request, &input); err != nil {
			writeAPIError(writer, http.StatusBadRequest, err)
			return
		}
		topology, _, err := handler.repository.Snapshot(request.Context())
		if err != nil {
			writeRepositoryError(writer, err)
			return
		}
		policy, err := orchestrator.ParsePolicy(topology, input)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		previous, _, previousErr := repository.PolicySnapshot(request.Context())
		if previousErr != nil && !errors.Is(previousErr, orchestrator.ErrPolicyNotFound) {
			writePolicyRepositoryError(writer, previousErr)
			return
		}
		if err := handler.applyTransparent(request.Context(), topology, &policy); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := repository.ReplacePolicy(request.Context(), policy); err != nil {
			var rollbackErr error
			if previousErr == nil {
				rollbackErr = handler.rollbackTransparent(request.Context(), topology, &previous)
			} else {
				rollbackErr = handler.rollbackTransparent(request.Context(), topology, nil)
			}
			if rollbackErr != nil {
				writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("persist policy: %v; restore transparent dataplane: %w", err, rollbackErr))
				return
			}
			writePolicyRepositoryError(writer, err)
			return
		}
		if err := handler.commitTransparentTransaction(request.Context()); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		stored, checksum, err := repository.PolicySnapshot(request.Context())
		if err != nil {
			writePolicyRepositoryError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": stored.View(), "checksum": checksum})
	case http.MethodDelete:
		topology, _, topologyErr := handler.repository.Snapshot(request.Context())
		if topologyErr != nil {
			writeRepositoryError(writer, topologyErr)
			return
		}
		previous, _, previousErr := repository.PolicySnapshot(request.Context())
		if previousErr != nil {
			writePolicyRepositoryError(writer, previousErr)
			return
		}
		if err := handler.applyTransparent(request.Context(), topology, nil); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := repository.DeletePolicy(request.Context()); err != nil {
			if rollbackErr := handler.rollbackTransparent(request.Context(), topology, &previous); rollbackErr != nil {
				writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("delete policy: %v; restore transparent dataplane: %w", err, rollbackErr))
				return
			}
			writePolicyRepositoryError(writer, err)
			return
		}
		if err := handler.commitTransparentTransaction(request.Context()); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

func (handler *Handler) handlePolicyCompile(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
		return
	}
	if !handler.authorize(writer, request, PermissionRead) {
		return
	}
	repository, ok := handler.policyRepository()
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, orchestrator.ErrRepositoryUnavailable)
		return
	}
	var input policyCompileRequest
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	policy, checksum, err := repository.PolicySnapshot(request.Context())
	if err != nil {
		writePolicyRepositoryError(writer, err)
		return
	}
	flow, err := orchestrator.ParseFlow(input.Flow)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	prelude, err := orchestrator.ParsePrelude(input.Prelude)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	path, err := orchestrator.CompilePolicy(policy, flow, prelude)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"path": path, "policy_checksum": checksum})
}

func (handler *Handler) handleServiceChainCompile(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
		return
	}
	if !handler.authorize(writer, request, PermissionRead) {
		return
	}
	repository, ok := handler.policyRepository()
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, orchestrator.ErrRepositoryUnavailable)
		return
	}
	var input serviceChainCompileRequest
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	topology, topologyChecksum, err := handler.repository.Snapshot(request.Context())
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	policy, policyChecksum, err := repository.PolicySnapshot(request.Context())
	if err != nil {
		writePolicyRepositoryError(writer, err)
		return
	}
	flow, err := orchestrator.ParseFlow(input.Flow)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	prelude, err := orchestrator.ParsePrelude(input.Prelude)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	path, err := orchestrator.CompilePolicy(policy, flow, prelude)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	response := map[string]any{"path": path, "topology_checksum": topologyChecksum, "policy_checksum": policyChecksum}
	if path.Exit == orchestrator.PathExitLAN {
		chain, chainErr := orchestrator.CompileServiceChain(topology, flow, path, input.Bindings)
		if chainErr != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, chainErr)
			return
		}
		response["chain"] = chain
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) handleServiceChainApply(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
		return
	}
	if !handler.authorize(writer, request, PermissionAdminWrite) {
		return
	}
	if handler.runtime == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, errors.New("orchestrator service-chain runtime is not configured"))
		return
	}
	repository, ok := handler.policyRepository()
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, orchestrator.ErrRepositoryUnavailable)
		return
	}
	var input serviceChainApplyRequest
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	topology, topologyChecksum, err := handler.repository.Snapshot(request.Context())
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	policy, policyChecksum, err := repository.PolicySnapshot(request.Context())
	if err != nil {
		writePolicyRepositoryError(writer, err)
		return
	}
	flow, err := orchestrator.ParseFlow(input.Flow)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	prelude, err := orchestrator.ParsePrelude(input.Prelude)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	path, err := orchestrator.CompilePolicy(policy, flow, prelude)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if path.Exit != orchestrator.PathExitLAN {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("service-chain apply requires a LAN-exit policy path"))
		return
	}
	desiredChain, err := orchestrator.CompileServiceChain(topology, flow, path, input.Bindings)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	intentRepository, ok := handler.repository.(ServiceChainIntentRepository)
	if !ok {
		writeAPIError(writer, http.StatusServiceUnavailable, errors.New("orchestrator service-chain intent persistence is unavailable"))
		return
	}
	intentPayload, err := json.Marshal(input)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("service-chain intent could not be encoded"))
		return
	}
	if err := intentRepository.SaveServiceChainIntent(request.Context(), desiredChain.ID, intentPayload); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, errors.New("service-chain intent could not be persisted"))
		return
	}
	unavailable := map[string]bool{}
	var health []orchestrator.GroupHealth
	if healthRuntime, ok := handler.runtime.(ServiceChainHealthRuntime); ok {
		unavailable, health, err = healthRuntime.ServiceChainUnavailable(request.Context(), input.Bindings)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
	}
	chain, err := orchestrator.CompileServiceChainWithHealth(topology, flow, path, input.Bindings, unavailable)
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	chain.ID = desiredChain.ID
	transactionID := strings.TrimSpace(request.Header.Get("X-Request-ID"))
	if transactionID == "" {
		transactionID = fmt.Sprintf("orchestrator-chain-%d", time.Now().UnixNano())
	}
	var result vpp.ServiceChainApplyResult
	if transitionRuntime, ok := handler.runtime.(ServiceChainTransitionRuntime); ok {
		result, err = transitionRuntime.ApplyServiceChainTransition(request.Context(), transactionID, desiredChain, chain, input.Attachments)
	} else {
		result, err = handler.runtime.ApplyServiceChain(request.Context(), transactionID, chain, input.Attachments)
	}
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"chain":             chain,
		"receipt":           result.Receipt,
		"readback":          result.Readback,
		"topology_checksum": topologyChecksum,
		"policy_checksum":   policyChecksum,
		"health":            health,
		"intent_id":         desiredChain.ID,
	})
}

func writePolicyRepositoryError(writer http.ResponseWriter, err error) {
	if errors.Is(err, orchestrator.ErrPolicyNotFound) || errors.Is(err, orchestrator.ErrTopologyNotFound) {
		writeAPIError(writer, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, orchestrator.ErrInvalidPolicyVersion) || errors.Is(err, orchestrator.ErrInvalidPolicyGroup) || errors.Is(err, orchestrator.ErrInvalidPolicyRule) || errors.Is(err, orchestrator.ErrDeletedPolicyReference) {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeAPIError(writer, http.StatusInternalServerError, errors.New("orchestration policy state is unavailable"))
}
