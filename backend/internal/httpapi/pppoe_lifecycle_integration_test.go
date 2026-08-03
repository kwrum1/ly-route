package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestPPPoELifecycleHTTPIntegration(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("LY_ROUTE_PPPOE_LIFECYCLE_ROOT"))
	clientInterface := strings.TrimSpace(os.Getenv("LY_ROUTE_PPPOE_CLIENT_INTERFACE"))
	if root == "" || clientInterface == "" {
		t.Skip("PPPoE lifecycle integration environment is not configured")
	}
	runner := &pppoeIntegrationRunner{root: root, logPath: filepath.Join(root, "pppd-client.log")}
	t.Cleanup(func() { _ = runner.stop() })
	controller := serviceRuntime.FilesystemController{RootDir: root, Runner: runner}
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:pppoe-lifecycle-integration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(
		WithStore(store),
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	wan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-live","name":"PPPoE Live","enabled":true,"type":"pppoe","interface_id":"`+clientInterface+`","username":"subscriber-live","password":"live-secret","vpp_table_id":100}`, cookie)
	if wan.Code != http.StatusOK || strings.Contains(wan.Body.String(), "live-secret") {
		t.Fatalf("create PPPoE WAN failed or leaked its secret: %d %s", wan.Code, wan.Body.String())
	}
	if os.Getenv("LY_ROUTE_PPPOE_EXPECT_UNAVAILABLE") == "1" {
		connect := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/pppoe/connect", `{}`, cookie)
		if connect.Code != http.StatusServiceUnavailable || !strings.Contains(connect.Body.String(), "pppoe_lifecycle_failed") || strings.Contains(connect.Body.String(), "live-secret") || strings.Contains(connect.Body.String(), `"route_ready":true`) {
			t.Fatalf("failed PPPoE dependency was not explicit and fail-closed: %d %s", connect.Code, connect.Body.String())
		}
		status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/pppoe/status", "", cookie)
		assertLivePPPoEResponse(t, status, "disconnected", false)
		if output, err := exec.Command("ip", "link", "show", "dev", "ppp-wan-live").CombinedOutput(); err == nil {
			t.Fatalf("failed PPPoE dependency retained a stale PPP interface: %s", output)
		}
		return
	}

	connect := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/pppoe/connect", `{}`, cookie)
	assertLivePPPoEResponse(t, connect, "connected", true)
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/pppoe/status", "", cookie)
	assertLivePPPoEResponse(t, status, "connected", true)

	disconnect := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/pppoe/disconnect", `{}`, cookie)
	assertLivePPPoEResponse(t, disconnect, "disconnected", false)
	if output, err := exec.Command("ip", "link", "show", "dev", "ppp-wan-live").CombinedOutput(); err == nil {
		t.Fatalf("PPP interface remained after verified disconnect: %s", output)
	}

	reconnect := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/pppoe/connect", `{}`, cookie)
	assertLivePPPoEResponse(t, reconnect, "connected", true)
}

func assertLivePPPoEResponse(t *testing.T, response *httptest.ResponseRecorder, state string, routeReady bool) {
	t.Helper()
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"state":"`+state+`"`) || strings.Contains(body, "live-secret") {
		t.Fatalf("PPPoE %s response = %d %s", state, response.Code, body)
	}
	if routeReady {
		for _, required := range []string{"10.67.0.", `"route_ready":true`, "vpp.fib.route"} {
			if !strings.Contains(body, required) {
				t.Fatalf("connected PPPoE response missing %q: %s", required, body)
			}
		}
	} else if strings.Contains(body, `"route_ready":true`) || strings.Contains(body, `"vpp_route_handoff":[{"`) {
		t.Fatalf("disconnected PPPoE response retained a route handoff: %s", body)
	}
}

type pppoeIntegrationRunner struct {
	mu      sync.Mutex
	root    string
	logPath string
	command *exec.Cmd
	log     *os.File
	address string
}

func (runner *pppoeIntegrationRunner) Run(_ context.Context, name string, args ...string) error {
	if name != "systemctl" || len(args) != 2 {
		return errors.New("PPPoE integration runner received an unsupported command")
	}
	switch args[0] {
	case "reload-or-restart", "restart":
		if err := runner.stop(); err != nil {
			return err
		}
		unit := strings.TrimSuffix(strings.TrimPrefix(args[1], "pppd@"), ".service")
		peer := filepath.Join(runner.root, "etc", "ppp", "peers", unit)
		for _, secrets := range []string{"pap-secrets", "chap-secrets"} {
			content, err := os.ReadFile(filepath.Join(runner.root, "etc", "ppp", secrets))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join("/etc/ppp", secrets), content, 0o600); err != nil {
				return err
			}
		}
		logFile, err := os.OpenFile(runner.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		command := exec.Command("pppd", "file", peer, "nodetach", "debug", "logfd", "2")
		command.Stdout, command.Stderr = logFile, logFile
		if err := command.Start(); err != nil {
			_ = logFile.Close()
			return err
		}
		if err := os.MkdirAll("/run/ly-route/pppoe", 0o755); err != nil {
			_ = command.Process.Kill()
			_ = logFile.Close()
			return err
		}
		if err := os.WriteFile("/run/ly-route/pppoe/wan-live.json", []byte(`{"state":"connected","interface":"ppp-wan-live","session":{"local_address":"10.67.0.10"}}
`), 0o600); err != nil {
			_ = command.Process.Kill()
			_ = logFile.Close()
			return err
		}
		runner.mu.Lock()
		runner.command, runner.log = command, logFile
		runner.mu.Unlock()
		return nil
	case "stop":
		return runner.stop()
	default:
		return errors.New("PPPoE integration runner received an unsupported systemctl action")
	}
}

func (runner *pppoeIntegrationRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	// The lifecycle test runs a real PPPoE client in the CPE namespace. Its
	// VPP control socket is intentionally replaced by deterministic readback
	// for this API test; native VPP packet/session coverage is exercised by the
	// dedicated VPP netns acceptance test.
	runner.mu.Lock()
	connected := runner.command != nil
	runner.mu.Unlock()
	if connected && name == "cat" && len(args) == 1 && strings.Contains(args[0], "/run/ly-route/pppoe/") {
		addresses, err := exec.CommandContext(ctx, "ip", "-o", "-4", "address", "show", "dev", "ppp-wan-live").CombinedOutput()
		if err != nil {
			return "", errors.New("real PPPoE client has not completed IPCP")
		}
		fields := strings.Fields(string(addresses))
		address := ""
		for index, field := range fields {
			if field == "inet" && index+1 < len(fields) {
				address = strings.SplitN(fields[index+1], "/", 2)[0]
				break
			}
		}
		if address == "" {
			return "", errors.New("real PPPoE client has not completed IPCP")
		}
		runner.mu.Lock()
		runner.address = address
		runner.mu.Unlock()
		return `{"state":"connected","interface":"ppp-wan-live","session":{"local_address":"` + address + `"}}
`, nil
	}
	if connected && name == "vppctl" && len(args) >= 2 && args[0] == "show" && args[1] == "pppoe" {
		runner.mu.Lock()
		address := runner.address
		runner.mu.Unlock()
		return "PPPoE session local " + address + "\n", nil
	}
	if connected && name == "vppctl" && len(args) >= 3 && args[0] == "show" && args[1] == "interface" && args[2] == "address" {
		runner.mu.Lock()
		address := runner.address
		runner.mu.Unlock()
		return "pppoe_session0 " + address + "/32\n", nil
	}
	if !connected && name == "vppctl" && len(args) >= 2 && args[0] == "show" && args[1] == "pppoe" {
		return "No pppoe sessions configured\n", nil
	}
	if !connected && name == "vppctl" && len(args) >= 3 && args[0] == "show" && args[1] == "interface" && args[2] == "address" {
		return "", nil
	}
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(output), err
}

func (runner *pppoeIntegrationRunner) Status(_ context.Context, service serviceRuntime.ServiceName) (serviceRuntime.Health, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	available := runner.command != nil && runner.command.Process != nil && runner.command.Process.Signal(syscall.Signal(0)) == nil
	return serviceRuntime.Health{Service: service, Available: available}, nil
}

func (runner *pppoeIntegrationRunner) stop() error {
	runner.mu.Lock()
	command, logFile := runner.command, runner.log
	runner.command, runner.log, runner.address = nil, nil, ""
	runner.mu.Unlock()
	_ = os.WriteFile("/run/ly-route/pppoe/wan-live.json", []byte(`{"state":"disconnected"}
`), 0o600)
	if command == nil || command.Process == nil {
		return nil
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = command.Process.Kill()
		<-done
	}
	if logFile != nil {
		_ = logFile.Close()
	}
	return nil
}
