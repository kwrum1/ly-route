package orchestratorapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"ly-route/backend/internal/orchestrator"
)

type topologyResponse struct {
	Item     orchestrator.TopologyView `json:"item"`
	Checksum string                    `json:"checksum"`
}

type groupsResponse struct {
	Items    []orchestrator.GroupView `json:"items"`
	Checksum string                   `json:"checksum"`
}

type groupResponse struct {
	Item     orchestrator.GroupView `json:"item"`
	Checksum string                 `json:"checksum"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return
	}
}

func writeAPIError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, errorResponse{Error: errorBody{Code: codeForError(err), Message: err.Error()}})
}

func codeForError(err error) string {
	switch {
	case errors.Is(err, orchestrator.ErrInvalidSchemaVersion):
		return "invalid_schema_version"
	case errors.Is(err, orchestrator.ErrMissingLAN):
		return "missing_lan"
	case errors.Is(err, orchestrator.ErrDuplicateLAN):
		return "duplicate_lan"
	case errors.Is(err, orchestrator.ErrMissingWAN):
		return "missing_wan"
	case errors.Is(err, orchestrator.ErrDuplicateWAN):
		return "duplicate_wan"
	case errors.Is(err, orchestrator.ErrGroupSize):
		return "invalid_group_size"
	case errors.Is(err, orchestrator.ErrGroupDirection):
		return "invalid_group_direction"
	case errors.Is(err, orchestrator.ErrGroupBond):
		return "group_bond_forbidden"
	case errors.Is(err, orchestrator.ErrManagementMembership):
		return "management_interface_forbidden"
	case errors.Is(err, orchestrator.ErrLogicalMembership):
		return "logical_interface_forbidden"
	case errors.Is(err, orchestrator.ErrSharedInterface):
		return "interface_already_owned"
	case errors.Is(err, orchestrator.ErrDuplicateGroup):
		return "duplicate_group"
	case errors.Is(err, orchestrator.ErrTopologyNotFound):
		return "topology_not_found"
	case errors.Is(err, orchestrator.ErrTopologyConflict):
		return "topology_conflict"
	case errors.Is(err, orchestrator.ErrGroupNotFound):
		return "group_not_found"
	case errors.Is(err, ErrAuthenticationRequired):
		return "unauthorized"
	case errors.Is(err, ErrAdminRequired):
		return "forbidden"
	case errors.Is(err, orchestrator.ErrInvalidName), errors.Is(err, orchestrator.ErrInvalidInterface), errors.Is(err, orchestrator.ErrInvalidBond):
		return "invalid_topology"
	default:
		return "internal_error"
	}
}
