package orchestratorapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"ly-route/backend/internal/orchestrator"
)

func TestHandler_requires_authorized_reads_and_admin_writes(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		permission Permission
		accessErr  error
		wantStatus int
		wantCode   string
	}{
		{name: "read requires authentication", method: http.MethodGet, permission: PermissionRead, accessErr: ErrAuthenticationRequired, wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "write requires admin", method: http.MethodPut, permission: PermissionAdminWrite, accessErr: ErrAdminRequired, wantStatus: http.StatusForbidden, wantCode: "forbidden"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			repository, baseServer := newTestHTTPServer(t)
			baseServer.Close()
			handler, err := New(repository, denyAccess{permission: test.permission, err: test.accessErr})
			if err != nil {
				t.Fatalf("New handler: %v", err)
			}
			mux := http.NewServeMux()
			handler.RegisterRoutes(mux)
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			var body []byte
			if test.method == http.MethodPut {
				body, err = os.ReadFile("testdata/topology-v1.json")
				if err != nil {
					t.Fatalf("read fixture: %v", err)
				}
			}

			// When
			response := sendRequest(t, server, test.method, TopologyPath, body)

			// Then
			if response.StatusCode != test.wantStatus {
				t.Fatalf("response status = %d, want %d: %s", response.StatusCode, test.wantStatus, response.Body)
			}
			var apiError errorResponse
			decodeResponse(t, response.Body, &apiError)
			if apiError.Error.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", apiError.Error.Code, test.wantCode)
			}
			if _, err := repository.Checksum(context.Background()); !errors.Is(err, orchestrator.ErrTopologyNotFound) {
				t.Fatalf("checksum after denied request error = %v, want ErrTopologyNotFound", err)
			}
		})
	}
}

func TestHandler_authenticates_group_item_before_rejecting_malformed_path(t *testing.T) {
	// Given
	repository, baseServer := newTestHTTPServer(t)
	baseServer.Close()
	handler, err := New(repository, denyAccess{permission: PermissionRead, err: ErrAuthenticationRequired})
	if err != nil {
		t.Fatalf("New handler: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, OrchestrationGroupsPath+"/bad/name", nil)
	response := httptest.NewRecorder()

	// When
	handler.handleGroup(response, request)

	// Then
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("malformed path status = %d, want 401: %s", response.Code, response.Body)
	}
}

func TestNew_rejects_typed_nil_access_controller(t *testing.T) {
	// Given
	repository, server := newTestHTTPServer(t)
	server.Close()
	var access *denyAccess

	// When
	_, err := New(repository, access)

	// Then
	if !errors.Is(err, ErrAccessControllerUnavailable) {
		t.Fatalf("New error = %v, want ErrAccessControllerUnavailable", err)
	}

	var mapController mapAccess
	_, err = New(repository, mapController)
	if !errors.Is(err, ErrAccessControllerUnavailable) {
		t.Fatalf("New map controller error = %v, want ErrAccessControllerUnavailable", err)
	}
}

type mapAccess map[string]bool

func (mapAccess) Authorize(*http.Request, Permission) error {
	return nil
}

type denyAccess struct {
	permission Permission
	err        error
}

func (access denyAccess) Authorize(_ *http.Request, permission Permission) error {
	if permission == access.permission {
		return access.err
	}
	return nil
}
