package orchestratorapi

import (
	"errors"
	"net/http"
)

var (
	ErrAccessControllerUnavailable = errors.New("orchestrator API access controller is unavailable")
	ErrAuthenticationRequired      = errors.New("authentication required")
	ErrAdminRequired               = errors.New("admin role required")
)

type Permission string

const (
	PermissionRead       Permission = "read"
	PermissionAdminWrite Permission = "admin_write"
)

type AccessController interface {
	Authorize(*http.Request, Permission) error
}

func (handler *Handler) authorize(writer http.ResponseWriter, request *http.Request, permission Permission) bool {
	if err := handler.access.Authorize(request, permission); err != nil {
		switch {
		case errors.Is(err, ErrAuthenticationRequired):
			writeAPIError(writer, http.StatusUnauthorized, err)
		case errors.Is(err, ErrAdminRequired):
			writeAPIError(writer, http.StatusForbidden, err)
		default:
			writeAPIError(writer, http.StatusForbidden, ErrAdminRequired)
		}
		return false
	}
	return true
}
