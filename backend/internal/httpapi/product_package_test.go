package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
)

func TestGatewayProfileOwnsEveryDeclaredDesiredResourceAndNoOrchestratorInternals(t *testing.T) {
	profile := product.Gateway()
	for resourceType := range desiredResourceDefs {
		if !resourceAllowed(profile, resourceType) {
			t.Errorf("Gateway profile does not allow declared desired resource %q", resourceType)
		}
	}
	if !resourceAllowed(profile, "proxy_egress") {
		t.Error("Gateway profile does not allow product-owned proxy_egress resource")
	}
	for _, resourceType := range []string{"orchestrator_topology", "orchestrator_policy", "orchestrator_service_chain_intent", "unknown_resource"} {
		if resourceAllowed(profile, resourceType) {
			t.Errorf("Gateway import allows forbidden resource %q", resourceType)
		}
	}
}

func TestProductConfigExportBindsManifestAndPayloadHash(t *testing.T) {
	// Given
	server, _, cookie := productTestServer(t, product.Gateway())

	// When
	exported := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/config/export", "", cookie)

	// Then
	if exported.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", exported.Code, exported.Body.String())
	}
	var body struct {
		PackageManifest ConfigPackageManifest `json:"package_manifest"`
		Payload         ConfigPackagePayload  `json:"payload"`
	}
	decode(t, exported, &body)
	if body.PackageManifest.Product != product.Gateway().ID() || body.Payload.Product != product.Gateway().ID() {
		t.Fatalf("export products manifest=%q payload=%q, want gateway", body.PackageManifest.Product, body.Payload.Product)
	}
	if body.PackageManifest.PackageHash != hashConfigPayload(body.Payload) {
		t.Fatalf("manifest hash = %q, want product-bound payload hash", body.PackageManifest.PackageHash)
	}
	tampered := body.Payload
	tampered.Product = product.Orchestrator().ID()
	if hashConfigPayload(tampered) == body.PackageManifest.PackageHash {
		t.Fatal("changing product did not invalidate package hash")
	}
}

func TestConfigPackageHashIsStableAcrossBrowserJSONNormalization(t *testing.T) {
	payload := ConfigPackagePayload{
		SchemaVersion: ConfigPackageSchemaVersion,
		ContentType:   configContentType,
		Product:       product.Gateway().ID(),
		DeviceMode:    "gateway",
		Resources: map[string][]json.RawMessage{
			"wan_link": {json.RawMessage(`{ "name": "WAN", "id": "wan-1", "enabled": true }`)},
		},
	}
	browserNormalized := payload
	browserNormalized.Resources = map[string][]json.RawMessage{
		"wan_link": {json.RawMessage(`{"enabled":true,"id":"wan-1","name":"WAN"}`)},
	}
	if hashConfigPayload(payload) != hashConfigPayload(browserNormalized) {
		t.Fatalf("hash must not depend on browser JSON formatting: %q != %q", hashConfigPayload(payload), hashConfigPayload(browserNormalized))
	}
}

func TestProductConfigImportAcceptsItsOwnExport(t *testing.T) {
	server, _, cookie := productTestServer(t, product.Gateway())
	exported := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/config/export", "", cookie)
	if exported.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", exported.Code, exported.Body.String())
	}
	var body struct {
		PackageManifest ConfigPackageManifest `json:"package_manifest"`
		Payload         ConfigPackagePayload  `json:"payload"`
	}
	decode(t, exported, &body)
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", configImportJSON(t, ConfigImportRequest{
		DryRun:          true,
		PackageManifest: body.PackageManifest,
		Payload:         body.Payload,
	}), cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"dry_run"`) {
		t.Fatalf("self-export import dry-run = %d %s", response.Code, response.Body.String())
	}
}

func TestProductConfigImportAcceptsExportedRedactionMetadataDuringDryRun(t *testing.T) {
	server, _, cookie := productTestServer(t, product.Gateway())
	payload := ConfigPackagePayload{
		SchemaVersion: ConfigPackageSchemaVersion,
		ContentType:   configContentType,
		Product:       product.Gateway().ID(),
		DeviceMode:    "gateway",
		Resources: map[string][]json.RawMessage{
			"wan_link": {json.RawMessage(`{"id":"wan-pppoe","credential_ref":"local-secret:pppoe:wan-pppoe","pppoe_password_redacted":"redacted"}`)},
			"proxy_node": {json.RawMessage(`{"id":"proxy-node","credential_ref":"local-secret:proxy_node:proxy-node:secret","secret_redacted":"redacted","uri_redacted":"redacted"}`)},
		},
	}

	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", configImportJSON(t, ConfigImportRequest{
		DryRun:          true,
		PackageManifest: manifestForPayload(payload),
		Payload:         payload,
	}), cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"dry_run"`) {
		t.Fatalf("redacted config import dry-run = %d %s", response.Code, response.Body.String())
	}
}

func TestProductConfigImportRejectsCrossProductDuringDryRunWithoutWrites(t *testing.T) {
	// Given
	server, store, cookie := productTestServer(t, product.Orchestrator())
	payload := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: product.Gateway().ID(), DeviceMode: "gateway", Resources: map[string][]json.RawMessage{}}
	requestBody := configImportJSON(t, ConfigImportRequest{DryRun: true, PackageManifest: manifestForPayload(payload), Payload: payload})

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", requestBody, cookie)

	// Then
	if response.Code != http.StatusUnprocessableEntity || !containsAll(response.Body.String(), "product_mismatch", "gateway", "orchestrator") {
		t.Fatalf("cross-product dry-run status=%d body=%s", response.Code, response.Body.String())
	}
	assertNoConfigOrSnapshots(t, store)
}

func TestProductConfigImportReportsMismatchBeforeInspectingPayloadContents(t *testing.T) {
	// Given
	server, store, cookie := productTestServer(t, product.Orchestrator())
	payload := ConfigPackagePayload{
		SchemaVersion: ConfigPackageSchemaVersion,
		ContentType:   configContentType,
		Product:       product.Gateway().ID(),
		DeviceMode:    "gateway",
		Resources: map[string][]json.RawMessage{
			"wan_link": {json.RawMessage(`{"id":"gateway-only","password":"secret"}`)},
		},
	}
	requestBody := configImportJSON(t, ConfigImportRequest{DryRun: true, PackageManifest: manifestForPayload(payload), Payload: payload})

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", requestBody, cookie)

	// Then
	if response.Code != http.StatusUnprocessableEntity || !containsAll(response.Body.String(), "product_mismatch", "gateway", "orchestrator") {
		t.Fatalf("cross-product preflight status=%d body=%s", response.Code, response.Body.String())
	}
	assertNoConfigOrSnapshots(t, store)
}

func TestProductConfigImportRejectsForgedOrchestratorGatewayResourceWithoutWrites(t *testing.T) {
	// Given
	server, store, cookie := productTestServer(t, product.Orchestrator())
	payload := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: product.Orchestrator().ID(), DeviceMode: "orchestrator", Resources: map[string][]json.RawMessage{"wan_link": {json.RawMessage(`{"id":"forged-wan"}`)}}}
	requestBody := configImportJSON(t, ConfigImportRequest{DryRun: true, PackageManifest: manifestForPayload(payload), Payload: payload})

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", requestBody, cookie)

	// Then
	if response.Code != http.StatusUnprocessableEntity || !containsAll(response.Body.String(), "invalid_import_package", "wan_link") {
		t.Fatalf("forged resource status=%d body=%s", response.Code, response.Body.String())
	}
	assertNoConfigOrSnapshots(t, store)
}

func TestProductConfigImportRejectsMissingMalformedAndUnknownProduct(t *testing.T) {
	// Given
	server, store, cookie := productTestServer(t, product.Gateway())
	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"dry_run":true,"package_manifest":{"schema_version":1,"content_type":"local_desired_config","product":"gateway","package_hash":"sha256:x"},"payload":{"schema_version":1,"content_type":"local_desired_config","resources":{}}}`},
		{name: "unknown", body: `{"dry_run":true,"package_manifest":{"schema_version":1,"content_type":"local_desired_config","product":"gateway","package_hash":"sha256:x"},"payload":{"schema_version":1,"content_type":"local_desired_config","product":"bridge","resources":{}}}`},
		{name: "unknown field", body: `{"dry_run":true,"package_manifest":{"schema_version":1,"content_type":"local_desired_config","product":"gateway","package_hash":"sha256:x","extra":true},"payload":{"schema_version":1,"content_type":"local_desired_config","product":"gateway","resources":{}}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", test.body, cookie)

			// Then
			if response.Code != http.StatusBadRequest && response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	assertNoConfigOrSnapshots(t, store)
}

func TestProductSnapshotContainsProductAndRejectsCrossProductRestoreWithoutWrites(t *testing.T) {
	// Given
	gateway, gatewayStore, gatewayCookie := productTestServer(t, product.Gateway())
	created := authenticatedJSONRequest(t, gateway, http.MethodPost, "/api/v1/config/snapshots", `{"name":"gateway-product"}`, gatewayCookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create Gateway snapshot status=%d body=%s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Snapshot struct {
			ID string `json:"id"`
		} `json:"snapshot"`
	}
	decode(t, created, &createdBody)
	snapshot, err := gatewayStore.RuntimeSnapshot(context.Background(), createdBody.Snapshot.ID)
	if err != nil {
		t.Fatalf("load Gateway snapshot: %v", err)
	}
	var payload ConfigPackagePayload
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		t.Fatalf("decode Gateway snapshot: %v", err)
	}
	if payload.Product != product.Gateway().ID() {
		t.Fatalf("snapshot product = %q, want gateway", payload.Product)
	}
	orchestrator, orchestratorStore, orchestratorCookie := productTestServer(t, product.Orchestrator())
	copied := snapshot
	copied.ID = "copied-gateway"
	if err := orchestratorStore.SaveRuntimeSnapshot(context.Background(), copied); err != nil {
		t.Fatalf("copy snapshot fixture: %v", err)
	}
	before, err := orchestratorStore.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list snapshots before restore: %v", err)
	}

	// When
	response := authenticatedJSONRequest(t, orchestrator, http.MethodPost, "/api/v1/config/snapshots/copied-gateway/restore", `{}`, orchestratorCookie)

	// Then
	if response.Code != http.StatusUnprocessableEntity || !containsAll(response.Body.String(), "product_mismatch", "gateway", "orchestrator") {
		t.Fatalf("cross-product restore status=%d body=%s", response.Code, response.Body.String())
	}
	after, err := orchestratorStore.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list snapshots after restore: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("snapshot count changed from %d to %d", len(before), len(after))
	}
	configs, err := orchestratorStore.Configs(context.Background(), "wan_link")
	if !errors.Is(err, persistence.ErrProductResource) || len(configs) != 0 {
		t.Fatalf("Orchestrator Gateway-only configs after rejected restore = %#v err=%v, want ErrProductResource", configs, err)
	}
}

func TestProductSnapshotRestoreRejectsTamperedPayloadHashWithoutWrites(t *testing.T) {
	// Given
	server, store, cookie := productTestServer(t, product.Gateway())
	payload := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: product.Gateway().ID(), DeviceMode: "gateway", Resources: map[string][]json.RawMessage{}}
	_, hash, err := persistence.MarshalPayload(payload)
	if err != nil {
		t.Fatalf("marshal snapshot fixture: %v", err)
	}
	tampered := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: product.Orchestrator().ID(), DeviceMode: "orchestrator", Resources: map[string][]json.RawMessage{}}
	tamperedRaw, _, err := persistence.MarshalPayload(tampered)
	if err != nil {
		t.Fatalf("marshal tampered snapshot: %v", err)
	}
	if err := store.SaveRuntimeSnapshot(context.Background(), persistence.RuntimeSnapshot{ID: "tampered", SourceTransactionID: "fixture", Payload: tamperedRaw, PayloadHash: hash, CreatedAt: time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("save tampered snapshot: %v", err)
	}
	before, err := store.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list snapshots before restore: %v", err)
	}

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/snapshots/tampered/restore", `{}`, cookie)

	// Then
	if response.Code != http.StatusUnprocessableEntity || !containsAll(response.Body.String(), "invalid_snapshot", "integrity") {
		t.Fatalf("tampered restore status=%d body=%s", response.Code, response.Body.String())
	}
	after, err := store.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list snapshots after restore: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("snapshot count changed from %d to %d", len(before), len(after))
	}
	assertNoConfigs(t, store, "wan_link")
}

func TestProductOrchestratorSnapshotRestoreDoesNotExposeGatewayOnlyRows(t *testing.T) {
	// Given
	ctx := context.Background()
	server, store, cookie := productTestServer(t, product.Orchestrator())
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/snapshots", `{"name":"orchestrator-scope"}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create Orchestrator snapshot status=%d body=%s", created.Code, created.Body.String())
	}
	var body struct {
		Snapshot struct {
			ID string `json:"id"`
		} `json:"snapshot"`
	}
	decode(t, created, &body)

	// When
	restored := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/snapshots/"+body.Snapshot.ID+"/restore", `{}`, cookie)

	// Then
	if restored.Code != http.StatusOK {
		t.Fatalf("restore Orchestrator snapshot status=%d body=%s", restored.Code, restored.Body.String())
	}
	if _, err := store.Config(ctx, "wan_link", "hidden-gateway-wan"); !errors.Is(err, persistence.ErrProductResource) {
		t.Fatalf("Gateway-only row lookup error = %v, want ErrProductResource", err)
	}
}

func productTestServer(t *testing.T, profile product.Profile) (*Server, *persistence.Store, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	store, err := persistence.OpenForProduct(ctx, "file:"+newRequestID()+"?mode=memory&cache=shared", profile.ID())
	if err != nil {
		t.Fatalf("open %s store: %v", profile.ID(), err)
	}
	t.Cleanup(func() { _ = store.Close() })
	selection := product.NewSelection()
	if err := selection.Initialize(profile); err != nil {
		t.Fatalf("initialize %s selection: %v", profile.ID(), err)
	}
	server, err := NewServer(selection, WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(func() time.Time { return time.Date(2026, 7, 19, 3, 0, 0, 0, time.UTC) }))
	if err != nil {
		t.Fatalf("create %s server: %v", profile.ID(), err)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	return server, store, login.Result().Cookies()[0]
}

func configImportJSON(t *testing.T, request ConfigImportRequest) string {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal config import: %v", err)
	}
	return string(body)
}

func assertNoConfigOrSnapshots(t *testing.T, store *persistence.Store) {
	t.Helper()
	snapshots, err := store.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("snapshots = %#v, want none", snapshots)
	}
	assertNoConfigs(t, store, "wan_link")
}

func assertNoConfigs(t *testing.T, store *persistence.Store, resourceType string) {
	t.Helper()
	configs, err := store.Configs(context.Background(), resourceType)
	if errors.Is(err, persistence.ErrProductResource) {
		return
	}
	if err != nil {
		t.Fatalf("list %s configs: %v", resourceType, err)
	}
	if len(configs) != 0 {
		t.Fatalf("%s configs = %#v, want none", resourceType, configs)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
