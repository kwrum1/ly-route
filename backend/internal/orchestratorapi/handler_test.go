package orchestratorapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ly-route/backend/internal/orchestrator"
	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
)

func TestHandler_CRUD_readback_matches_versioned_fixture(t *testing.T) {
	// Given
	repository, server := newTestHTTPServer(t)
	fixture, err := os.ReadFile("testdata/topology-v1.json")
	if err != nil {
		t.Fatalf("read topology fixture: %v", err)
	}
	var want orchestrator.TopologyView
	if err := json.Unmarshal(fixture, &want); err != nil {
		t.Fatalf("decode topology fixture: %v", err)
	}

	// When
	put := sendRequest(t, server, http.MethodPut, TopologyPath, fixture)

	// Then
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT topology status = %d, want 200: %s", put.StatusCode, put.Body)
	}
	get := sendRequest(t, server, http.MethodGet, TopologyPath, nil)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET topology status = %d, want 200: %s", get.StatusCode, get.Body)
	}
	artifactPath := filepath.Join(t.TempDir(), "topology-readback.json")
	if err := os.WriteFile(artifactPath, get.Body, 0o600); err != nil {
		t.Fatalf("capture topology readback artifact: %v", err)
	}
	t.Logf("captured real HTTP readback artifact at %s (test cleanup removes it)", artifactPath)
	var readback topologyResponse
	decodeResponse(t, get.Body, &readback)
	if !reflect.DeepEqual(readback.Item, want) {
		t.Fatalf("GET topology item = %#v, want fixture %#v", readback.Item, want)
	}
	checksum, err := repository.Checksum(context.Background())
	if err != nil || readback.Checksum != checksum {
		t.Fatalf("GET topology checksum = %q, repository = %q, %v", readback.Checksum, checksum, err)
	}

	list := sendRequest(t, server, http.MethodGet, OrchestrationGroupsPath, nil)
	if list.StatusCode != http.StatusOK {
		t.Fatalf("GET groups status = %d, want 200: %s", list.StatusCode, list.Body)
	}
	var groups groupsResponse
	decodeResponse(t, list.Body, &groups)
	if len(groups.Items) != 2 || groups.Items[0].Name != "inline-east" || groups.Items[1].Name != "inline-west" {
		t.Fatalf("GET groups = %#v, want deterministic east/west order", groups.Items)
	}

	update := []byte(`{"name":"inline-west","ports":[{"interface":"eth8","direction":"lan_facing"},{"interface":"eth9","direction":"wan_facing"}]}`)
	updated := sendRequest(t, server, http.MethodPut, OrchestrationGroupsPath+"/inline-west", update)
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("PUT group status = %d, want 200: %s", updated.StatusCode, updated.Body)
	}
	groupReadback := sendRequest(t, server, http.MethodGet, OrchestrationGroupsPath+"/inline-west", nil)
	if groupReadback.StatusCode != http.StatusOK || !bytes.Contains(groupReadback.Body, []byte(`"interface":"eth8"`)) {
		t.Fatalf("GET updated group = %d %s, want eth8 readback", groupReadback.StatusCode, groupReadback.Body)
	}
	deleted := sendRequest(t, server, http.MethodDelete, OrchestrationGroupsPath+"/inline-west", nil)
	if deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE group status = %d, want 204: %s", deleted.StatusCode, deleted.Body)
	}
	created := sendRequest(t, server, http.MethodPost, OrchestrationGroupsPath, []byte(`{"name":"inline-north","ports":[{"interface":"eth6","direction":"lan_facing"},{"interface":"eth7","direction":"wan_facing"}]}`))
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("POST group status = %d, want 201: %s", created.StatusCode, created.Body)
	}

	cleanup := sendRequest(t, server, http.MethodDelete, TopologyPath, nil)
	if cleanup.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE topology status = %d, want 204: %s", cleanup.StatusCode, cleanup.Body)
	}
	missing := sendRequest(t, server, http.MethodGet, TopologyPath, nil)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("GET deleted topology status = %d, want 404: %s", missing.StatusCode, missing.Body)
	}
}

func TestHandler_managementSharingResolverControlsLANReuse(t *testing.T) {
	_, handler, server := newTestHTTPServerWithHandler(t)
	body := []byte(`{"schema_version":1,"management_interface":"eth0","management_shared":false,"interfaces":[{"name":"lan","role":"lan","port":"eth0"},{"name":"wan","role":"wan","port":"eth1"}],"orchestration_groups":[]}`)

	rejected := sendRequest(t, server, http.MethodPut, TopologyPath, body)
	if rejected.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("exclusive shared-LAN status = %d, want 422: %s", rejected.StatusCode, rejected.Body)
	}

	handler.SetManagementSharingResolver(func(context.Context, string) bool { return true })
	accepted := sendRequest(t, server, http.MethodPut, TopologyPath, body)
	if accepted.StatusCode != http.StatusOK {
		t.Fatalf("shared-LAN status = %d, want 200: %s", accepted.StatusCode, accepted.Body)
	}
	if !bytes.Contains(accepted.Body, []byte(`"management_shared":true`)) {
		t.Fatalf("shared-LAN readback = %s", accepted.Body)
	}
}

func TestHandler_rejects_group_creation_before_topology(t *testing.T) {
	// Given
	repository, server := newTestHTTPServer(t)
	body := []byte(`{"name":"inline-east","ports":[{"interface":"eth1","direction":"lan_facing"},{"interface":"eth2","direction":"wan_facing"}]}`)

	// When
	response := sendRequest(t, server, http.MethodPost, OrchestrationGroupsPath, body)

	// Then
	if response.StatusCode != http.StatusConflict || !bytes.Contains(response.Body, []byte(`"code":"topology_not_found"`)) {
		t.Fatalf("POST premature group = %d %s, want 409 topology_not_found", response.StatusCode, response.Body)
	}
	if _, err := repository.Checksum(context.Background()); err != orchestrator.ErrTopologyNotFound {
		t.Fatalf("Checksum after premature group error = %v, want ErrTopologyNotFound", err)
	}
}

func TestHandler_rejects_backslash_group_path_after_authorization(t *testing.T) {
	// Given
	_, server := newTestHTTPServer(t)

	// When
	response := sendRequest(t, server, http.MethodGet, OrchestrationGroupsPath+`/bad%5Cname`, nil)

	// Then
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET backslash group path = %d %s, want 404", response.StatusCode, response.Body)
	}
	var apiError errorResponse
	decodeResponse(t, response.Body, &apiError)
	if apiError.Error.Code != "group_not_found" {
		t.Fatalf("GET backslash group code = %q, want group_not_found", apiError.Error.Code)
	}
}

type recordedResponse struct {
	StatusCode int
	Body       []byte
}

func newTestHTTPServer(t *testing.T, runtimes ...ServiceChainRuntime) (*orchestrator.Repository, *httptest.Server) {
	repository, _, server := newTestHTTPServerWithHandler(t, runtimes...)
	return repository, server
}

func newTestHTTPServerWithHandler(t *testing.T, runtimes ...ServiceChainRuntime) (*orchestrator.Repository, *Handler, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	store, err := persistence.OpenForProduct(ctx, filepath.Join(t.TempDir(), "orchestrator.db"), product.Orchestrator().ID())
	if err != nil {
		t.Fatalf("OpenForProduct: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository, err := orchestrator.NewRepository(store, orchestrator.RepositoryOptions{Now: func() time.Time {
		return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	handler, err := New(repository, allowAccess{}, runtimes...)
	if err != nil {
		t.Fatalf("New handler: %v", err)
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return repository, handler, server
}

type allowAccess struct{}

func (allowAccess) Authorize(*http.Request, Permission) error {
	return nil
}

func sendRequest(t *testing.T, server *httptest.Server, method, path string, body []byte) recordedResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do request: %v", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return recordedResponse{StatusCode: response.StatusCode, Body: payload}
}

func decodeResponse(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode response %s: %v", body, err)
	}
}
