package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
)

func TestConfigExportCharacterizationIncludesGatewayDesiredResources(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-product-export-characterization?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveConfig(ctx, configDocument(t, "wan_link", "wan-characterized", map[string]any{"id": "wan-characterized", "enabled": true}, time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC))); err != nil {
		t.Fatalf("save desired config: %v", err)
	}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}

	// When
	exported := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/config/export", "", login.Result().Cookies()[0])

	// Then
	if exported.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", exported.Code, exported.Body.String())
	}
	var body struct {
		Payload struct {
			DeviceMode string                     `json:"device_mode"`
			Resources  map[string]json.RawMessage `json:"resources"`
		} `json:"payload"`
	}
	decode(t, exported, &body)
	if body.Payload.DeviceMode != "gateway" || !json.Valid(body.Payload.Resources["wan_link"]) || !containsJSONID(body.Payload.Resources["wan_link"], "wan-characterized") {
		t.Fatalf("export payload = %#v, want Gateway desired WAN", body.Payload)
	}
}

func TestSnapshotCharacterizationStoresExportedGatewayPayloadAndHash(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-product-snapshot-characterization?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveConfig(ctx, configDocument(t, "wan_link", "wan-snapshot", map[string]any{"id": "wan-snapshot"}, time.Date(2026, 7, 19, 2, 30, 0, 0, time.UTC))); err != nil {
		t.Fatalf("save desired config: %v", err)
	}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}

	// When
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/snapshots", `{"name":"characterized"}`, login.Result().Cookies()[0])

	// Then
	if created.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d: %s", created.Code, created.Body.String())
	}
	var response struct {
		Snapshot struct {
			ID string `json:"id"`
		} `json:"snapshot"`
	}
	decode(t, created, &response)
	snapshot, err := store.RuntimeSnapshot(ctx, response.Snapshot.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if hashObject(json.RawMessage(snapshot.Payload)) != "sha256:"+snapshot.PayloadHash {
		t.Fatalf("snapshot hash = %q, want payload digest", snapshot.PayloadHash)
	}
	var payload struct {
		DeviceMode string                     `json:"device_mode"`
		Resources  map[string]json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		t.Fatalf("decode snapshot payload: %v", err)
	}
	if payload.DeviceMode != "gateway" || !containsJSONID(payload.Resources["wan_link"], "wan-snapshot") {
		t.Fatalf("snapshot payload = %#v, want exported Gateway desired WAN", payload)
	}
}

func containsJSONID(raw json.RawMessage, id string) bool {
	var items []struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(raw, &items) == nil && len(items) == 1 && items[0].ID == id
}
