package orchestratorapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"ly-route/backend/internal/orchestrator"
	"ly-route/backend/internal/runtime/vpp"
)

const (
	TopologyPath            = "/api/v1/orchestrator/topology"
	OrchestrationGroupsPath = "/api/v1/orchestrator/orchestration-groups"
	PolicyPath              = "/api/v1/orchestrator/policy"
	PolicyCompilePath       = "/api/v1/orchestrator/policy/compile"
	ServiceChainCompilePath = "/api/v1/orchestrator/service-chains/compile"
	ServiceChainApplyPath   = "/api/v1/orchestrator/service-chains/apply"
	ServiceChainStatusPath  = "/api/v1/orchestrator/service-chains/status"
	maxBodyBytes            = 1 << 20
)

type Handler struct {
	repository       orchestrator.Orchestrator
	access           AccessController
	runtime          ServiceChainRuntime
	managementShared func(context.Context, string) bool
	reconcile        reconcileState
}

type ServiceChainRuntime interface {
	ApplyServiceChain(context.Context, string, orchestrator.ServiceChain, []vpp.NativeAttachment) (vpp.ServiceChainApplyResult, error)
}

type TransparentRuntime interface {
	ApplyTransparent(context.Context, string, orchestrator.Topology, *orchestrator.Policy) error
}

type TransparentDisableRuntime interface {
	DisableTransparent(context.Context, string) error
}

type TransparentRuntimeEvidence struct {
	TransactionID string    `json:"transaction_id"`
	Generation    string    `json:"generation"`
	State         string    `json:"state"`
	AppliedAt     time.Time `json:"applied_at"`
	ObservedAt    time.Time `json:"observed_at"`
}

type TransparentRuntimeEvidenceProvider interface {
	TransparentRuntimeEvidence(context.Context) (TransparentRuntimeEvidence, error)
}

type TransparentTransactionRuntime interface {
	CommitTransparentTransaction(context.Context) error
}

func (handler *Handler) SetManagementSharingResolver(resolver func(context.Context, string) bool) {
	handler.managementShared = resolver
}

type ServiceChainTransitionRuntime interface {
	ApplyServiceChainTransition(context.Context, string, orchestrator.ServiceChain, orchestrator.ServiceChain, []vpp.NativeAttachment) (vpp.ServiceChainApplyResult, error)
}

type ServiceChainHealthRuntime interface {
	ServiceChainUnavailable(context.Context, []orchestrator.ServiceChainBindingInput) (map[string]bool, []orchestrator.GroupHealth, error)
}

type ServiceChainIntentRepository interface {
	SaveServiceChainIntent(context.Context, string, json.RawMessage) error
	ServiceChainIntents(context.Context) ([]orchestrator.ServiceChainIntentRecord, error)
}

func New(repository orchestrator.Orchestrator, access AccessController, runtimes ...ServiceChainRuntime) (*Handler, error) {
	if interfaceIsNil(repository) {
		return nil, orchestrator.ErrRepositoryUnavailable
	}
	if interfaceIsNil(access) {
		return nil, ErrAccessControllerUnavailable
	}
	var runtime ServiceChainRuntime
	if len(runtimes) > 0 {
		runtime = runtimes[0]
	}
	return &Handler{repository: repository, access: access, runtime: runtime}, nil
}

func interfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return reflected.IsNil()
	default:
		return false
	}
}

func (handler *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(TopologyPath, handler.handleTopology)
	mux.HandleFunc(OrchestrationGroupsPath, handler.handleGroups)
	mux.HandleFunc(OrchestrationGroupsPath+"/", handler.handleGroup)
	mux.HandleFunc(PolicyPath, handler.handlePolicy)
	mux.HandleFunc(PolicyCompilePath, handler.handlePolicyCompile)
	if _, transparent := handler.runtime.(TransparentRuntime); !transparent {
		mux.HandleFunc(ServiceChainCompilePath, handler.handleServiceChainCompile)
		mux.HandleFunc(ServiceChainApplyPath, handler.handleServiceChainApply)
		mux.HandleFunc(ServiceChainStatusPath, handler.handleServiceChainStatus)
	}
}

func (handler *Handler) handleTopology(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		if !handler.authorize(writer, request, PermissionRead) {
			return
		}
		handler.writeTopology(writer, request)
	case http.MethodPut:
		if !handler.authorize(writer, request, PermissionAdminWrite) {
			return
		}
		var dto topologyDTO
		if err := decodeStrictJSON(writer, request, &dto); err != nil {
			writeAPIError(writer, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
			return
		}
		if handler.managementShared != nil {
			dto.ManagementShared = handler.managementShared(request.Context(), dto.ManagementInterface)
		} else {
			dto.ManagementShared = false
		}
		topology, err := orchestrator.ParseTopology(dto.input())
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		previous, _, previousErr := handler.repository.Snapshot(request.Context())
		if previousErr != nil && !errors.Is(previousErr, orchestrator.ErrTopologyNotFound) {
			writeRepositoryError(writer, previousErr)
			return
		}
		var policy *orchestrator.Policy
		if previousErr == nil {
			var policyErr error
			policy, policyErr = handler.currentPolicy(request.Context())
			if policyErr != nil {
				writePolicyRepositoryError(writer, policyErr)
				return
			}
		}
		if err := handler.applyTransparent(request.Context(), topology, policy); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := handler.repository.Replace(request.Context(), topology); err != nil {
			if previousErr == nil {
				if rollbackErr := handler.rollbackTransparent(request.Context(), previous, policy); rollbackErr != nil {
					writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("persist topology: %v; restore transparent dataplane: %w", err, rollbackErr))
					return
				}
			} else if rollbackErr := handler.rollbackTransparentAbsent(request.Context()); rollbackErr != nil {
				writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("persist topology: %v; disable unpersisted transparent dataplane: %w", err, rollbackErr))
				return
			}
			writeRepositoryError(writer, err)
			return
		}
		if err := handler.commitTransparentTransaction(request.Context()); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		handler.writeTopology(writer, request)
	case http.MethodDelete:
		if !handler.authorize(writer, request, PermissionAdminWrite) {
			return
		}
		current, _, snapshotErr := handler.repository.Snapshot(request.Context())
		if snapshotErr != nil {
			writeRepositoryError(writer, snapshotErr)
			return
		}
		policy, policyErr := handler.currentPolicy(request.Context())
		if policyErr != nil {
			writePolicyRepositoryError(writer, policyErr)
			return
		}
		if policy != nil {
			writeMutationError(writer, orchestrator.ErrDeletedPolicyReference)
			return
		}
		if err := handler.disableTransparent(request.Context()); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := handler.repository.Delete(request.Context()); err != nil {
			if rollbackErr := handler.rollbackTransparent(request.Context(), current, nil); rollbackErr != nil {
				writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("delete topology: %v; restore transparent dataplane: %w", err, rollbackErr))
				return
			}
			writeRepositoryError(writer, err)
			return
		}
		if err := handler.commitTransparentTransaction(request.Context()); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
	}
}

func (handler *Handler) handleGroups(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		if !handler.authorize(writer, request, PermissionRead) {
			return
		}
		topology, checksum, err := handler.repository.Snapshot(request.Context())
		if err != nil {
			writeRepositoryError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, groupsResponse{Items: topology.View().Groups, Checksum: checksum})
	case http.MethodPost:
		if !handler.authorize(writer, request, PermissionAdminWrite) {
			return
		}
		group, err := parseGroupRequest(writer, request)
		if err != nil {
			return
		}
		current, _, snapshotErr := handler.repository.Snapshot(request.Context())
		if snapshotErr != nil {
			writeMutationError(writer, snapshotErr)
			return
		}
		candidate, candidateErr := orchestrator.AddTopologyGroup(current, group)
		if candidateErr != nil {
			writeMutationError(writer, candidateErr)
			return
		}
		policy, policyErr := handler.currentPolicy(request.Context())
		if policyErr != nil {
			writePolicyRepositoryError(writer, policyErr)
			return
		}
		if err := handler.applyTransparent(request.Context(), candidate, policy); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := handler.repository.CreateGroup(request.Context(), group); err != nil {
			if rollbackErr := handler.rollbackTransparent(request.Context(), current, policy); rollbackErr != nil {
				writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("create group: %v; restore transparent dataplane: %w", err, rollbackErr))
				return
			}
			writeMutationError(writer, err)
			return
		}
		if err := handler.commitTransparentTransaction(request.Context()); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		handler.writeGroup(writer, request, group.Name(), http.StatusCreated)
	default:
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
	}
}

func (handler *Handler) handleGroup(writer http.ResponseWriter, request *http.Request) {
	var permission Permission
	switch request.Method {
	case http.MethodGet:
		permission = PermissionRead
	case http.MethodPut, http.MethodDelete:
		permission = PermissionAdminWrite
	default:
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
		return
	}
	if !handler.authorize(writer, request, permission) {
		return
	}
	name, err := groupNameFromPath(request.URL.Path)
	if err != nil {
		writeAPIError(writer, http.StatusNotFound, orchestrator.ErrGroupNotFound)
		return
	}
	switch request.Method {
	case http.MethodGet:
		handler.writeGroup(writer, request, name, http.StatusOK)
	case http.MethodPut:
		group, parseErr := parseGroupRequest(writer, request)
		if parseErr != nil {
			return
		}
		if group.Name() != name {
			writeAPIError(writer, http.StatusUnprocessableEntity, fmt.Errorf("%w: body name must match path", orchestrator.ErrInvalidName))
			return
		}
		current, _, snapshotErr := handler.repository.Snapshot(request.Context())
		if snapshotErr != nil {
			writeMutationError(writer, snapshotErr)
			return
		}
		candidate, candidateErr := orchestrator.ReplaceTopologyGroup(current, name, group)
		if candidateErr != nil {
			writeMutationError(writer, candidateErr)
			return
		}
		policy, policyErr := handler.currentPolicy(request.Context())
		if policyErr != nil {
			writePolicyRepositoryError(writer, policyErr)
			return
		}
		if err := handler.applyTransparent(request.Context(), candidate, policy); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := handler.repository.ReplaceGroup(request.Context(), name, group); err != nil {
			if rollbackErr := handler.rollbackTransparent(request.Context(), current, policy); rollbackErr != nil {
				writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("replace group: %v; restore transparent dataplane: %w", err, rollbackErr))
				return
			}
			writeMutationError(writer, err)
			return
		}
		if err := handler.commitTransparentTransaction(request.Context()); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		handler.writeGroup(writer, request, name, http.StatusOK)
	case http.MethodDelete:
		current, _, snapshotErr := handler.repository.Snapshot(request.Context())
		if snapshotErr != nil {
			writeMutationError(writer, snapshotErr)
			return
		}
		candidate, candidateErr := orchestrator.RemoveTopologyGroup(current, name)
		if candidateErr != nil {
			writeMutationError(writer, candidateErr)
			return
		}
		policy, policyErr := handler.currentPolicy(request.Context())
		if policyErr != nil {
			writePolicyRepositoryError(writer, policyErr)
			return
		}
		if err := handler.applyTransparent(request.Context(), candidate, policy); err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, err)
			return
		}
		if err := handler.repository.DeleteGroup(request.Context(), name); err != nil {
			if rollbackErr := handler.rollbackTransparent(request.Context(), current, policy); rollbackErr != nil {
				writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("delete group: %v; restore transparent dataplane: %w", err, rollbackErr))
				return
			}
			writeMutationError(writer, err)
			return
		}
		if err := handler.commitTransparentTransaction(request.Context()); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}
}

func (handler *Handler) currentPolicy(ctx context.Context) (*orchestrator.Policy, error) {
	repository, ok := handler.policyRepository()
	if !ok {
		return nil, nil
	}
	policy, _, err := repository.PolicySnapshot(ctx)
	if errors.Is(err, orchestrator.ErrPolicyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

func (handler *Handler) applyTransparent(ctx context.Context, topology orchestrator.Topology, policy *orchestrator.Policy) error {
	runtime, ok := handler.runtime.(TransparentRuntime)
	if !ok {
		return nil
	}
	requestID := fmt.Sprintf("orchestrator-transparent-%d", time.Now().UnixNano())
	return runtime.ApplyTransparent(ctx, requestID, topology, policy)
}

func (handler *Handler) disableTransparent(ctx context.Context) error {
	runtime, ok := handler.runtime.(TransparentDisableRuntime)
	if !ok {
		return nil
	}
	requestID := fmt.Sprintf("orchestrator-transparent-disable-%d", time.Now().UnixNano())
	return runtime.DisableTransparent(ctx, requestID)
}

func (handler *Handler) rollbackTransparent(ctx context.Context, topology orchestrator.Topology, policy *orchestrator.Policy) error {
	if err := handler.applyTransparent(ctx, topology, policy); err != nil {
		return err
	}
	return handler.commitTransparentTransaction(ctx)
}

func (handler *Handler) rollbackTransparentAbsent(ctx context.Context) error {
	if err := handler.disableTransparent(ctx); err != nil {
		return err
	}
	return handler.commitTransparentTransaction(ctx)
}

func (handler *Handler) commitTransparentTransaction(ctx context.Context) error {
	runtime, ok := handler.runtime.(TransparentTransactionRuntime)
	if !ok {
		return nil
	}
	if err := runtime.CommitTransparentTransaction(ctx); err != nil {
		return fmt.Errorf("commit transparent transaction journal: %w", err)
	}
	return nil
}

func (handler *Handler) writeTopology(writer http.ResponseWriter, request *http.Request) {
	topology, checksum, err := handler.repository.Snapshot(request.Context())
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, topologyResponse{Item: topology.View(), Checksum: checksum})
}

func (handler *Handler) writeGroup(writer http.ResponseWriter, request *http.Request, name string, status int) {
	topology, checksum, err := handler.repository.Snapshot(request.Context())
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	for _, group := range topology.View().Groups {
		if group.Name == name {
			writeJSON(writer, status, groupResponse{Item: group, Checksum: checksum})
			return
		}
	}
	writeAPIError(writer, http.StatusNotFound, fmt.Errorf("%w: %q", orchestrator.ErrGroupNotFound, name))
}

func parseGroupRequest(writer http.ResponseWriter, request *http.Request) (orchestrator.Group, error) {
	var dto groupDTO
	if err := decodeStrictJSON(writer, request, &dto); err != nil {
		writeAPIError(writer, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return orchestrator.Group{}, err
	}
	group, err := orchestrator.ParseGroup(dto.input())
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return orchestrator.Group{}, err
	}
	return group, nil
}

func decodeStrictJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func groupNameFromPath(path string) (string, error) {
	raw := strings.TrimPrefix(path, OrchestrationGroupsPath+"/")
	if raw == "" || strings.ContainsAny(raw, `/\`) {
		return "", orchestrator.ErrGroupNotFound
	}
	name, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(name) == "" || strings.ContainsAny(name, `/\`) {
		return "", orchestrator.ErrGroupNotFound
	}
	return name, nil
}

func writeMutationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, orchestrator.ErrTopologyNotFound), errors.Is(err, orchestrator.ErrDuplicateGroup), errors.Is(err, orchestrator.ErrTopologyConflict):
		writeAPIError(writer, http.StatusConflict, err)
	case errors.Is(err, orchestrator.ErrGroupNotFound):
		writeAPIError(writer, http.StatusNotFound, err)
	case codeForError(err) != "internal_error":
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
	default:
		writeAPIError(writer, http.StatusInternalServerError, errors.New("orchestrator state mutation failed"))
	}
}

func writeRepositoryError(writer http.ResponseWriter, err error) {
	if errors.Is(err, orchestrator.ErrTopologyNotFound) || errors.Is(err, orchestrator.ErrGroupNotFound) {
		writeAPIError(writer, http.StatusNotFound, err)
		return
	}
	writeAPIError(writer, http.StatusInternalServerError, errors.New("orchestrator state is unavailable"))
}
