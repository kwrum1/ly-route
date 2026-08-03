package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	controlapi "ly-route/backend/internal/api"
	"ly-route/backend/internal/product"
)

func TestProductRouteReturnsActiveTypedProfile(t *testing.T) {
	t.Parallel()

	for _, profile := range []product.Profile{product.Gateway(), product.Orchestrator()} {
		profile := profile
		t.Run(profile.ID().String(), func(t *testing.T) {
			t.Parallel()

			// Given
			server := newServerForProfile(t, profile)

			// When
			response := request(t, server, http.MethodGet, "/api/v1/product")

			// Then
			if response.Code != http.StatusOK {
				t.Fatalf("product status = %d, want 200: %s", response.Code, response.Body.String())
			}
			var body controlapi.ProductProfile
			decode(t, response, &body)
			if body.Product != profile.ID() {
				t.Fatalf("product = %q, want %q", body.Product, profile.ID())
			}
			if !slices.Equal(body.Capabilities, profile.Capabilities()) {
				t.Fatalf("capabilities = %#v, want %#v", body.Capabilities, profile.Capabilities())
			}
		})
	}
}

func TestGatewayProfileAllowsEveryRegisteredProductRoute(t *testing.T) {
	t.Parallel()

	// Given
	profile := product.Gateway()

	// When / Then
	for _, route := range productRoutes() {
		if !profile.Allows(route.capability) {
			t.Fatalf("Gateway profile does not allow %s for %s", route.capability, route.pattern)
		}
	}
}

func TestOrchestratorRejectsGatewayOnlyRoutesWithTypedCapabilityError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		capability product.Capability
	}{
		{name: "WAN group", path: "/api/v1/gateway/wan-groups", capability: product.CapabilityGatewayWAN},
		{name: "NAT", path: "/api/v1/gateway/nat/static", capability: product.CapabilityGatewayNAT},
		{name: "port map", path: "/api/v1/gateway/nat/port-maps/example", capability: product.CapabilityGatewayNAT},
		{name: "DNS", path: "/api/v1/dns/policies", capability: product.CapabilityDNS},
		{name: "DHCP", path: "/api/v1/dhcp/servers", capability: product.CapabilityDHCP},
		{name: "Top Domains", path: "/api/v1/telemetry/top-domains", capability: product.CapabilityTopDomains},
		{name: "Gateway object-group alias", path: "/api/v1/objects/groups", capability: product.CapabilityGatewayRouting},
		{name: "proxy", path: "/api/v1/proxy/egresses", capability: product.CapabilityProxy},
		{name: "Gateway traffic control alias", path: "/api/v1/gateway/traffic-control", capability: product.CapabilityGatewayRouting},
		{name: "Gateway Smart QoS", path: "/api/v1/flow-control/smart-qos", capability: product.CapabilityGatewayRouting},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// Given
			server := newServerForProfile(t, product.Orchestrator())

			// When
			response := request(t, server, http.MethodGet, test.path)

			// Then
			if response.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want 404: %s", test.path, response.Code, response.Body.String())
			}
			var body ErrorResponse
			decode(t, response, &body)
			if body.Error.Code != "capability_not_supported" || body.Error.Capability != test.capability {
				t.Fatalf("GET %s error = %#v, want capability_not_supported/%s", test.path, body.Error, test.capability)
			}
		})
	}
}

func TestOrchestratorRegistersSharedRuntimeTelemetryAndConfigRoutes(t *testing.T) {
	t.Parallel()

	// Given
	server := newServerForProfile(t, product.Orchestrator())
	paths := []string{
		"/api/v1/product",
		"/api/v1/health",
		"/api/v1/auth/session",
		"/api/v1/management/network",
		"/api/v1/interfaces",
		"/api/v1/objects/ip-groups",
		"/api/v1/security/acls",
		"/api/v1/flow-control/runtime",
		"/api/v1/flow-control/policies",
		"/api/v1/runtime/status",
		"/api/v1/config/export",
		"/api/v1/dashboard/summary",
		"/api/v1/telemetry/top-sessions",
	}

	for _, path := range paths {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			// When
			response := request(t, server, http.MethodGet, path)

			// Then
			var body ErrorResponse
			if response.Code == http.StatusNotFound {
				decode(t, response, &body)
				if body.Error.Code == "capability_not_supported" || body.Error.Message == "unknown API route" {
					t.Fatalf("GET %s was not registered: %#v", path, body.Error)
				}
			}
		})
	}
}

func TestOrchestratorSharedStatusExcludesGatewayOnlyServices(t *testing.T) {
	t.Parallel()

	// Given
	server := newServerForProfile(t, product.Orchestrator())

	for _, path := range []string{"/api/v1/health", "/api/v1/capabilities", "/api/v1/runtime/status"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			// When
			response := request(t, server, http.MethodGet, path)

			// Then
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200: %s", path, response.Code, response.Body.String())
			}
			for _, forbidden := range []string{"smartdns", "kea", "xray", "pppoe", "pppd", "linux_routing"} {
				if strings.Contains(response.Body.String(), `"name":"`+forbidden+`"`) {
					t.Fatalf("GET %s exposed Gateway-only service %q: %s", path, forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestOrchestratorObjectGroupsAreIPOnly(t *testing.T) {
	t.Parallel()

	server := newServerForProfile(t, product.Orchestrator())
	items, err := server.desiredItems(context.Background(), "object_group")
	if err != nil {
		t.Fatalf("read object groups: %v", err)
	}
	for _, item := range items {
		if kind := objectGroupKind(item); kind != "ip" {
			t.Fatalf("Orchestrator exposed object group kind %q: %#v", kind, item)
		}
	}
	if err := server.normalizeObjectGroupPayload(context.Background(), map[string]any{"id": "domain-1", "name": "blocked", "kind": "domain", "entries": []any{"example.com"}}); err == nil || !strings.Contains(err.Error(), "supports IP") {
		t.Fatalf("domain object group error = %v, want IP-only rejection", err)
	}
	payload := ConfigPackagePayload{
		SchemaVersion: ConfigPackageSchemaVersion,
		ContentType:   configContentType,
		Product:       product.Orchestrator().ID(),
		DeviceMode:    "orchestrator",
		Resources: map[string][]json.RawMessage{
			"object_group": {json.RawMessage(`{"id":"domain-1","name":"blocked","kind":"domain","entries":["example.com"]}`)},
		},
	}
	if _, err := configDocumentsFromImport(payload, time.Now(), product.Orchestrator()); err == nil || !strings.Contains(err.Error(), "only supports IP") {
		t.Fatalf("domain object config import error = %v, want IP-only rejection", err)
	}
}

func TestNewServerRejectsUninitializedProductSelection(t *testing.T) {
	t.Parallel()

	// Given
	selection := product.NewSelection()

	// When
	_, err := NewServer(selection)

	// Then
	if !errors.Is(err, product.ErrSelectionUninitialized) {
		t.Fatalf("NewServer error = %v, want ErrSelectionUninitialized", err)
	}
}

func newServerForProfile(t *testing.T, profile product.Profile) *Server {
	t.Helper()
	selection := product.NewSelection()
	if err := selection.Initialize(profile); err != nil {
		t.Fatalf("initialize product selection: %v", err)
	}
	server, err := NewServer(selection)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}
