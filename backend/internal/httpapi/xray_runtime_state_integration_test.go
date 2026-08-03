package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestXrayStatusIntegration(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LY_ROUTE_XRAY_BALANCER_API_BINARY"))
	if binary == "" {
		t.Skip("LY_ROUTE_XRAY_BALANCER_API_BINARY is not set")
	}
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:xray-status-integration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveConfig(ctx, configDocument(t, "proxy_subscription", "main", map[string]any{"id": "main", "enabled": true, "selection": "fastest", "node_refs": []string{"a", "b"}}, fixedClock()())); err != nil {
		t.Fatal(err)
	}
	controller := &xrayStatusIntegrationController{binary: binary}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/xray/status", "", login.Result().Cookies()[0])
	body := status.Body.String()
	if os.Getenv("LY_ROUTE_XRAY_EXPECT_UNAVAILABLE") == "1" {
		if status.Code != http.StatusOK || !strings.Contains(body, `"available":false`) || !strings.Contains(body, `"state":"degraded"`) || !strings.Contains(body, "no healthy selected outbound") || strings.Contains(body, `"live_verified":true`) {
			t.Fatalf("failed live Xray status was not degraded: %d %s", status.Code, body)
		}
		return
	}
	expected := strings.TrimSpace(os.Getenv("LY_ROUTE_XRAY_EXPECT_SELECTED"))
	expectedNode := strings.TrimPrefix(expected, "subscription-main-node-")
	if status.Code != http.StatusOK || !strings.Contains(body, `"available":true`) || !strings.Contains(body, `"selected_node_ids":["`+expectedNode+`"]`) || !strings.Contains(body, `"live_verified":true`) {
		t.Fatalf("live Xray status did not expose selected node %q: %d %s", expectedNode, status.Code, body)
	}
}

type xrayStatusIntegrationController struct{ binary string }

func (controller *xrayStatusIntegrationController) ReloadOrRestart(context.Context, serviceRuntime.ServiceName, []serviceRuntime.RenderedArtifact) error {
	return errors.New("restart is not used by Xray status integration")
}

func (controller *xrayStatusIntegrationController) Status(context.Context, serviceRuntime.ServiceName) (serviceRuntime.Health, error) {
	return serviceRuntime.Health{Service: serviceRuntime.Xray, Available: true}, nil
}

func (controller *xrayStatusIntegrationController) Rollback(context.Context, serviceRuntime.ServiceName, []serviceRuntime.RenderedArtifact) error {
	return errors.New("rollback is not used by Xray status integration")
}

func (controller *xrayStatusIntegrationController) XrayBalancerStates(ctx context.Context, tags []string) ([]serviceRuntime.XrayBalancerState, error) {
	return (serviceRuntime.FilesystemController{Runner: xrayStatusCommandRunner{binary: controller.binary}}).XrayBalancerStates(ctx, tags)
}

type xrayStatusCommandRunner struct{ binary string }

func (runner xrayStatusCommandRunner) Run(context.Context, string, ...string) error {
	return errors.New("run is not used by Xray status integration")
}

func (runner xrayStatusCommandRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	if name != "xray" {
		return "", errors.New("unexpected Xray status integration command")
	}
	output, err := exec.CommandContext(ctx, runner.binary, args...).CombinedOutput()
	return string(output), err
}

func (runner xrayStatusCommandRunner) Status(context.Context, serviceRuntime.ServiceName) (serviceRuntime.Health, error) {
	return serviceRuntime.Health{}, errors.New("runner status is not used by Xray status integration")
}
