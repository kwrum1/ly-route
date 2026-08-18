package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	controlapi "ly-route/backend/internal/api"
	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/dns"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "ly-route-vpp-proof-test-")
	if err != nil {
		panic(err)
	}
	proofPath := filepath.Join(tmp, "capabilities.json")
	proof := `{"management_interface":"eth0","proofs":[` + testProofs("eth1", "eth2", "eth3", "eth4", "enp2s0", "enp3s0", "enp4s0", "lan-bridge-1", "wan0", "wan1") + `]}`
	proof = strings.ReplaceAll(proof, `\"`, `"`)
	if err := os.WriteFile(proofPath, []byte(proof), 0600); err != nil {
		panic(err)
	}
	if err := os.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", proofPath); err != nil {
		panic(err)
	}
	if err := os.Setenv("LY_ROUTE_LAN_INTERFACE", "eth0"); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

func useVPPProof(t *testing.T, management string) {
	t.Helper()
	proofPath := filepath.Join(t.TempDir(), "capabilities.json")
	proof := `{"management_interface":` + fmt.Sprintf("%q", management) + `,"proofs":[` + testProofs("eth1", "eth2", "eth3", "eth4", "enp2s0", "enp3s0", "enp4s0", "lan0", "lan-bridge-1", "wan0", "wan1") + `]}`
	proof = strings.ReplaceAll(proof, `\"`, `"`)
	if err := os.WriteFile(proofPath, []byte(proof), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", proofPath)
}

func testProofs(interfaces ...string) string {
	items := make([]string, 0, len(interfaces))
	for _, name := range interfaces {
		items = append(items, fmt.Sprintf(`{"linux_interface":%q,"proof":{"hook":"af_xdp","mode":"zero_copy","source":"runtime_probe","runtime_verified":true,"native":true,"high_performance":true,"observed_at":"2026-06-06T11:59:00Z","valid_until":"2026-06-06T12:01:00Z"}}`, name))
	}
	return strings.Join(items, ",")
}

func TestHealthReturnsDependencyStatesAndRequestID(t *testing.T) {
	server := New(WithVersion("test-version"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set(HeaderRequestID, "req-test")
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	if got := res.Header().Get(HeaderRequestID); got != "req-test" {
		t.Fatalf("request header = %q, want req-test", got)
	}
	var body HealthResponse
	decode(t, res, &body)
	if body.Version != "test-version" || body.Status != "degraded" || !body.Degraded || body.RequestID != "req-test" {
		t.Fatalf("health body = %#v", body)
	}
	want := map[string]string{"vpp": "degraded", "smartdns": "degraded", "kea": "degraded", "xray": "degraded", "pppoe": "available", "persistence": "degraded"}
	for _, dep := range body.Dependencies {
		if dep.State != want[dep.Name] || (dep.State == controlapi.CapabilityDegraded && dep.Reason == "") {
			t.Fatalf("dependency = %#v, want explicit degraded capability", dep)
		}
		delete(want, dep.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing dependencies: %#v", want)
	}
}

func TestDashboardSummaryReportsSystemResourcesAndDependencies(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/dashboard/summary", "", login.Result().Cookies()[0])
	if res.Code != http.StatusOK {
		t.Fatalf("summary status = %d, want 200: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{"dependencies", "system", "cpu_busy_percent", "memory_total_bytes", "memory_used_percent", "system_summary"} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("dashboard summary missing %q: %s", required, res.Body.String())
		}
	}
}

func TestDNSPoliciesMergeByPriorityAndUseDedicatedDefaultMiss(t *testing.T) {
	store, err := persistence.Open(context.Background(), "file:dns-policy-merge-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	local := `{"id":"dns-cn","name":"国内","priority":10,"enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"cn-rule","domains":["cn.example"],"outcome":{"kind":"direct","upstream_id":"ali","wan_egress_id":"pppoe"}}]}}`
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", local, cookie); res.Code != http.StatusOK {
		t.Fatalf("local DNS policy status = %d: %s", res.Code, res.Body.String())
	}
	defaultPolicy := `{"id":"dns-default","name":"缺省","priority":100,"enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"direct","upstream_id":"cf","wan_egress_id":"proxy"},"rules":[]}}`
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", defaultPolicy, cookie); res.Code != http.StatusOK {
		t.Fatalf("default DNS policy status = %d: %s", res.Code, res.Body.String())
	}
	// A later client-specific policy defaults to reject for requests that
	// reach its own listener. It must not replace the global fallback used
	// by SmartDNS's shared listener.
	clientScoped := `{"id":"client-scoped","name":"client scoped","priority":320,"enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"client-rule","source_prefixes":["192.168.50.101/32"],"domains":["client.example"],"outcome":{"kind":"direct","upstream_id":"ali","wan_egress_id":"pppoe"}}]}}`
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", clientScoped, cookie); res.Code != http.StatusOK {
		t.Fatalf("client-scoped DNS policy status = %d: %s", res.Code, res.Body.String())
	}
	matched := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/dns/resolve?domain=cn.example", "", cookie)
	if matched.Code != http.StatusOK || !strings.Contains(matched.Body.String(), `"rule_id":"cn-rule"`) {
		t.Fatalf("domestic DNS decision = %d: %s", matched.Code, matched.Body.String())
	}
	miss := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/dns/resolve?domain=foreign.example", "", cookie)
	if miss.Code != http.StatusOK || !strings.Contains(miss.Body.String(), `"wan_egress_id":"proxy"`) {
		t.Fatalf("default DNS decision = %d: %s", miss.Code, miss.Body.String())
	}
}

func TestBuiltInRoutingGroupsMaterializeOnlyWhenReferenced(t *testing.T) {
	geodataDir := filepath.Join("..", "..", "..", "packaging", "geodata")
	if _, err := os.Stat(filepath.Join(geodataDir, "geoip.dat")); err != nil {
		t.Skip("routing geodata is not present in this checkout")
	}
	t.Setenv("LY_ROUTE_GEODATA_DIR", geodataDir)
	server := New()
	items, err := server.desiredItems(context.Background(), "object_group")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if stringField(item, "id") == "obj-geoip-cn" && len(objectGroupEntries(item)) != 0 {
			t.Fatal("built-in GeoIP group must remain lazy in the list representation")
		}
	}
	materialized, err := materializeObjectGroupItems(items, map[string]bool{"obj-geoip-cn": true, "obj-geosite-cn": true})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, item := range materialized {
		id := stringField(item, "id")
		if id == "obj-geoip-cn" || id == "obj-geosite-cn" {
			counts[id] = len(objectGroupEntries(item))
		}
	}
	if counts["obj-geoip-cn"] < 4000 || counts["obj-geosite-cn"] < 100000 {
		t.Fatalf("materialized built-in groups are unexpectedly small: %#v", counts)
	}
}

func TestHealthIgnoresOptionalUnavailableCapabilities(t *testing.T) {
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
		serviceRuntime.VPP:          {Service: serviceRuntime.VPP, Available: true},
		serviceRuntime.SmartDNS:     {Service: serviceRuntime.SmartDNS, Available: true},
		serviceRuntime.Kea:          {Service: serviceRuntime.Kea, Available: true},
		serviceRuntime.Xray:         {Service: serviceRuntime.Xray, Available: true},
		serviceRuntime.PPPd:         {Service: serviceRuntime.PPPd, Available: false, Reason: "pppoe not configured"},
		serviceRuntime.Nftables:     {Service: serviceRuntime.Nftables, Available: true},
		serviceRuntime.LinuxRouting: {Service: serviceRuntime.LinuxRouting, Available: true},
	}}
	store, err := persistence.Open(context.Background(), "file:httpapi-health-optional-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	res := request(t, server, http.MethodGet, "/api/v1/health")
	var body HealthResponse
	decode(t, res, &body)
	if body.Status != "ok" || body.Degraded {
		t.Fatalf("health body = %#v, want ok despite optional PPPoE", body)
	}
	if !strings.Contains(res.Body.String(), `"name":"pppoe"`) || strings.Contains(res.Body.String(), "transparent_proxy_handoff") {
		t.Fatalf("optional capability rendering unexpected: %s", res.Body.String())
	}
}

func TestTrafficTrendReportsSamplerDegradedWithoutSyntheticData(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/traffic-trend?window=24h&points=999", "", login.Result().Cookies()[0])
	if res.Code != http.StatusOK {
		t.Fatalf("traffic trend status = %d, want 200: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{"gateway_logical_egress", "gateway telemetry collector is not configured", `"points":288`, `"logical_egresses":[]`, `"state":"unavailable"`} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("traffic trend missing %q: %s", required, res.Body.String())
		}
	}
}

func TestTrafficTrendCollectorBacksTrendSeries(t *testing.T) {
	download := float64(1234)
	upload := float64(567)
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithTrafficTrend(fakeTrafficTrend{result: TrafficTrendResult{Series: TrafficTrendSets{LogicalEgresses: []LogicalEgressSeries{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Samples: []LogicalEgressSample{{Timestamp: time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC), DownloadBPS: &download, UploadBPS: &upload}}}}}}}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/traffic-trend?window=24h&points=12", "", login.Result().Cookies()[0])
	if res.Code != http.StatusOK {
		t.Fatalf("traffic trend status = %d, want 200: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{`"degraded":false`, `"points":12`, `"sampling_interval_seconds":300`, `"logical_egresses"`, `"download_bps":1234`, `"upload_bps":567`, `"state":"available"`} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("traffic trend collector response missing %q: %s", required, res.Body.String())
		}
	}
}

type fakeTrafficTrend struct {
	result TrafficTrendResult
}

func (collector fakeTrafficTrend) TrafficTrend(context.Context, TrafficTrendQuery) (TrafficTrendResult, error) {
	return collector.result, nil
}

func TestHealthUsesServiceRuntimeStatusWithoutClaimingVPPOrNftables(t *testing.T) {
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
		serviceRuntime.SmartDNS: {Service: serviceRuntime.SmartDNS, Available: true},
		serviceRuntime.Kea:      {Service: serviceRuntime.Kea, Available: true},
		serviceRuntime.Xray:     {Service: serviceRuntime.Xray, Available: true},
		serviceRuntime.PPPd:     {Service: serviceRuntime.PPPd, Available: false, Reason: "pppd inactive"},
	}}
	server := New(WithVersion("test-version"), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	res := request(t, server, http.MethodGet, "/api/v1/health")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	var body HealthResponse
	decode(t, res, &body)
	if body.Status != "degraded" || !body.Degraded {
		t.Fatalf("health body = %#v, want degraded because vpp is not wired and pppd is inactive", body)
	}
	states := map[string]controlapi.CapabilityState{}
	for _, dependency := range body.Dependencies {
		states[dependency.Name] = dependency
	}
	for _, name := range []string{"smartdns", "kea", "xray"} {
		if !states[name].Available || states[name].State != controlapi.CapabilityAvailable {
			t.Fatalf("%s capability = %#v, want available", name, states[name])
		}
	}
	for _, name := range []string{"vpp"} {
		if states[name].Available || states[name].State != controlapi.CapabilityDegraded || states[name].Reason == "" {
			t.Fatalf("%s capability = %#v, want explicit degraded state", name, states[name])
		}
	}
	if states["pppoe"].Available || states["pppoe"].State != controlapi.CapabilityAvailable || states["pppoe"].Reason == "" {
		t.Fatalf("pppoe capability = %#v, want optional unavailable state", states["pppoe"])
	}
}

func TestModeRoutesAreGatewayOnlyAndRejectBridgeSwitching(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-mode-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret", ReadonlyUsername: "readonly", ReadonlyPassword: "secret"}), WithClock(fixedClock()))
	mode := request(t, server, http.MethodGet, "/api/v1/mode")
	if mode.Code != http.StatusOK {
		t.Fatalf("mode status = %d, want 200: %s", mode.Code, mode.Body.String())
	}
	for _, required := range []string{"gateway", "switchable", "preserve_admin_account", "preserve_management_port"} {
		if !strings.Contains(mode.Body.String(), required) {
			t.Fatalf("mode response missing %q: %s", required, mode.Body.String())
		}
	}
	if strings.Contains(mode.Body.String(), `"mode":"bridge"`) || strings.Contains(mode.Body.String(), "bridge-switching") {
		t.Fatalf("mode response exposed bridge mode or switching: %s", mode.Body.String())
	}
	if !strings.Contains(mode.Body.String(), `"initialized":false`) {
		t.Fatalf("mode response should start uninitialized: %s", mode.Body.String())
	}

	unauthorized := requestBody(t, server, http.MethodPost, "/api/v1/mode/initialize", `{"mode":"gateway","confirm_reset":true}`)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized initialize status = %d, want 401: %s", unauthorized.Code, unauthorized.Body.String())
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	rejected := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/mode/initialize", `{"mode":"bridge","confirm_reset":true}`, cookie)
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bridge initialize status = %d, want 422: %s", rejected.Code, rejected.Body.String())
	}
	missingConfirmation := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/mode/initialize", `{"mode":"gateway"}`, cookie)
	if missingConfirmation.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing confirmation status = %d, want 422: %s", missingConfirmation.Code, missingConfirmation.Body.String())
	}
	accepted := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/mode/initialize", `{"mode":"gateway","confirm_reset":true,"preserve_admin_account":true,"preserve_management_port":true}`, cookie)
	if accepted.Code != http.StatusOK {
		t.Fatalf("gateway initialize status = %d, want 200: %s", accepted.Code, accepted.Body.String())
	}
	mode = request(t, server, http.MethodGet, "/api/v1/mode")
	if mode.Code != http.StatusOK || !strings.Contains(mode.Body.String(), `"initialized":true`) || !strings.Contains(mode.Body.String(), `"preserve_admin_account":true`) {
		t.Fatalf("persisted mode response = %d %s", mode.Code, mode.Body.String())
	}
	audit := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/audit-events", "", cookie)
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), "/api/v1/mode/initialize") {
		t.Fatalf("audit response = %d %s", audit.Code, audit.Body.String())
	}
}

func TestAdminLoginRequiresPasswordChangeBeforeMutations(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-auth-password-change-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "password", ForcePasswordChange: true}), WithClock(fixedClock()))

	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"password"}`)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), "password_change_required") {
		t.Fatalf("login requiring password change = %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	blocked := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/mode/initialize", `{"mode":"gateway","confirm_reset":true}`, cookie)
	if blocked.Code != http.StatusForbidden || !strings.Contains(blocked.Body.String(), "password_change_required") {
		t.Fatalf("mutation before password change = %d %s", blocked.Code, blocked.Body.String())
	}
	weak := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/auth/change-password", `{"current_password":"password","new_password":"admin"}`, cookie)
	if weak.Code != http.StatusUnprocessableEntity {
		t.Fatalf("weak password status = %d, want 422: %s", weak.Code, weak.Body.String())
	}
	changed := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/auth/change-password", `{"current_password":"password","new_password":"CorrectHorseBattery1"}`, cookie)
	if changed.Code != http.StatusOK || strings.Contains(changed.Body.String(), `"password_change_required":true`) {
		t.Fatalf("change password = %d %s", changed.Code, changed.Body.String())
	}
	oldLogin := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"password"}`)
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login status = %d, want 401: %s", oldLogin.Code, oldLogin.Body.String())
	}
	newLogin := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"CorrectHorseBattery1"}`)
	if newLogin.Code != http.StatusOK || strings.Contains(newLogin.Body.String(), `"password_change_required":true`) {
		t.Fatalf("new password login = %d %s", newLogin.Code, newLogin.Body.String())
	}
}

func TestDesiredStateRoutesCoverOpenAPIControlPlaneWithoutClaimingRuntimeApply(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-desired-state-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", `{"id":"eth1","name":"eth1","interface_id":"eth1","gateway_role":"lan","cidr":"192.168.88.1/24"}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("seed interface status=%d body=%s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dhcp/servers", `{"id":"dhcp-test","interface_id":"eth1","subnet":"192.168.88.0/24","pools":["192.168.88.100-192.168.88.199"],"routers":["192.168.88.1"],"name_servers":["192.168.88.1"],"enabled":true}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("seed dhcp status=%d body=%s", res.Code, res.Body.String())
	}

	for _, path := range []string{
		"/api/v1/interfaces",
		"/api/v1/interfaces/eth1",
		"/api/v1/interfaces/eth1/stats",
		"/api/v1/objects/groups",
		"/api/v1/gateway/wan-links",
		"/api/v1/gateway/wan-groups",
		"/api/v1/gateway/policies/routes",
		"/api/v1/gateway/nat/static",
		"/api/v1/gateway/nat/port-maps",
		"/api/v1/proxy/egresses/proxy-egress-default",
		"/api/v1/proxy/nodes",
		"/api/v1/proxy/subscriptions",
		"/api/v1/proxy/groups",
		"/api/v1/dns/policies/default",
		"/api/v1/dns/domain-ip-sets",
		"/api/v1/dns/upstreams",
		"/api/v1/dhcp/servers",
		"/api/v1/dhcp/servers/dhcp-test",
		"/api/v1/dhcp/leases",
		"/api/v1/dhcp/static-bindings",
		"/api/v1/security/acls",
		"/api/v1/security/ip-mac-bindings",
		"/api/v1/security/threat-intel",
		"/api/v1/security/attack-rules",
	} {
		res := request(t, server, http.MethodGet, path)
		if res.Code == http.StatusNotFound {
			t.Fatalf("%s returned 404 after desired-state route coverage", path)
		}
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200: %s", path, res.Code, res.Body.String())
		}
	}

	create := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-backup","name":"Backup WAN","enabled":true}`, cookie)
	if create.Code != http.StatusOK {
		t.Fatalf("create desired WAN status = %d, want 200: %s", create.Code, create.Body.String())
	}
	if !strings.Contains(create.Body.String(), "desired_not_applied") || !strings.Contains(create.Body.String(), "vpp_runtime_apply") {
		t.Fatalf("create desired WAN did not report honest degraded desired state: %s", create.Body.String())
	}
	read := request(t, server, http.MethodGet, "/api/v1/gateway/wan-links/wan-backup")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "wan-backup") {
		t.Fatalf("read desired WAN status=%d body=%s", read.Code, read.Body.String())
	}
	deleted := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/gateway/wan-links/wan-backup", "", cookie)
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete desired WAN status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := request(t, server, http.MethodGet, "/api/v1/gateway/wan-links/wan-backup")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted desired WAN status=%d, want 404: %s", missing.Code, missing.Body.String())
	}
	attackRule := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/security/attack-rules", `{"id":"sec-syn-flood","name":"SYN flood","enabled":false,"priority":100,"interface_id":"wan0","attack_type":"syn_flood","threshold_pps":100,"burst_packets":200,"enforcement_mode":"enforce","source_prefix":"any","destination_prefix":"any"}`, cookie)
	if attackRule.Code != http.StatusOK || !strings.Contains(attackRule.Body.String(), `"kind":"attack_rule"`) {
		t.Fatalf("attack rule status=%d body=%s", attackRule.Code, attackRule.Body.String())
	}
	proxyNode := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/nodes", `{"id":"proxy-node-hk1","kind":"node","name":"HK 1","enabled":true,"protocol":"vless","address":"203.0.113.10","port":443,"secret":"node-secret"}`, cookie)
	if proxyNode.Code != http.StatusOK || !strings.Contains(proxyNode.Body.String(), `"runtime_state":"desired_not_applied"`) {
		t.Fatalf("proxy node status=%d body=%s", proxyNode.Code, proxyNode.Body.String())
	}
	proxySubscription := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/subscriptions", `{"id":"proxy-sub-main","kind":"subscription","name":"Main","enabled":true,"url":"vless://token@example/sub"}`, cookie)
	if proxySubscription.Code != http.StatusOK || !strings.Contains(proxySubscription.Body.String(), `"kind":"subscription"`) {
		t.Fatalf("proxy subscription status=%d body=%s", proxySubscription.Code, proxySubscription.Body.String())
	}
	wrongSecurityKind := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/security/attack-rules", `{"id":"sec-wrong","kind":"acl","name":"Wrong","enabled":true}`, cookie)
	if wrongSecurityKind.Code != http.StatusUnprocessableEntity || !strings.Contains(wrongSecurityKind.Body.String(), "kind must be") {
		t.Fatalf("wrong security kind status=%d body=%s", wrongSecurityKind.Code, wrongSecurityKind.Body.String())
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "vpp_operations") || strings.Contains(strings.ToLower(preview.Body.String()), "bridge") {
		t.Fatalf("runtime preview should compile gateway desired state without bridge scope: status=%d body=%s", preview.Code, preview.Body.String())
	}

	for _, path := range []string{"/api/v1/telemetry/dashboard", "/api/v1/telemetry/interfaces", "/api/v1/telemetry/top-sessions", "/api/v1/telemetry/top-domains", "/api/v1/telemetry/online-users", "/api/v1/telemetry/policy-hits", "/api/v1/config/export", "/api/v1/config/snapshots"} {
		res := authenticatedJSONRequest(t, server, http.MethodGet, path, "", cookie)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200: %s", path, res.Code, res.Body.String())
		}
		if !strings.Contains(res.Body.String(), "degraded") && !strings.Contains(res.Body.String(), "local_desired_config") && !strings.Contains(res.Body.String(), "config_snapshots") {
			t.Fatalf("%s did not report degraded or desired config state: %s", path, res.Body.String())
		}
	}

	emptyGatewayPayload := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: product.Gateway().ID(), DeviceMode: "gateway", Resources: map[string][]json.RawMessage{}, Excluded: []string{"rendered_config", "runtime_state", "audit_logs", "secrets", "snapshots"}}
	importDryRun := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", configImportJSON(t, ConfigImportRequest{DryRun: true, PackageManifest: manifestForPayload(emptyGatewayPayload), Payload: emptyGatewayPayload}), cookie)
	if importDryRun.Code != http.StatusOK || !strings.Contains(importDryRun.Body.String(), "dry_run") || !strings.Contains(importDryRun.Body.String(), "package_hash") || !strings.Contains(importDryRun.Body.String(), "confirmation_actor") {
		t.Fatalf("import dry-run status=%d body=%s", importDryRun.Code, importDryRun.Body.String())
	}
	var dryRunBody struct {
		PackageHash           string `json:"package_hash"`
		DiffHash              string `json:"diff_hash"`
		ConfirmationToken     string `json:"confirmation_token"`
		ConfirmationActor     string `json:"confirmation_actor"`
		ConfirmationExpiresAt string `json:"confirmation_expires_at"`
	}
	decode(t, importDryRun, &dryRunBody)
	missingConfirmation := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", configImportJSON(t, ConfigImportRequest{Confirm: true, PackageManifest: manifestForPayload(emptyGatewayPayload), Payload: emptyGatewayPayload}), cookie)
	if missingConfirmation.Code != http.StatusUnprocessableEntity || !strings.Contains(missingConfirmation.Body.String(), "confirmation_token") {
		t.Fatalf("missing confirmation status=%d body=%s", missingConfirmation.Code, missingConfirmation.Body.String())
	}
	wanPayload := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: product.Gateway().ID(), DeviceMode: "gateway", Resources: map[string][]json.RawMessage{"wan_link": {json.RawMessage(`{"id":"wan-imported","name":"Imported WAN","enabled":true}`)}}, Excluded: emptyGatewayPayload.Excluded}
	wanPackageHash := hashConfigPayload(wanPayload)
	confirmedRequest := ConfigImportRequest{Confirm: true, ConfirmationToken: dryRunBody.ConfirmationToken, ConfirmationActor: dryRunBody.ConfirmationActor, ConfirmationExpiresAt: dryRunBody.ConfirmationExpiresAt, ConfirmationPackageHash: wanPackageHash, ConfirmationDiffHash: hashObject(map[string]any{"current": "desired_config", "incoming": wanPackageHash}), PackageManifest: manifestForPayload(wanPayload), Payload: wanPayload}
	confirmed := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", configImportJSON(t, confirmedRequest), cookie)
	if confirmed.Code != http.StatusOK || !strings.Contains(confirmed.Body.String(), "rollback_snapshot_id") || !strings.Contains(confirmed.Body.String(), "persisted") {
		t.Fatalf("confirmed import status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	importedWAN := request(t, server, http.MethodGet, "/api/v1/gateway/wan-links/wan-imported")
	if importedWAN.Code != http.StatusOK {
		t.Fatalf("imported WAN status=%d body=%s", importedWAN.Code, importedWAN.Body.String())
	}
	secretPayload := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: product.Gateway().ID(), DeviceMode: "gateway", Resources: map[string][]json.RawMessage{"proxy_subscription": {json.RawMessage(`{"id":"secret-subscription","url":"vless://user@example"}`)}}}
	secretImport := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", configImportJSON(t, ConfigImportRequest{DryRun: true, PackageManifest: manifestForPayload(secretPayload), Payload: secretPayload}), cookie)
	if secretImport.Code != http.StatusUnprocessableEntity || !strings.Contains(secretImport.Body.String(), "secret_material_rejected") {
		t.Fatalf("secret import status=%d body=%s", secretImport.Code, secretImport.Body.String())
	}
	controlFieldImport := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/import", configImportJSON(t, ConfigImportRequest{DryRun: true, ConfirmationToken: "0123456789abcdef", PackageManifest: manifestForPayload(emptyGatewayPayload), Payload: emptyGatewayPayload}), cookie)
	if controlFieldImport.Code != http.StatusOK || !strings.Contains(controlFieldImport.Body.String(), "dry_run") {
		t.Fatalf("control-field import status=%d body=%s", controlFieldImport.Code, controlFieldImport.Body.String())
	}
	createSnapshot := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/snapshots", `{"name":"snapshot-test"}`, cookie)
	if createSnapshot.Code != http.StatusOK || !strings.Contains(createSnapshot.Body.String(), "created") || !strings.Contains(createSnapshot.Body.String(), "desired_not_applied") {
		t.Fatalf("create snapshot status=%d body=%s", createSnapshot.Code, createSnapshot.Body.String())
	}
	snapshotList := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/config/snapshots", "", cookie)
	if snapshotList.Code != http.StatusOK || !strings.Contains(snapshotList.Body.String(), "snapshot-test") || !strings.Contains(snapshotList.Body.String(), "config_snapshots") {
		t.Fatalf("snapshot list status=%d body=%s", snapshotList.Code, snapshotList.Body.String())
	}
	factoryReset := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/factory-reset", `{}`, cookie)
	if factoryReset.Code != http.StatusOK || !strings.Contains(factoryReset.Body.String(), "reset") || !strings.Contains(factoryReset.Body.String(), "desired_not_applied") {
		t.Fatalf("factory reset status=%d body=%s", factoryReset.Code, factoryReset.Body.String())
	}
}

func TestWANLinkRejectsDuplicateIPFamilyOnInterface(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-wan-family-conflict-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	firstIPv4 := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-eth5-ipv4","name":"WAN IPv4","interface_id":"eth5","gateway_role":"wan","type":"static","wan_type":"static","cidr":"198.51.100.2/24","gateway":"198.51.100.1","ipv4":{"mode":"static","address":"198.51.100.2/24","gateway":"198.51.100.1"},"ipv6":{"mode":"disabled"}}`, cookie)
	if firstIPv4.Code != http.StatusOK {
		t.Fatalf("first IPv4 WAN status=%d body=%s", firstIPv4.Code, firstIPv4.Body.String())
	}
	duplicateIPv4 := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-eth5-dhcp4","name":"WAN DHCP4","interface_id":"eth5","gateway_role":"wan","type":"dhcp4","wan_type":"dhcp4","ipv4":{"mode":"dhcp4"},"ipv6":{"mode":"disabled"}}`, cookie)
	if duplicateIPv4.Code != http.StatusUnprocessableEntity || !strings.Contains(duplicateIPv4.Body.String(), "already has IPV4 configuration") {
		t.Fatalf("duplicate IPv4 WAN status=%d body=%s", duplicateIPv4.Code, duplicateIPv4.Body.String())
	}
	firstIPv6 := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-eth5-ipv6","name":"WAN IPv6","interface_id":"eth5","gateway_role":"wan","type":"static6","wan_type":"static6","cidr":"2001:db8::2/64","gateway":"fe80::1","ipv4":{"mode":"disabled"},"ipv6":{"mode":"static","address":"2001:db8::2/64","gateway":"fe80::1"}}`, cookie)
	if firstIPv6.Code != http.StatusOK {
		t.Fatalf("first IPv6 WAN status=%d body=%s", firstIPv6.Code, firstIPv6.Body.String())
	}
	patchIPv4 := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/gateway/wan-links/wan-eth5-ipv4", `{"id":"wan-eth5-ipv4","name":"WAN IPv4 changed","interface_id":"eth5","gateway_role":"wan","type":"static","wan_type":"static","cidr":"198.51.100.3/24","gateway":"198.51.100.1","ipv4":{"mode":"static","address":"198.51.100.3/24","gateway":"198.51.100.1"},"ipv6":{"mode":"disabled"}}`, cookie)
	if patchIPv4.Code != http.StatusOK || !strings.Contains(patchIPv4.Body.String(), `"interface_id":"eth5"`) || strings.Contains(patchIPv4.Body.String(), `"interface_id":"wan-eth5-ipv4"`) {
		t.Fatalf("patch IPv4 WAN status=%d body=%s", patchIPv4.Code, patchIPv4.Body.String())
	}
}

func TestWANLinkRejectsUnconfiguredStaticLinks(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-wan-empty-config-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	emptyStatic := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"enp3s0","name":"eth2","interface_id":"enp3s0","gateway_role":"wan","type":"static","wan_type":"static","ipv4":{"mode":"static","address":"","gateway":""},"ipv6":{"mode":"disabled"}}`, cookie)
	if emptyStatic.Code != http.StatusUnprocessableEntity || !strings.Contains(emptyStatic.Body.String(), "static IPv4 WAN requires address and gateway") {
		t.Fatalf("empty static WAN status=%d body=%s", emptyStatic.Code, emptyStatic.Body.String())
	}
	dhcp := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-dhcp","name":"WAN DHCP","interface_id":"eth7","gateway_role":"wan","type":"dhcp4","wan_type":"dhcp4","ipv4":{"mode":"dhcp4"},"ipv6":{"mode":"disabled"}}`, cookie)
	if dhcp.Code != http.StatusOK {
		t.Fatalf("dhcp WAN status=%d body=%s", dhcp.Code, dhcp.Body.String())
	}
}

func TestRuntimePreviewRendersGatewayPlanWithoutMutatingRuntime(t *testing.T) {
	server := New(WithClock(fixedClock()))
	res := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if res.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, required := range []string{"service_artifacts", "smartdns", "xray", "vpp_operations", "nftables_tproxy_plan", "linux_policy_routing_plan"} {
		if !strings.Contains(body, required) {
			t.Fatalf("preview response missing %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{"\"content\"", "\"password\"", "\"audit_summary\""} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("preview response leaked %q: %s", forbidden, body)
		}
	}
	if server.lastRuntime != nil {
		t.Fatalf("preview mutated last runtime apply: %#v", server.lastRuntime)
	}
}

func TestRuntimePreviewDoesNotAttachUnassignedNonManagementPhysicalInterfaces(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp1s0", "name": "enp1s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp2s0", "name": "enp2s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp3s0", "name": "enp3s0", "link_state": "down", "admin_state": "up"},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp1s0")
	res := request(t, New(WithClock(fixedClock())), http.MethodGet, "/api/v1/runtime/preview")
	if res.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{`"dataplane_state":"native_ready"`, `"vpp_operations":[]`} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("runtime preview missing idle dataplane evidence %q: %s", required, res.Body.String())
		}
	}
	if strings.Contains(res.Body.String(), "native-driver-") {
		t.Fatalf("runtime preview must not attach any unassigned interface: %s", res.Body.String())
	}
}

func TestRuntimePreviewCompilesLANAndWANAddressesToVPPOperations(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp1s0", "name": "enp1s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp2s0", "name": "enp2s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp3s0", "name": "enp3s0", "link_state": "up", "admin_state": "up"},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp1s0")
	useVPPProof(t, "enp1s0")
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-interface-address-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	lan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", `{"id":"eth1","name":"eth1","interface_id":"eth1","gateway_role":"lan","cidr":"10.10.10.2/24","dns_servers":[],"nat":false}`, cookie)
	if lan.Code != http.StatusOK {
		t.Fatalf("lan status = %d, want 200: %s", lan.Code, lan.Body.String())
	}
	wan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-static","name":"WAN Static","interface_id":"eth2","gateway_role":"wan","wan_type":"static","type":"static","cidr":"10.10.10.16/24","gateway":"10.10.10.1","ipv4":{"mode":"static","address":"10.10.10.16/24","gateway":"10.10.10.1"}}`, cookie)
	if wan.Code != http.StatusOK {
		t.Fatalf("wan status = %d, want 200: %s", wan.Code, wan.Body.String())
	}
	preview := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/runtime/preview", "", cookie)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{"vpp.interface.address", "set interface ip address lyroute-enp2s0 10.10.10.2/24", "set interface ip address lyroute-enp3s0 10.10.10.16/24"} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("runtime preview missing %q: %s", required, preview.Body.String())
		}
	}
	if strings.Contains(preview.Body.String(), `"dns_interception_plan":{"lan_interfaces":["enp2s0"],"listen_port":53}`) {
		t.Fatalf("runtime preview rendered forbidden nftables DNS interception: %s", preview.Body.String())
	}
}

func TestInterfacesExposeDesiredLANCIDRForVPPInterface(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp1s0", "name": "enp1s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp2s0", "name": "enp2s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp3s0", "name": "enp3s0", "link_state": "up", "admin_state": "up", "addresses": []string{"169.254.79.144/16"}},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp1s0")
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-interface-lan-cidr-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	lan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", `{"id":"eth2","name":"eth2","interface_id":"eth2","gateway_role":"lan","cidr":"10.10.10.1/24","dns_servers":[],"nat":false}`, cookie)
	if lan.Code != http.StatusOK {
		t.Fatalf("lan status = %d, want 200: %s", lan.Code, lan.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/interfaces/eth2", "", cookie)
	if res.Code != http.StatusOK {
		t.Fatalf("interface status = %d, want 200: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"cidr":"10.10.10.1/24"`) || !strings.Contains(res.Body.String(), `"address":"10.10.10.1/24"`) {
		t.Fatalf("interface snapshot missing desired LAN cidr: %s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"system_name":"enp3s0"`) || !strings.Contains(res.Body.String(), `"interface_id":"enp3s0"`) {
		t.Fatalf("interface snapshot missing physical interface mapping: %s", res.Body.String())
	}
}

func TestRuntimePreviewDHCPWANOverridesStaleStaticAlias(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp1s0", "name": "enp1s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp2s0", "name": "enp2s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp3s0", "name": "enp3s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp4s0", "name": "enp4s0", "link_state": "up", "admin_state": "up"},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp1s0")
	useVPPProof(t, "enp1s0")
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-dhcp-wan-alias-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := fixedClock()().UTC()
	for _, document := range []persistence.ConfigDocument{
		configDocument(t, "wan_link", "enp4s0", map[string]any{"id": "enp4s0", "name": "eth3", "interface_id": "enp4s0", "gateway_role": "wan", "type": "static", "wan_type": "static", "cidr": "10.10.10.6/24", "ipv4": map[string]any{"mode": "static", "address": "10.10.10.6/24"}}, now),
		configDocument(t, "wan_link", "eth3", map[string]any{"id": "eth3", "name": "dhcp wan", "interface_id": "eth3", "gateway_role": "wan", "type": "dhcp4", "wan_type": "dhcp4", "ipv4": map[string]any{"mode": "dhcp4"}}, now),
	} {
		if err := store.SaveConfig(ctx, document); err != nil {
			t.Fatal(err)
		}
	}
	preview := request(t, New(WithStore(store), WithClock(fixedClock())), http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{"set interface ip address del lyroute-enp4s0 10.10.10.6/24", "set dhcp client intfc lyroute-enp4s0", `"mode":"dhcp4"`} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("runtime preview missing %q: %s", required, preview.Body.String())
		}
	}
	if strings.Contains(preview.Body.String(), `"?set interface ip address lyroute-enp4s0 10.10.10.6/24","`) {
		t.Fatalf("runtime preview re-added stale static WAN address: %s", preview.Body.String())
	}
}

func TestRuntimePreviewSkipsInvalidVPPAddressAssignments(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp1s0", "name": "enp1s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp2s0", "name": "enp2s0", "link_state": "up", "admin_state": "up"},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp1s0")
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-invalid-interface-address-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	lan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", `{"id":"eth1","name":"eth1","interface_id":"eth1","gateway_role":"lan","cidr":"10.10.10.2/24;show version","dns_servers":[],"nat":false}`, cookie)
	if lan.Code != http.StatusOK {
		t.Fatalf("lan status = %d, want 200: %s", lan.Code, lan.Body.String())
	}
	preview := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/runtime/preview", "", cookie)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", preview.Code, preview.Body.String())
	}
	if strings.Contains(preview.Body.String(), "set interface ip address") || strings.Contains(preview.Body.String(), "show version") {
		t.Fatalf("runtime preview rendered invalid VPP address command: %s", preview.Body.String())
	}
}

func TestRuntimePreviewCompilesDesiredNATPortMapToVPPOperation(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-nat-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if assigned := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", `{"id":"wan0","name":"wan0","interface_id":"wan0","gateway_role":"wan"}`, cookie); assigned.Code != http.StatusOK {
		t.Fatalf("assign WAN status = %d: %s", assigned.Code, assigned.Body.String())
	}
	create := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/nat/port-maps", `{"id":"nas-https","wan_link":"wan0","external_address":"203.0.113.10","external_port":9443,"internal_host":"192.168.10.30","internal_port":443,"protocol":"TCP"}`, cookie)
	if create.Code != http.StatusOK {
		t.Fatalf("create port map status = %d, want 200: %s", create.Code, create.Body.String())
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK {
		t.Fatalf("runtime preview status = %d, want 200: %s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{"compiled_nat", "vpp.nat44-ed.static-mapping", "nat44 add static mapping tcp local 192.168.10.30 443 external 203.0.113.10 9443", "set interface nat44 out lyroute-wan0"} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("runtime preview missing %q: %s", required, preview.Body.String())
		}
	}
}

func TestRuntimePlanRendersSingleKeaArtifactForMultipleDHCPServers(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-dhcp-multi-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := fixedClock()().UTC()
	documents := []persistence.ConfigDocument{}
	for _, item := range []map[string]any{
		{"id": "eth1", "name": "eth1", "interface_id": "eth1", "gateway_role": "lan", "cidr": "192.168.88.1/24"},
		{"id": "eth2", "name": "eth2", "interface_id": "eth2", "gateway_role": "lan", "cidr": "192.168.20.1/24"},
	} {
		payload, hash, err := persistence.MarshalPayload(item)
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, persistence.ConfigDocument{ResourceType: "interface", ResourceID: item["id"].(string), Payload: payload, PayloadHash: hash, UpdatedAt: now})
	}
	for _, item := range []map[string]any{
		{"id": "lan", "interface_id": "eth1", "subnet": "192.168.88.0/24", "pools": []string{"192.168.88.100-192.168.88.199"}, "routers": []string{"192.168.88.1"}, "name_servers": []string{"192.168.88.1"}, "enabled": true},
		{"id": "guest", "interface_id": "eth2", "subnet": "192.168.20.0/24", "pools": []string{"192.168.20.100-192.168.20.199"}, "routers": []string{"192.168.20.1"}, "name_servers": []string{"192.168.20.1"}, "enabled": true},
	} {
		payload, hash, err := persistence.MarshalPayload(item)
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, persistence.ConfigDocument{ResourceType: "dhcp_server", ResourceID: item["id"].(string), Payload: payload, PayloadHash: hash, UpdatedAt: now})
	}
	if err := store.SaveConfigs(ctx, documents); err != nil {
		t.Fatal(err)
	}
	plan, err := New(WithStore(store), WithClock(fixedClock())).buildRuntimePlan(ctx, "test-dhcp")
	if err != nil {
		t.Fatal(err)
	}
	var keaArtifacts []serviceRuntime.RenderedArtifact
	for _, artifact := range plan.RuntimeArtifacts {
		if artifact.Service == serviceRuntime.Kea {
			keaArtifacts = append(keaArtifacts, artifact)
		}
	}
	if len(keaArtifacts) != 1 {
		t.Fatalf("kea artifact count = %d, want 1: %#v", len(keaArtifacts), keaArtifacts)
	}
	for _, required := range []string{"lylan-ens33", "lylan-ens35", "192.168.88.0/24", "192.168.20.0/24"} {
		if !strings.Contains(keaArtifacts[0].Content, required) {
			t.Fatalf("kea artifact missing %q: %s", required, keaArtifacts[0].Content)
		}
	}
	if len(plan.DHCPServers) < 2 {
		t.Fatalf("dhcp plans = %#v, want at least the stored DHCP servers", plan.DHCPServers)
	}
}

func TestRuntimePlanFailsClosedWhenStoredDNSPoliciesAreDisabled(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-dns-disabled-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policy := dns.NewPolicy(dns.Reject(), []dns.Rule{})
	payload, hash, err := persistence.MarshalPayload(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePolicy(ctx, persistence.PolicyDocument{Namespace: "dns-policy", PolicyID: "disabled", Priority: 10, Enabled: false, Payload: payload, PayloadHash: hash, UpdatedAt: fixedClock()().UTC()}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(WithStore(store), WithClock(fixedClock())).buildRuntimePlan(ctx, "test-dns")
	if err != nil {
		t.Fatal(err)
	}
	var smartDNSArtifacts []serviceRuntime.RenderedArtifact
	for _, artifact := range plan.RuntimeArtifacts {
		if artifact.Service == serviceRuntime.SmartDNS {
			smartDNSArtifacts = append(smartDNSArtifacts, artifact)
		}
	}
	var disabledArtifact serviceRuntime.RenderedArtifact
	for _, artifact := range smartDNSArtifacts {
		if artifact.Path == "/etc/smartdns/conf.d/ly-route-active.conf" {
			disabledArtifact = artifact
		}
	}
	if disabledArtifact.Path == "" || !strings.Contains(disabledArtifact.Content, "address #") {
		t.Fatalf("smartdns disabled-policy fail-closed artifact = %#v", smartDNSArtifacts)
	}
}

func TestRuntimePlanIgnoresDeletedProxyEgress(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-deleted-proxy-egress-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	payload, hash, err := persistence.MarshalPayload(map[string]any{"id": "proxy-egress-default", "deleted": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(ctx, persistence.ConfigDocument{ResourceType: "proxy_egress", ResourceID: "proxy-egress-default", Payload: payload, PayloadHash: hash, UpdatedAt: fixedClock()().UTC()}); err != nil {
		t.Fatal(err)
	}
	plan, err := New(WithStore(store), WithClock(fixedClock())).buildRuntimePlan(ctx, "test-deleted-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProxyEgress.ID != "" {
		t.Fatalf("proxy egress = %#v, want no compiled proxy egress", plan.ProxyEgress)
	}
}

func TestNATPortMapCRUDDecoratesStatsAndUpdatesRuntimePreview(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-nat-crud-stats-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if assigned := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", `{"id":"wan0","name":"wan0","interface_id":"wan0","gateway_role":"wan"}`, cookie); assigned.Code != http.StatusOK {
		t.Fatalf("assign WAN status = %d: %s", assigned.Code, assigned.Body.String())
	}

	create := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/nat/port-maps", `{"id":"nas-service","wan_link":"wan0","external_address":"203.0.113.10","external_port":9443,"internal_host":"192.168.10.30","internal_port":9443,"protocol":"tcp","hairpin":true}`, cookie)
	if create.Code != http.StatusOK {
		t.Fatalf("create port map status = %d, want 200: %s", create.Code, create.Body.String())
	}
	list := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/nat/port-maps", "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("list port maps status = %d, want 200: %s", list.Code, list.Body.String())
	}
	for _, required := range []string{`"enabled":true`, `"last_hit_at":null`, `"session_count":null`, `"dataplane_observed":false`, `"runtime_state":"desired_not_applied"`} {
		if !strings.Contains(list.Body.String(), required) {
			t.Fatalf("port map list missing %q: %s", required, list.Body.String())
		}
	}

	update := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/gateway/nat/port-maps/nas-service", `{"id":"nas-service","wan_link":"wan0","external_address":"203.0.113.10","external_port":5353,"internal_host":"192.168.10.53","internal_port":53,"protocol":"udp"}`, cookie)
	if update.Code != http.StatusOK {
		t.Fatalf("update port map status = %d, want 200: %s", update.Code, update.Body.String())
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK {
		t.Fatalf("runtime preview status = %d, want 200: %s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{"nat44 add static mapping udp local 192.168.10.53 53 external 203.0.113.10 5353", "nat44 add static mapping udp local 192.168.10.53 53 external 203.0.113.10 5353 del"} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("runtime preview missing %q: %s", required, preview.Body.String())
		}
	}
	if strings.Contains(preview.Body.String(), "nat44 add static mapping tcp local 192.168.10.30 9443 external 203.0.113.10 9443") {
		t.Fatalf("runtime preview retained old TCP mapping: %s", preview.Body.String())
	}

	deleted := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/gateway/nat/port-maps/nas-service", "", cookie)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete port map status = %d, want 200: %s", deleted.Code, deleted.Body.String())
	}
	afterDelete := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if afterDelete.Code != http.StatusOK {
		t.Fatalf("runtime preview after delete status = %d, want 200: %s", afterDelete.Code, afterDelete.Body.String())
	}
	if strings.Contains(afterDelete.Body.String(), "nas-service") || strings.Contains(afterDelete.Body.String(), "203.0.113.10 5353") {
		t.Fatalf("runtime preview retained deleted mapping: %s", afterDelete.Body.String())
	}
}

func TestNATPortMapRejectsDuplicateInternalEndpointBeforeSave(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-nat-duplicate-endpoint-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	first := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/nat/port-maps", `{"id":"existing","external_address":"203.0.113.10","external_port":18080,"internal_host":"192.168.50.101","internal_port":8080,"protocol":"tcp"}`, cookie)
	if first.Code != http.StatusOK {
		t.Fatalf("first port map status = %d, want 200: %s", first.Code, first.Body.String())
	}
	duplicate := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/nat/port-maps", `{"id":"duplicate","external_address":"203.0.113.10","external_port":37140,"internal_host":"192.168.50.101","internal_port":8080,"protocol":"tcp"}`, cookie)
	if duplicate.Code != http.StatusUnprocessableEntity || !strings.Contains(duplicate.Body.String(), "one mapping per internal endpoint") {
		t.Fatalf("duplicate port map = %d %s", duplicate.Code, duplicate.Body.String())
	}
	list := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/nat/port-maps", "", cookie)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), `"id":"duplicate"`) {
		t.Fatalf("duplicate port map was persisted: %d %s", list.Code, list.Body.String())
	}
}

func TestNATUnsupportedIPv6AndUPnPRoutesAbsent(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-nat-unsupported-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	nat64 := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/nat/port-maps", `{"id":"nat64-web","kind":"nat64","external_address":"2001:db8::10","external_port":8443,"internal_host":"192.168.10.30","internal_port":443,"protocol":"tcp"}`, cookie)
	if nat64.Code != http.StatusUnprocessableEntity || !strings.Contains(nat64.Body.String(), "only supports IPv4 NAT44") {
		t.Fatalf("nat64 status=%d body=%s", nat64.Code, nat64.Body.String())
	}
	list := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/nat/port-maps", "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("list port maps status = %d, want 200: %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), "nat64-web") {
		t.Fatalf("unsupported NAT64 request was persisted: %s", list.Body.String())
	}
	upnp := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/nat/upnp", "", cookie)
	if upnp.Code != http.StatusNotFound {
		t.Fatalf("UPnP route status = %d, want 404: %s", upnp.Code, upnp.Body.String())
	}
}

func TestTrafficControlBehaviorPoliciesCompileToVPPPreview(t *testing.T) {
	ctx := context.Background()
	previousInventory := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "enp1s0", "name": "enp1s0"}, {"id": "enp2s0", "name": "enp2s0"}}
	}
	t.Cleanup(func() { hostInterfaceInventory = previousInventory })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp1s0")
	useVPPProof(t, "enp1s0")
	store, err := persistence.Open(ctx, "file:httpapi-traffic-control-behavior-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	if got := server.managementInterfaceID(ctx); got != "enp1s0" {
		t.Fatalf("management interface = %q, want enp1s0", got)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if assigned := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", `{"id":"eth1","name":"eth1","interface_id":"eth1","gateway_role":"lan"}`, cookie); assigned.Code != http.StatusOK {
		t.Fatalf("assign LAN status = %d: %s", assigned.Code, assigned.Body.String())
	}
	create := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/traffic-control", `{"id":"behavior-default","rules":[{"id":"drop-guest","granularity":"rule","match":{"sources":["192.168.20.0/24"],"destinations":["10.0.0.0/8"],"protocols":["tcp"],"dest_ports":["443"],"direction":"uplink"},"actions":[{"kind":"drop"}]},{"id":"limit-video","granularity":"rule","match":{"destinations":["203.0.113.10"],"protocols":["udp"],"direction":"downlink"},"actions":[{"kind":"policer","policer":{"rate_bps":20000000,"burst_bps":2000000}}]}]}`, cookie)
	if create.Code != http.StatusOK {
		t.Fatalf("create traffic control status = %d, want 200: %s", create.Code, create.Body.String())
	}
	list := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/traffic-control", "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("list traffic control status = %d, want 200: %s", list.Code, list.Body.String())
	}
	for _, required := range []string{`"hit_count":null`, `"hit_count_state":"unavailable"`, "VPP policy hit counter readback is not configured"} {
		if !strings.Contains(list.Body.String(), required) {
			t.Fatalf("traffic-control list missing %q: %s", required, list.Body.String())
		}
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK {
		t.Fatalf("runtime preview status = %d, want 200: %s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{"vpp.acl.drop", `"sources":["192.168.20.0/24"]`, `"destinations":["10.0.0.0/8"]`, `"dest_ports":["443"]`, "vpp.behavior.rate", `"rate_bps":20000000`, `"hit_count_state":"unavailable"`} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("runtime preview missing %q: %s", required, preview.Body.String())
		}
	}
	if strings.Contains(preview.Body.String(), "connection_limit") || strings.Contains(preview.Body.String(), "max_connections") {
		t.Fatalf("runtime preview leaked connection-limit feature: %s", preview.Body.String())
	}
}

func TestTrafficControlRejectsConnectionLimitAction(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-traffic-control-connection-limit-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	rejected := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/traffic-control", `{"id":"bad-connlimit","rules":[{"id":"limit-connections","granularity":"rule","actions":[{"kind":"connection_limit","max_connections":100}]}]}`, login.Result().Cookies()[0])
	if rejected.Code != http.StatusBadRequest && rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("connection-limit status = %d, want 400/422: %s", rejected.Code, rejected.Body.String())
	}
}

func TestWANGroupRejectsMixedProxyAndRealMembers(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-wan-group-mixed-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	wan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-static","name":"WAN Static","enabled":true,"type":"static","ipv4":{"mode":"static","address":"198.51.100.10/30"}}`, cookie)
	if wan.Code != http.StatusOK || !strings.Contains(wan.Body.String(), "static_or_slaac") || !strings.Contains(wan.Body.String(), "active_health_checks") {
		t.Fatalf("WAN create status=%d body=%s", wan.Code, wan.Body.String())
	}
	proxyWAN := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/egresses", `{"id":"proxy-wan","kind":"egress","name":"Proxy WAN","enabled":true,"semantic_type":"proxy_egress","display_list":"wan","proxy_profile_id":"xray-tproxy-outbound","underlay_wan_id":"wan-static"}`, cookie)
	if proxyWAN.Code != http.StatusOK {
		t.Fatalf("proxy WAN status=%d body=%s", proxyWAN.Code, proxyWAN.Body.String())
	}
	mixed := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-groups", `{"id":"wan-group-mixed","name":"Mixed","members":["wan-static","proxy-wan"],"member_weights":[{"id":"wan-static","weight":2},{"id":"proxy-wan","weight":1}]}`, cookie)
	if mixed.Code != http.StatusUnprocessableEntity || !strings.Contains(mixed.Body.String(), "does not support proxy egress") {
		t.Fatalf("mixed group status=%d body=%s", mixed.Code, mixed.Body.String())
	}
	pureProxy := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-groups", `{"id":"wan-group-proxy","name":"Proxy","members":["proxy-wan","proxy-wan"]}`, cookie)
	if pureProxy.Code != http.StatusUnprocessableEntity || !strings.Contains(pureProxy.Body.String(), "does not support proxy egress") {
		t.Fatalf("proxy group status=%d body=%s", pureProxy.Code, pureProxy.Body.String())
	}
	wanBackup := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-backup","name":"WAN Backup","enabled":true,"type":"static","ipv4":{"mode":"static","address":"203.0.113.10/30"}}`, cookie)
	if wanBackup.Code != http.StatusOK {
		t.Fatalf("backup WAN status=%d body=%s", wanBackup.Code, wanBackup.Body.String())
	}
	realOnly := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-groups", `{"id":"wan-group-real","name":"Real","members":["wan-static","wan-backup"],"member_weights":[{"id":"wan-static","weight":2},{"id":"wan-backup","weight":1}]}`, cookie)
	if realOnly.Code != http.StatusOK || !strings.Contains(realOnly.Body.String(), "per_connection_weighted") || !strings.Contains(realOnly.Body.String(), "auto_rejoin") {
		t.Fatalf("real group status=%d body=%s", realOnly.Code, realOnly.Body.String())
	}
}

func TestProxyWANRequiresUnderlayWAN(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-wan-underlay-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	rejected := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/egresses", `{"id":"proxy-no-underlay","kind":"egress","semantic_type":"proxy_egress","display_list":"wan","proxy_profile_id":"xray-tproxy-outbound"}`, login.Result().Cookies()[0])
	if rejected.Code != http.StatusUnprocessableEntity || !strings.Contains(rejected.Body.String(), "underlay_wan_id") {
		t.Fatalf("proxy underlay status=%d body=%s", rejected.Code, rejected.Body.String())
	}
}

func TestProxyEgressPatchRejectsNonCanonicalPayloadAndPreservesRuntimeFields(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-egress-patch-canonical-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	wan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-static","name":"WAN Static","enabled":true,"type":"static","interface_id":"eth1","ipv4":{"mode":"static","address":"198.51.100.10/30"}}`, cookie)
	if wan.Code != http.StatusOK {
		t.Fatalf("WAN status = %d, want 200: %s", wan.Code, wan.Body.String())
	}
	create := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/egresses", `{"id":"proxy-egress-test","kind":"egress","name":"Proxy WAN","enabled":true,"semantic_type":"proxy_egress","display_list":"wan","proxy_profile_id":"xray-tproxy-outbound","underlay_wan_id":"wan-static"}`, cookie)
	if create.Code != http.StatusOK {
		t.Fatalf("proxy egress create status = %d, want 200: %s", create.Code, create.Body.String())
	}
	bad := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/proxy/egresses/proxy-egress-test", `{"id":"proxy-egress-test","name":"Proxy WAN","underlay_wan_id":"wan-static","node_id":"proxy-node-test"}`, cookie)
	if bad.Code != http.StatusUnprocessableEntity || !strings.Contains(bad.Body.String(), "runtime_profile") {
		t.Fatalf("non-canonical proxy patch status = %d, want 422 with runtime_profile error: %s", bad.Code, bad.Body.String())
	}
	good := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/proxy/egresses/proxy-egress-test", `{"id":"proxy-egress-test","name":"Proxy WAN","enabled":true,"semantic_type":"proxy_egress","display_list":"wan","runtime_profile":"xray-tproxy-outbound","capture_path":"vpp_service_interface","engine":"xray","handoff":"vpp_to_service","listener_mode":"vpp-service","underlay_wan_id":"wan-static","node_id":"proxy-node-test"}`, cookie)
	if good.Code != http.StatusOK {
		t.Fatalf("canonical proxy patch status = %d, want 200: %s", good.Code, good.Body.String())
	}
	item := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/egresses/proxy-egress-test", "", cookie)
	if item.Code != http.StatusOK {
		t.Fatalf("proxy egress read status = %d, want 200: %s", item.Code, item.Body.String())
	}
	for _, required := range []string{"runtime_profile", "capture_path", "engine", "handoff", "node_id"} {
		if !strings.Contains(item.Body.String(), required) {
			t.Fatalf("proxy egress readback missing %q: %s", required, item.Body.String())
		}
	}
}

func TestProxyNodeSubscriptionCRUDRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-redaction-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	node := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/nodes", `{"id":"proxy-node-secret","kind":"node","name":"Secret Node","enabled":true,"protocol":"vless","address":"203.0.113.10","port":443,"secret":"node-super-secret"}`, cookie)
	if node.Code != http.StatusOK || strings.Contains(node.Body.String(), "node-super-secret") || !strings.Contains(node.Body.String(), "secret_redacted") {
		t.Fatalf("node redaction status=%d body=%s", node.Code, node.Body.String())
	}
	subscription := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/subscriptions", `{"id":"proxy-sub-secret","kind":"subscription","name":"Secret Sub","enabled":true,"url":"vless://token@example/sub"}`, cookie)
	if subscription.Code != http.StatusOK || strings.Contains(subscription.Body.String(), "vless://token") || !strings.Contains(subscription.Body.String(), "url_redacted") {
		t.Fatalf("subscription redaction status=%d body=%s", subscription.Code, subscription.Body.String())
	}
	listed := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/subscriptions", "", cookie)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "vless://token") {
		t.Fatalf("subscription list leaked secret: status=%d body=%s", listed.Code, listed.Body.String())
	}
	storedNodeSecret, err := store.Secret(ctx, "proxy_node", "proxy-node-secret", "secret")
	if err != nil || storedNodeSecret != "node-super-secret" {
		t.Fatalf("stored node secret = %q, err=%v", storedNodeSecret, err)
	}
	storedSubscriptionURL, err := store.Secret(ctx, "proxy_subscription", "proxy-sub-secret", "url")
	if err != nil || storedSubscriptionURL != "vless://token@example/sub" {
		t.Fatalf("stored subscription URL = %q, err=%v", storedSubscriptionURL, err)
	}
	for _, resource := range []struct{ resourceType, id string }{{"proxy_node", "proxy-node-secret"}, {"proxy_subscription", "proxy-sub-secret"}} {
		document, err := store.Config(ctx, resource.resourceType, resource.id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(document.Payload), "node-super-secret") || strings.Contains(string(document.Payload), "vless://token") {
			t.Fatalf("public desired document leaked secret: %s", document.Payload)
		}
	}
}

func TestProxyNodeURIWritePreservesRealitySettingsWithoutLeakingURI(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-uri-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	uri := "vless://11111111-1111-1111-1111-111111111111@node.example:443?type=tcp&encryption=none&security=reality&pbk=public-key&fp=chrome&sni=www.example.com&sid=98&flow=xtls-rprx-vision#Primary"
	body, _ := json.Marshal(map[string]any{"id": "proxy-node-uri", "kind": "node", "name": "Reality node", "enabled": true, "uri": uri})
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/nodes", string(body), cookie)
	response := created.Body.String()
	if created.Code != http.StatusOK || strings.Contains(response, uri) || strings.Contains(response, "11111111-1111-1111-1111-111111111111") || !strings.Contains(response, `"uri_redacted":"redacted"`) {
		t.Fatalf("proxy URI redaction status=%d body=%s", created.Code, response)
	}
	items, err := server.desiredItems(ctx, "proxy_node")
	if err != nil || len(items) != 1 {
		t.Fatalf("proxy node items=%#v err=%v", items, err)
	}
	settings, _ := items[0]["settings"].(map[string]any)
	reality, _ := settings["realitySettings"].(map[string]any)
	if stringField(items[0], "protocol") != "vless" || stringField(items[0], "address") != "node.example" || intField(items[0], "port") != 443 || settings["flow"] != "xtls-rprx-vision" || reality["publicKey"] != "public-key" || reality["serverName"] != "www.example.com" {
		t.Fatalf("parsed proxy node=%#v", items[0])
	}
	secret, err := store.Secret(ctx, "proxy_node", "proxy-node-uri", "secret")
	if err != nil || secret != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("stored proxy credential=%q err=%v", secret, err)
	}
}

func TestPrivateSubscriptionRefreshAPIImportsRealityNode(t *testing.T) {
	privateFile := strings.TrimSpace(os.Getenv("LY_ROUTE_PRIVATE_SUBSCRIPTION_FILE"))
	if privateFile == "" {
		t.Skip("private subscription file is not configured")
	}
	privateContent, err := os.ReadFile(privateFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-private-subscription-refresh?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()), WithProxyEgress(proxy.NewProxyEgress("proxy-egress-default", "xray-tproxy-outbound")), WithSubscriptionFetcher(func(context.Context, string, bool) ([]byte, error) { return privateContent, nil }))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	endpoint := "https://provider.example/private-subscription"
	requestBody, _ := json.Marshal(map[string]any{"id": "private-sub", "name": "Private", "enabled": true, "selection": "fixed", "allow_insecure_tls": true, "url": endpoint})
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/subscriptions", string(requestBody), cookie)
	if created.Code != http.StatusOK || strings.Contains(created.Body.String(), endpoint) {
		t.Fatalf("create private subscription status=%d", created.Code)
	}
	refreshed := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/subscriptions/private-sub/refresh", `{}`, cookie)
	if refreshed.Code != http.StatusOK || !strings.Contains(refreshed.Body.String(), `"imported_nodes":1`) || strings.Contains(refreshed.Body.String(), endpoint) {
		t.Fatalf("refresh private subscription status=%d body=%s", refreshed.Code, refreshed.Body.String())
	}
	items, err := server.desiredItems(ctx, "proxy_node")
	if err != nil || len(items) != 1 || stringField(items[0], "subscription_id") != "private-sub" {
		t.Fatalf("imported nodes = %#v, err=%v", items, err)
	}
	secret, err := store.Secret(ctx, "proxy_node", stringField(items[0], "id"), "secret")
	if err != nil || secret == "" {
		t.Fatalf("imported node credential unavailable: %v", err)
	}
	plan, err := server.buildRuntimePlan(ctx, "private-subscription-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.CompiledProxy.XrayRuntime.ConfigPayload.Outbounds) != 1 || plan.CompiledProxy.XrayRuntime.ConfigPayload.Outbounds[0].StreamSettings["realitySettings"] == nil {
		t.Fatalf("private subscription did not compile a Reality outbound")
	}
}

func TestProxyLowCopyGateRejectsProductionEnablement(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-lowcopy-gate-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	underlay := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-underlay","name":"Underlay","enabled":true,"type":"static"}`, cookie)
	if underlay.Code != http.StatusOK {
		t.Fatalf("underlay status=%d body=%s", underlay.Code, underlay.Body.String())
	}
	rejected := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/egresses", `{"id":"proxy-lowcopy","kind":"egress","semantic_type":"proxy_egress","display_list":"wan","proxy_profile_id":"xray-tproxy-outbound","underlay_wan_id":"wan-underlay","low_copy":true}`, cookie)
	if rejected.Code != http.StatusUnprocessableEntity || !strings.Contains(rejected.Body.String(), "poc_failed") {
		t.Fatalf("low-copy status=%d body=%s", rejected.Code, rejected.Body.String())
	}
}

func TestPPPoEStatusLifecycleRedactionAndRouteHandoff(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-pppoe-status-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	wan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-pppoe","name":"PPPoE","enabled":true,"type":"pppoe","interface_id":"eth1","username":"isp-user","password":"isp-secret","pppoe_state":"connected","assigned_ipv4":"198.51.100.44","assigned_ipv6":"2001:db8::44","vpp_table_id":100}`, cookie)
	if wan.Code != http.StatusOK || strings.Contains(wan.Body.String(), "isp-secret") || !strings.Contains(wan.Body.String(), "pppoe_password_redacted") {
		t.Fatalf("pppoe wan status=%d body=%s", wan.Code, wan.Body.String())
	}
	storedPassword, err := store.Secret(ctx, "wan_link", "wan-pppoe", "pppoe_password")
	if err != nil || storedPassword != "isp-secret" {
		t.Fatalf("stored PPPoE password = %q, err=%v", storedPassword, err)
	}
	peers, _, err := server.currentPPPoEPeers(ctx)
	if err != nil || len(peers) != 1 || peers[0].Password != "isp-secret" {
		t.Fatalf("runtime PPPoE peers = %#v, err=%v", peers, err)
	}
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/pppoe/status", "", cookie)
	if status.Code != http.StatusOK {
		t.Fatalf("pppoe status=%d body=%s", status.Code, status.Body.String())
	}
	for _, required := range []string{"connected", "198.51.100.44", "2001:db8::44", "route_ready", "vpp.fib.route"} {
		if !strings.Contains(status.Body.String(), required) {
			t.Fatalf("pppoe status missing %q: %s", required, status.Body.String())
		}
	}
	controller := &httpServiceController{}
	server = New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	login = requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie = login.Result().Cookies()[0]
	disconnect := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/pppoe/disconnect", `{}`, cookie)
	if disconnect.Code != http.StatusOK || !strings.Contains(disconnect.Body.String(), `"runtime_state":"disconnected"`) || !strings.Contains(disconnect.Body.String(), "vpp_route_handoff") || len(controller.stopped) != 1 {
		t.Fatalf("pppoe disconnect status=%d body=%s", disconnect.Code, disconnect.Body.String())
	}
	connect := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/pppoe/connect", `{}`, cookie)
	if connect.Code != http.StatusOK || !strings.Contains(connect.Body.String(), `"runtime_state":"connected"`) || len(controller.applied) != 1 {
		t.Fatalf("pppoe connect status=%d body=%s", connect.Code, connect.Body.String())
	}
}

func TestPPPAddressFromStatusJSON(t *testing.T) {
	ipv4, ipv6, ok := pppAddressFromStatusJSON([]byte(`{"state":"connected","interface":"pppoe_session0","session":{"local_address":"10.255.0.100","local_ipv6_address":"2001:db8::100"}}`))
	if !ok || ipv4 != "10.255.0.100" || ipv6 != "2001:db8::100" {
		t.Fatalf("ppp address = %q %q %v", ipv4, ipv6, ok)
	}
	if _, _, ok := pppAddressFromStatusJSON([]byte(`{"state":"disconnected"}`)); ok {
		t.Fatal("disconnected status was reported as live")
	}
}

func TestPPPACMACFromStatusJSON(t *testing.T) {
	if got := pppACMACFromStatusJSON([]byte(`{"state":"connected","session":{"ac_mac":[0,80,86,181,202,154]}}`)); got != "00:50:56:b5:ca:9a" {
		t.Fatalf("AC MAC = %q", got)
	}
	for _, input := range []string{
		`{"state":"disconnected","session":{"ac_mac":[0,80,86,181,202,154]}}`,
		`{"state":"connected","session":{"ac_mac":[0,80,86]}}`,
		`{"state":"connected","session":{"ac_mac":[0,80,86,181,202,256]}}`,
	} {
		if got := pppACMACFromStatusJSON([]byte(input)); got != "" {
			t.Fatalf("invalid status exposed AC MAC %q for %s", got, input)
		}
	}
}

func TestFullConeNATIsAcceptedForRuntimeCompilation(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-full-cone-gate-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	portMap := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/nat/port-maps", `{"id":"full-cone-map","full_cone":true,"external_address":"203.0.113.10","external_port":9443,"internal_host":"192.168.10.30","internal_port":443,"protocol":"tcp"}`, cookie)
	if portMap.Code != http.StatusOK {
		t.Fatalf("full-cone port map status=%d body=%s", portMap.Code, portMap.Body.String())
	}
	route := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/policies/routes", `{"id":"full-cone-route","enabled":true,"match":{"src_ip":"192.168.1.0/24"},"action":"nat","egress":"wan0","nat_behavior":"full-cone"}`, cookie)
	if route.Code != http.StatusOK {
		t.Fatalf("full-cone route status=%d body=%s", route.Code, route.Body.String())
	}
}

func TestWANAddressChangeReportsNATRebuildImpactAndAudit(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-wan-nat-rebuild-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	wan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan0","name":"WAN0","enabled":true,"type":"dhcp4","interface_id":"eth0","current_address":"198.51.100.10"}`, cookie)
	if wan.Code != http.StatusOK {
		t.Fatalf("wan create status = %d, want 200: %s", wan.Code, wan.Body.String())
	}
	portMap := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/nat/port-maps", `{"id":"nas-https","wan_link":"wan0","external_port":9443,"internal_host":"192.168.10.30","internal_port":443,"protocol":"tcp"}`, cookie)
	if portMap.Code != http.StatusOK {
		t.Fatalf("port map create status = %d, want 200: %s", portMap.Code, portMap.Body.String())
	}
	updated := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/gateway/wan-links/wan0", `{"id":"wan0","name":"WAN0","enabled":true,"type":"dhcp4","interface_id":"eth0","current_address":"198.51.100.20"}`, cookie)
	if updated.Code != http.StatusOK {
		t.Fatalf("wan update status = %d, want 200: %s", updated.Code, updated.Body.String())
	}
	body := updated.Body.String()
	for _, required := range []string{"nat_rebuild", "nat_rebuild_required", "nas-https", "198.51.100.10", "198.51.100.20"} {
		if !strings.Contains(body, required) {
			t.Fatalf("wan update response missing %q: %s", required, body)
		}
	}
	events, err := store.AuditEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "nat_rebuild" && event.Resource == "/api/v1/gateway/nat/port-maps" && strings.Contains(event.Error, "nas-https") {
			found = true
		}
	}
	if !found {
		t.Fatalf("nat rebuild audit event not found: %#v", events)
	}
}

func TestConfigSnapshotRestoreReplacesDesiredState(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-snapshot-restore-replace-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	createWAN := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-snapshot","name":"Snapshot WAN","enabled":true}`, cookie)
	if createWAN.Code != http.StatusOK {
		t.Fatalf("create WAN status = %d, want 200: %s", createWAN.Code, createWAN.Body.String())
	}
	snapshot := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/snapshots", `{"name":"restore-replace"}`, cookie)
	if snapshot.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, want 200: %s", snapshot.Code, snapshot.Body.String())
	}
	var snapshotBody struct {
		Snapshot struct {
			ID string `json:"id"`
		} `json:"snapshot"`
	}
	decode(t, snapshot, &snapshotBody)
	if snapshotBody.Snapshot.ID == "" {
		t.Fatalf("snapshot response missing id: %s", snapshot.Body.String())
	}
	createStale := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-stale","name":"Stale WAN","enabled":true}`, cookie)
	if createStale.Code != http.StatusOK {
		t.Fatalf("create stale status = %d, want 200: %s", createStale.Code, createStale.Body.String())
	}
	restore := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/snapshots/"+snapshotBody.Snapshot.ID+"/restore", `{}`, cookie)
	if restore.Code != http.StatusOK || !strings.Contains(restore.Body.String(), `"status":"restored"`) {
		t.Fatalf("restore status=%d body=%s", restore.Code, restore.Body.String())
	}
	preserved := request(t, server, http.MethodGet, "/api/v1/gateway/wan-links/wan-snapshot")
	if preserved.Code != http.StatusOK {
		t.Fatalf("preserved WAN status = %d, want 200: %s", preserved.Code, preserved.Body.String())
	}
	stale := request(t, server, http.MethodGet, "/api/v1/gateway/wan-links/wan-stale")
	if stale.Code != http.StatusNotFound {
		t.Fatalf("stale WAN status = %d, want 404 after restore: %s", stale.Code, stale.Body.String())
	}
}

func TestObjectGroupImportExportAndReferenceDeleteGuard(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-object-group-import-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	createGroup := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups", `{"id":"obj-import","kind":"ip","name":"Imported IPs","enabled":true,"entries":["192.168.1.1","192.168.1.1"],"remarks":{"192.168.1.1":"router"}}`, cookie)
	if createGroup.Code != http.StatusOK {
		t.Fatalf("create group status = %d, want 200: %s", createGroup.Code, createGroup.Body.String())
	}
	if !strings.Contains(createGroup.Body.String(), "compiled_expansion") || !strings.Contains(createGroup.Body.String(), `"entry_count":1`) {
		t.Fatalf("create group response missing compiled expansion/dedup: %s", createGroup.Body.String())
	}
	duplicateName := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups", `{"id":"obj-import-copy","kind":"ip","name":"Imported IPs","enabled":true,"entries":["192.168.2.1"]}`, cookie)
	if duplicateName.Code != http.StatusUnprocessableEntity || !strings.Contains(duplicateName.Body.String(), "already exists") {
		t.Fatalf("duplicate group status=%d body=%s", duplicateName.Code, duplicateName.Body.String())
	}
	importRes := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups/obj-import/import", `{"text":"10.0.0.1\n10.0.0.0/24\nbad entry"}`, cookie)
	if importRes.Code != http.StatusOK {
		t.Fatalf("import status = %d, want 200: %s", importRes.Code, importRes.Body.String())
	}
	for _, required := range []string{"imported_count", "10.0.0.1", "10.0.0.0/24", "invalid_lines", "bad entry", "compiled_expansion"} {
		if !strings.Contains(importRes.Body.String(), required) {
			t.Fatalf("import response missing %q: %s", required, importRes.Body.String())
		}
	}
	exportRes := request(t, server, http.MethodGet, "/api/v1/objects/groups/obj-import/export")
	if exportRes.Code != http.StatusOK || !strings.Contains(exportRes.Body.String(), "192.168.1.1\\n10.0.0.1\\n10.0.0.0/24") {
		t.Fatalf("export status=%d body=%s", exportRes.Code, exportRes.Body.String())
	}
	policy := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/policies/routes", `{"id":"route-uses-object","enabled":true,"match":{"src_ip":"obj-import"},"action":"route","egress":"wan0"}`, cookie)
	if policy.Code != http.StatusOK {
		t.Fatalf("policy create status = %d, want 200: %s", policy.Code, policy.Body.String())
	}
	updateGroup := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/objects/groups/obj-import", `{"id":"obj-import","kind":"ip","name":"Imported IPs","enabled":true,"entries":["192.168.1.1","10.0.0.1","10.0.0.0/24","10.0.0.0/24"]}`, cookie)
	if updateGroup.Code != http.StatusOK || !strings.Contains(updateGroup.Body.String(), "affected_consumers") || !strings.Contains(updateGroup.Body.String(), "route-uses-object") || !strings.Contains(updateGroup.Body.String(), "recompile_state") {
		t.Fatalf("update group dirty consumers status=%d body=%s", updateGroup.Code, updateGroup.Body.String())
	}
	deleteGroup := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/objects/groups/obj-import", "", cookie)
	if deleteGroup.Code != http.StatusConflict || !strings.Contains(deleteGroup.Body.String(), "object_group_in_use") || !strings.Contains(deleteGroup.Body.String(), "route-uses-object") {
		t.Fatalf("delete referenced group status=%d body=%s", deleteGroup.Code, deleteGroup.Body.String())
	}
}

func TestObjectGroupMultipartImport(t *testing.T) {
	store, err := persistence.Open(context.Background(), "file:httpapi-object-group-multipart-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatal(login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups", `{"id":"obj-multipart","kind":"ip","name":"Multipart","enabled":true}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatal(created.Body.String())
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("kind", "ip")
	_ = writer.WriteField("format", "text")
	_ = writer.WriteField("mode", "overwrite")
	part, err := writer.CreateFormFile("file", "ips.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("192.0.2.1\n192.0.2.0/24\n"))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/objects/groups/obj-multipart/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.AddCookie(cookie)
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"imported_count":2`) {
		t.Fatalf("multipart import status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestRuntimePreviewCompilesDesiredRoutePolicyAndSecurityACLToVPPOperations(t *testing.T) {
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "mgmt0")
	useVPPProof(t, "mgmt0")
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-policy-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	lan := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", `{"id":"lan0","name":"lan0","interface_id":"lan0","gateway_role":"lan","cidr":"192.168.20.1/24"}`, cookie)
	if lan.Code != http.StatusOK {
		t.Fatalf("assign lan0 status = %d: %s", lan.Code, lan.Body.String())
	}
	for _, interfaceName := range []string{"wan0", "wan1"} {
		assigned := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", fmt.Sprintf(`{"id":%q,"name":%q,"interface_id":%q,"gateway_role":"wan"}`, interfaceName, interfaceName, interfaceName), cookie)
		if assigned.Code != http.StatusOK {
			t.Fatalf("assign %s status = %d: %s", interfaceName, assigned.Code, assigned.Body.String())
		}
	}
	wanGroup := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-groups", `{"id":"wan-primary","wan_members":["wan0","wan1"]}`, cookie)
	if wanGroup.Code != http.StatusOK {
		t.Fatalf("create wan group status = %d, want 200: %s", wanGroup.Code, wanGroup.Body.String())
	}
	route := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/policies/routes", `{"id":"office-egress","priority":10,"action":"route","wan_group":"wan-primary","match":{"src_ip":"obj-local-lan","dst_port":"tcp/443"}}`, cookie)
	if route.Code != http.StatusOK {
		t.Fatalf("create route policy status = %d, want 200: %s", route.Code, route.Body.String())
	}
	acl := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/security/acls", `{"id":"guest-block","priority":20,"action":"deny","match":{"src_ip":"192.168.20.0/24","dst_ip":"10.0.0.0/8","protocol":"tcp","direction":"lan_to_wan"}}`, cookie)
	if acl.Code != http.StatusOK {
		t.Fatalf("create security acl status = %d, want 200: %s", acl.Code, acl.Body.String())
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK {
		t.Fatalf("runtime preview status = %d, want 200: %s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{"compiled_policy", "vpp.route-policy", "vpp.security-generation", "vpp.pbr.next-hop-group", "set acl-plugin acl", "ip table add", "show ip fib table", "deny src 192.168.20.0/24 dst 10.0.0.0/8", "via ip4-lookup-in-table", "via lyroute-wan1"} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("runtime preview missing %q: %s", required, preview.Body.String())
		}
	}
	if strings.Contains(preview.Body.String(), `"name":"vpp.security-acl"`) {
		t.Fatalf("runtime preview contains duplicate legacy security ACL operation: %s", preview.Body.String())
	}
}

func TestRuntimePreviewCompilesDomainPolicyFromDomainIPSet(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-domain-route-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan0","name":"WAN 0","enabled":true,"type":"static","gateway":"198.51.100.1","ipv4":{"mode":"static","address":"198.51.100.2/30","gateway":"198.51.100.1"}}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("WAN status=%d body=%s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups", `{"id":"obj-video-domains","kind":"domain","name":"Video Domains","entries":["video.example"]}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("object group status=%d body=%s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/domain-ip-sets", `{"id":"video.example","domain":"video.example","ips":["203.0.113.10"],"expires_at":"2099-06-12T13:00:00Z"}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("domain ip set status=%d body=%s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/policies/routes", `{"id":"route-video-domain","enabled":true,"match":{"domain_group":"obj-video-domains"},"action":"route","egress":"wan0"}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("route policy status=%d body=%s", res.Code, res.Body.String())
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "203.0.113.10") || !strings.Contains(preview.Body.String(), "route-video-domain") {
		t.Fatalf("runtime preview status=%d body=%s", preview.Code, preview.Body.String())
	}
}

func TestRuntimePreviewDNSObservedAnswerOverridesOrdinaryPBR(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-override-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	for _, body := range []string{
		`{"id":"wan-primary","enabled":true,"type":"static","gateway":"198.51.100.1","ipv4":{"mode":"static","address":"198.51.100.2/30","gateway":"198.51.100.1"}}`,
		`{"id":"wan-secondary","enabled":true,"type":"static","gateway":"203.0.113.1","ipv4":{"mode":"static","address":"203.0.113.2/30","gateway":"203.0.113.1"}}`,
	} {
		if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", body, cookie); res.Code != http.StatusOK {
			t.Fatalf("WAN status=%d body=%s", res.Code, res.Body.String())
		}
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/upstreams", `{"id":"dns-primary","enabled":true,"servers":["9.9.9.9"],"wan_egress_id":"wan-primary","interface":"wan-primary","cache_size":32768,"ttl_min_seconds":60,"ttl_max_seconds":600,"prefetch":true}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("DNS upstream status=%d body=%s", res.Code, res.Body.String())
	}
	dnsPolicy := `{"id":"fixed-wan","name":"Fixed WAN","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"updates","domains":["updates.example"],"outcome":{"kind":"direct","wan_egress_id":"wan-primary"}}]}}`
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", dnsPolicy, cookie); res.Code != http.StatusOK {
		t.Fatalf("DNS policy status=%d body=%s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/domain-ip-sets", `{"id":"dns-observed-updates","dns_rule_id":"updates","ips":["203.0.113.53"],"expires_at":"2099-06-12T13:00:00Z"}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("DNS observation status=%d body=%s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/policies/routes", `{"id":"ordinary-pbr","priority":10,"action":"route","egress":"wan-secondary","match":{"dst_ip":"any"}}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("ordinary PBR status=%d body=%s", res.Code, res.Body.String())
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK {
		t.Fatalf("runtime preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{"dns-override-updates", "203.0.113.53", "wan-primary", "ordinary-pbr", "wan-secondary"} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("runtime preview missing %q: %s", required, preview.Body.String())
		}
	}
}

func TestDNSIPSetObservationTriggersGatewayPolicyWithDNSPrecedence(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-observation-runtime-apply-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	useVPPProof(t, "eth0")
	saveExplicitDataInterface(t, store, "wan0", "wan1")
	transaction := &capturingGatewayTransaction{}
	controller := &httpServiceController{}
	server := New(
		WithStore(store),
		WithDNSSyncToken("sync-secret"),
		WithClock(fixedClock()),
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}),
		WithGatewayTransaction(transaction),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	for _, write := range []struct {
		path string
		body string
	}{
		{"/api/v1/gateway/wan-links", `{"id":"wan-primary","interface_id":"wan0","enabled":true,"type":"static","gateway":"198.51.100.1","ipv4":{"mode":"static","address":"198.51.100.2/30","gateway":"198.51.100.1"}}`},
		{"/api/v1/gateway/wan-links", `{"id":"wan-secondary","interface_id":"wan1","enabled":true,"type":"static","gateway":"203.0.113.1","ipv4":{"mode":"static","address":"203.0.113.2/30","gateway":"203.0.113.1"}}`},
		{"/api/v1/dns/upstreams", `{"id":"dns-primary","enabled":true,"servers":["9.9.9.9"],"wan_egress_id":"wan-primary","interface":"wan-primary","cache_size":32768,"ttl_min_seconds":60,"ttl_max_seconds":600,"prefetch":true}`},
		{"/api/v1/dns/policies", `{"id":"fixed-wan","name":"Fixed WAN","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"updates","domains":["updates.example"],"outcome":{"kind":"direct","wan_egress_id":"wan-primary"}}]}}`},
		{"/api/v1/gateway/policies/routes", `{"id":"ordinary-pbr","priority":10,"action":"route","egress":"wan-secondary","match":{"dst_ip":"any"}}`},
	} {
		if res := authenticatedJSONRequest(t, server, http.MethodPost, write.path, write.body, cookie); res.Code != http.StatusOK {
			t.Fatalf("write %s status=%d body=%s", write.path, res.Code, res.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/dns/ipset-observations", strings.NewReader(`{"rule_id":"updates","set_name":"lyroute_dns_updates","members":[{"set_name":"lyroute_dns_updates","ip":"203.0.113.53","expires_at":"2099-06-12T13:00:00Z"}]}`))
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set(HeaderDNSSyncToken, "sync-secret")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"members_applied":1`) {
		t.Fatalf("observation status=%d body=%s", response.Code, response.Body.String())
	}
	if transaction.plan == nil {
		t.Fatalf("DNS observation did not invoke the gateway transaction: %s", response.Body.String())
	}
	var overrideFound, ordinaryFound bool
	for _, route := range transaction.plan.GatewayPlan.Policy.RoutePolicies {
		switch route.ID {
		case "dns-override-updates":
			overrideFound = route.Priority == 0 && route.Priority < 10 && route.Egress == "wan-primary" && len(route.Match.Destinations) == 1 && route.Match.Destinations[0] == "203.0.113.53/32"
		case "ordinary-pbr":
			ordinaryFound = route.Egress == "wan-secondary"
		}
	}
	if !overrideFound || !ordinaryFound {
		t.Fatalf("gateway transaction routes do not preserve DNS precedence: %#v", transaction.plan.GatewayPlan.Policy.RoutePolicies)
	}
}

type capturingGatewayTransaction struct{ plan *apply.Plan }

func (transaction *capturingGatewayTransaction) Run(_ context.Context, plan apply.Plan) (apply.GatewayTransactionResult, error) {
	transaction.plan = &plan
	now := fixedClock()()
	return apply.GatewayTransactionResult{
		Order:     []string{"routes"},
		Receipts:  []apply.ApplyReceipt{{TransactionID: plan.Request.TransactionID, Capability: "routes", Status: apply.ReceiptApplied, AppliedAt: now}},
		Readbacks: []apply.Readback{{TransactionID: plan.Request.TransactionID, Capability: "routes", Timestamp: now, Fresh: true}},
	}, nil
}

func (transaction *capturingGatewayTransaction) Rollback(context.Context, apply.Plan) error {
	return nil
}

func TestDNSIPSetObservationEndpointPersistsOnlyValidatedMembers(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-ipset-observation-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithDNSSyncToken("sync-secret"), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-primary","enabled":true,"type":"static","gateway":"198.51.100.1","ipv4":{"mode":"static","address":"198.51.100.2/30","gateway":"198.51.100.1"}}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("WAN status=%d body=%s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/upstreams", `{"id":"dns-primary","enabled":true,"servers":["9.9.9.9"],"wan_egress_id":"wan-primary","interface":"wan-primary","cache_size":32768,"ttl_min_seconds":60,"ttl_max_seconds":600,"prefetch":true}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("DNS upstream status=%d body=%s", res.Code, res.Body.String())
	}
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", `{"id":"fixed-wan","name":"Fixed WAN","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"updates","domains":["updates.example"],"outcome":{"kind":"direct","wan_egress_id":"wan-primary"}}]}}`, cookie); res.Code != http.StatusOK {
		t.Fatalf("DNS policy status=%d body=%s", res.Code, res.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/dns/ipset-observations", strings.NewReader(`{"rule_id":"updates","set_name":"lyroute_dns_updates","members":[{"set_name":"lyroute_dns_updates","ip":"203.0.113.53","expires_at":"2099-06-12T13:00:00Z"},{"set_name":"wrong","ip":"198.51.100.9","expires_at":"2099-06-12T13:00:00Z"}]}`))
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set(HeaderDNSSyncToken, "sync-secret")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"members_applied":1`) {
		t.Fatalf("observation status=%d body=%s", response.Code, response.Body.String())
	}
	items, err := server.desiredItems(ctx, "domain_ip_set")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || stringField(items[0], "dns_rule_id") != "updates" || stringSliceField(items[0], "ips")[0] != "203.0.113.53" {
		t.Fatalf("persisted observations = %#v", items)
	}
}

func TestInterfaceTelemetryUsesSharedRuntimeSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-interface-snapshot-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	interfaces := request(t, server, http.MethodGet, "/api/v1/interfaces")
	stats := request(t, server, http.MethodGet, "/api/v1/interfaces/ens33/stats")
	telemetry := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/interfaces", "", cookie)
	for label, res := range map[string]*httptest.ResponseRecorder{"interfaces": interfaces, "stats": stats, "telemetry": telemetry} {
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", label, res.Code, res.Body.String())
		}
		for _, required := range []string{"rx_bps", "tx_bps", "runtime_state", "degraded_reason", "vpp_interface_runtime"} {
			if !strings.Contains(res.Body.String(), required) {
				t.Fatalf("%s missing %q: %s", label, required, res.Body.String())
			}
		}
	}
}

func TestInterfaceRuntimeSnapshotBacksInterfacesStatsAndTelemetry(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{{"id": "eth9", "name": "eth9", "active_path": "vpp", "work_mode": "vpp", "rx_bps": 100, "tx_bps": 200}}}), WithClock(fixedClock()))
	interfaces := request(t, server, http.MethodGet, "/api/v1/interfaces")
	if interfaces.Code != http.StatusOK {
		t.Fatalf("interfaces status = %d, want 200: %s", interfaces.Code, interfaces.Body.String())
	}
	for _, required := range []string{"eth9", "work_mode", "stats", "vpp_interface_runtime"} {
		if !strings.Contains(interfaces.Body.String(), required) {
			t.Fatalf("interfaces response missing %q: %s", required, interfaces.Body.String())
		}
	}
	stats := request(t, server, http.MethodGet, "/api/v1/interfaces/eth9/stats")
	if stats.Code != http.StatusOK {
		t.Fatalf("interface stats status = %d, want 200: %s", stats.Code, stats.Body.String())
	}
	if !strings.Contains(stats.Body.String(), `"item"`) || strings.Contains(stats.Body.String(), `"items"`) {
		t.Fatalf("interface stats should return one item, got: %s", stats.Body.String())
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	telemetry := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/interfaces", "", login.Result().Cookies()[0])
	if telemetry.Code != http.StatusOK || !strings.Contains(telemetry.Body.String(), "eth9") || !strings.Contains(telemetry.Body.String(), "vpp_interface_runtime") {
		t.Fatalf("telemetry interfaces response = %d %s", telemetry.Code, telemetry.Body.String())
	}
}

func TestInterfaceRuntimeSnapshotIncludesLinkDownHostInterfaces(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp1s0", "name": "enp1s0", "link_state": "up", "admin_state": "up", "speed": "1000Mb/s", "active_path": "native_auto", "work_mode": "native_auto"},
			{"id": "enp2s0", "name": "enp2s0", "link_state": "down", "admin_state": "up", "speed": "1000Mb/s", "active_path": "native_auto", "work_mode": "native_auto"},
			{"id": "enp3s0", "name": "enp3s0", "link_state": "down", "admin_state": "up", "speed": "1000Mb/s", "active_path": "native_auto", "work_mode": "native_auto"},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp1s0")
	t.Setenv("LY_ROUTE_LAN_INTERFACE", "enp2s0")
	server := New(WithClock(fixedClock()))
	res := request(t, server, http.MethodGet, "/api/v1/interfaces")
	if res.Code != http.StatusOK {
		t.Fatalf("interfaces status = %d, want 200: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{"enp1s0", "enp2s0", "enp3s0", `"link_state":"down"`, `"gateway_role":"management"`, `"work_mode":"kernel_stack"`} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("interfaces response missing %q: %s", required, res.Body.String())
		}
	}
}

func TestRuntimePreviewDoesNotAutomaticallyBindNonManagementPhysicalInterfaces(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{
			{"id": "enp1s0", "name": "enp1s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp2s0", "name": "enp2s0", "link_state": "up", "admin_state": "up"},
			{"id": "enp3s0", "name": "enp3s0", "link_state": "down", "admin_state": "up"},
		}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "enp1s0")
	server := New(WithClock(fixedClock()))
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK {
		t.Fatalf("runtime preview status = %d: %s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{`"dataplane_state":"native_ready"`, `"vpp_operations":[]`} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("runtime preview missing idle dataplane evidence %q: %s", required, preview.Body.String())
		}
	}
	if strings.Contains(preview.Body.String(), "native-driver-") {
		t.Fatalf("runtime preview must not automatically attach any interface: %s", preview.Body.String())
	}
}

func TestInterfaceRuntimeSnapshotUsesLiveTelemetryCollector(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{{"id": "wan0", "name": "wan0", "active_path": "vpp", "rx_bps": 1234, "tx_bps": 5678}}}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	telemetry := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/interfaces", "", login.Result().Cookies()[0])
	if telemetry.Code != http.StatusOK {
		t.Fatalf("telemetry status = %d, want 200: %s", telemetry.Code, telemetry.Body.String())
	}
	for _, required := range []string{"wan0", "1234", "5678", `"degraded":false`, `"state":"available"`} {
		if !strings.Contains(telemetry.Body.String(), required) {
			t.Fatalf("live telemetry response missing %q: %s", required, telemetry.Body.String())
		}
	}
}

func TestFreshDesiredConfigDoesNotAssignLANOrDHCP(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-empty-data-defaults-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	config := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/config/export", "", cookie)
	if config.Code != http.StatusOK {
		t.Fatalf("config export status = %d, want 200: %s", config.Code, config.Body.String())
	}
	for _, unexpected := range []string{`"dhcp_server":[{`, `"interface":[{`, `"gateway":"lan"`, `"dhcp-lan-default"`} {
		if strings.Contains(config.Body.String(), unexpected) {
			t.Fatalf("fresh config should not contain %q: %s", unexpected, config.Body.String())
		}
	}
}

func TestInterfaceRolePatchOverlaysRuntimeSnapshotAndAudit(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-interface-role-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{{"id": "eth1", "name": "eth1", "active_path": "vpp", "rx_bps": 100, "tx_bps": 200}}}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	patch := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/interfaces/eth1/role", `{"gateway_role":"wan"}`, cookie)
	if patch.Code != http.StatusOK || !strings.Contains(patch.Body.String(), `"gateway_role":"wan"`) || !strings.Contains(patch.Body.String(), "candidate_scopes") {
		t.Fatalf("role patch response = %d %s", patch.Code, patch.Body.String())
	}
	interfaces := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/interfaces", "", cookie)
	if interfaces.Code != http.StatusOK || !strings.Contains(interfaces.Body.String(), `"id":"eth1"`) || !strings.Contains(interfaces.Body.String(), `"gateway_role":"wan"`) || !strings.Contains(interfaces.Body.String(), `"work_mode":"vpp"`) {
		t.Fatalf("interfaces after role patch = %d %s", interfaces.Code, interfaces.Body.String())
	}
	audit := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/audit-events", "", cookie)
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), "/api/v1/interfaces/eth1/role") || !strings.Contains(audit.Body.String(), `"status":"success"`) {
		t.Fatalf("audit after role patch = %d %s", audit.Code, audit.Body.String())
	}
}

func TestInterfaceRolePatchRejectsManagementInterface(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-interface-management-role-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "eth0")
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{{"id": "eth0", "name": "eth0", "active_path": "kernel_stack", "work_mode": "kernel_stack"}}}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	patch := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/interfaces/eth0/role", `{"gateway_role":"wan"}`, login.Result().Cookies()[0])
	if patch.Code != http.StatusUnprocessableEntity || !strings.Contains(patch.Body.String(), "management interface cannot be configured as lan or wan") {
		t.Fatalf("management role patch response = %d %s", patch.Code, patch.Body.String())
	}
}

func TestInterfaceBondDeleteRemovesDesiredBond(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-interface-bond-delete-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{
		{"id": "eth1", "name": "eth1", "active_path": "vpp", "work_mode": "vpp", "speed": "10G"},
		{"id": "eth2", "name": "eth2", "active_path": "vpp", "work_mode": "vpp", "speed": "10G"},
	}}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interface-bonds", `{"id":"bond0","name":"bond0","members":["eth1","eth2"]}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create bond status = %d, want 200: %s", created.Code, created.Body.String())
	}
	deleted := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/interface-bonds/bond0", ``, cookie)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete bond status = %d, want 200: %s", deleted.Code, deleted.Body.String())
	}
	listed := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/interface-bonds", ``, cookie)
	if listed.Code != http.StatusOK {
		t.Fatalf("list bond status = %d, want 200: %s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), "bond0") {
		t.Fatalf("deleted bond still listed: %s", listed.Body.String())
	}
}

func TestInterfaceBondPatchRolePreservesMembers(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-interface-bond-patch-role-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{
		{"id": "eth1", "name": "eth1", "active_path": "vpp", "work_mode": "vpp", "speed": "10G"},
		{"id": "eth2", "name": "eth2", "active_path": "vpp", "work_mode": "vpp", "speed": "10G"},
	}}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interface-bonds", `{"id":"bond0","name":"bond0","members":["eth1","eth2"],"work_mode":"af_xdp"}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create bond status = %d, want 200: %s", created.Code, created.Body.String())
	}
	patched := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/interface-bonds/bond0", `{"id":"bond0","name":"bond0","members":["eth1","eth2"],"work_mode":"af_xdp","gateway_role":"wan","mode_role":{"gateway":"wan","bridge":null},"role_configured":true}`, cookie)
	if patched.Code != http.StatusOK {
		t.Fatalf("patch bond status = %d, want 200: %s", patched.Code, patched.Body.String())
	}
	listed := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/interface-bonds", ``, cookie)
	if listed.Code != http.StatusOK {
		t.Fatalf("list bond status = %d, want 200: %s", listed.Code, listed.Body.String())
	}
	body := listed.Body.String()
	for _, required := range []string{"eth1", "eth2", "\"gateway_role\":\"wan\""} {
		if !strings.Contains(body, required) {
			t.Fatalf("patched bond missing %s: %s", required, body)
		}
	}
}

func TestInterfaceBondCreateValidatesMembersAndAudits(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-interface-bond-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithInterfaceTelemetry(fakeInterfaceTelemetry{items: []map[string]any{
		{"id": "eth1", "name": "eth1", "active_path": "vpp", "work_mode": "vpp", "speed": "10G"},
		{"id": "eth2", "name": "eth2", "active_path": "vpp", "work_mode": "vpp", "speed": "10G"},
		{"id": "eth3", "name": "eth3", "active_path": "vpp", "work_mode": "vpp", "speed": "1G"},
		{"id": "eth4", "name": "eth4", "active_path": "vpp", "work_mode": "vpp", "speed": "10G", "bond": "bond-old"},
	}}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interface-bonds", `{"id":"bond0","name":"bond0","members":["eth1","eth2"]}`, cookie)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"mode":"xor"`) || !strings.Contains(created.Body.String(), `"load_balance":"l34"`) || !strings.Contains(created.Body.String(), "vpp.interface-bond") {
		t.Fatalf("bond create response = %d %s", created.Code, created.Body.String())
	}
	mixedSpeed := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interface-bonds", `{"id":"bond-bad","members":["eth1","eth3"]}`, cookie)
	if mixedSpeed.Code != http.StatusBadRequest || !strings.Contains(mixedSpeed.Body.String(), "same speed") {
		t.Fatalf("mixed speed bond response = %d %s", mixedSpeed.Code, mixedSpeed.Body.String())
	}
	alreadyBound := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interface-bonds", `{"id":"bond-bound","members":["eth1","eth4"]}`, cookie)
	if alreadyBound.Code != http.StatusConflict || !strings.Contains(alreadyBound.Body.String(), "already bound") {
		t.Fatalf("already bound response = %d %s", alreadyBound.Code, alreadyBound.Body.String())
	}
	audit := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/audit-events", "", cookie)
	for _, required := range []string{"/api/v1/interface-bonds", `"action":"create"`, `"status":"success"`, `"status":"failure"`} {
		if !strings.Contains(audit.Body.String(), required) {
			t.Fatalf("bond audit missing %q: %s", required, audit.Body.String())
		}
	}
}

func TestDHCPLeaseCollectorBacksLeasesAndOnlineUsers(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithDHCPLeases(fakeDHCPLeases{items: []map[string]any{{"ip_address": "192.168.88.100", "mac": "00:11:22:33:44:55", "hostname": "client-a", "lease_start": "2026-06-11T08:00:00Z", "lease_end": "2026-06-11T20:00:00Z", "last_traffic_time": "2026-06-11T08:10:00Z", "rx_bps": 1234, "tx_bps": 5678, "rx_bytes": 100000, "tx_bytes": 200000}}}))
	leases := request(t, server, http.MethodGet, "/api/v1/dhcp/leases")
	if leases.Code != http.StatusOK || !strings.Contains(leases.Body.String(), "192.168.88.100") || !strings.Contains(leases.Body.String(), `"state":"available"`) {
		t.Fatalf("dhcp leases response = %d %s", leases.Code, leases.Body.String())
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	online := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/online-users", "", login.Result().Cookies()[0])
	for _, required := range []string{"client-a", "kea_leases", "vpp_user_traffic", `"available":true`, `"online_status":"online"`, `"lease_start":"2026-06-11T08:00:00Z"`, `"lease_end":"2026-06-11T20:00:00Z"`, `"last_traffic_time":"2026-06-11T08:10:00Z"`, `"rx_bps":1234`, `"tx_bps":5678`, `"rx_bytes":100000`, `"tx_bytes":200000`} {
		if online.Code != http.StatusOK || !strings.Contains(online.Body.String(), required) {
			t.Fatalf("online users response missing %q: %d %s", required, online.Code, online.Body.String())
		}
	}
}

func TestDHCPLeasesFallbackIsExplicitlyDegraded(t *testing.T) {
	server := New()
	res := request(t, server, http.MethodGet, "/api/v1/dhcp/leases")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Kea lease collector is not configured") || !strings.Contains(res.Body.String(), `"available":false`) {
		t.Fatalf("dhcp fallback response = %d %s", res.Code, res.Body.String())
	}
}

func TestDHCPLeaseReserveCreatesStaticBindingAndAudit(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dhcp-reserve-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithDHCPLeases(fakeDHCPLeases{items: []map[string]any{{"id": "lease-a", "ip_address": "192.168.88.101", "mac": "00:11:22:33:44:66", "hostname": "workstation"}}}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	reserved := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dhcp/leases/lease-a/reserve", `{}`, cookie)
	if reserved.Code != http.StatusOK || !strings.Contains(reserved.Body.String(), "192.168.88.101") || !strings.Contains(reserved.Body.String(), "00:11:22:33:44:66") || !strings.Contains(reserved.Body.String(), "workstation") {
		t.Fatalf("reserve response = %d %s", reserved.Code, reserved.Body.String())
	}
	bindings := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/dhcp/static-bindings", "", cookie)
	if bindings.Code != http.StatusOK || !strings.Contains(bindings.Body.String(), "192.168.88.101") || !strings.Contains(bindings.Body.String(), "00:11:22:33:44:66") {
		t.Fatalf("static bindings response = %d %s", bindings.Code, bindings.Body.String())
	}
	audit := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/audit-events", "", cookie)
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), "/api/v1/dhcp/leases/lease-a/reserve") || !strings.Contains(audit.Body.String(), `"action":"reserve"`) || !strings.Contains(audit.Body.String(), `"status":"success"`) {
		t.Fatalf("audit response = %d %s", audit.Code, audit.Body.String())
	}
}

func TestTopTelemetryCollectorBacksTopSessionsAndDomains(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithTopTelemetry(fakeTopTelemetry{sessions: []map[string]any{{"src_ip": "192.168.88.10", "dst_ip": "8.8.8.8", "bytes": 4096}}, domains: []map[string]any{{"domain": "example.com", "queries": 17}}}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	sessions := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/top-sessions", "", cookie)
	if sessions.Code != http.StatusOK || !strings.Contains(sessions.Body.String(), "192.168.88.10") || !strings.Contains(sessions.Body.String(), `"bytes":4096`) || !strings.Contains(sessions.Body.String(), `"state":"available"`) {
		t.Fatalf("top sessions response = %d %s", sessions.Code, sessions.Body.String())
	}
	domains := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/top-domains", "", cookie)
	if domains.Code != http.StatusOK || strings.Contains(domains.Body.String(), "example.com") || !strings.Contains(domains.Body.String(), "SmartDNS collector") || !strings.Contains(domains.Body.String(), `"state":"unavailable"`) {
		t.Fatalf("Gateway top domains response = %d %s", domains.Code, domains.Body.String())
	}
}

func TestTopTelemetryWithoutCollectorReportsExplicitDegradedCapability(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/top-domains", "", login.Result().Cookies()[0])
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "SmartDNS collector") || !strings.Contains(res.Body.String(), `"degraded":true`) {
		t.Fatalf("top domains degraded response = %d %s", res.Code, res.Body.String())
	}
}

type fakeInterfaceTelemetry struct {
	items []map[string]any
}

func (collector fakeInterfaceTelemetry) Interfaces(context.Context) ([]map[string]any, error) {
	return collector.items, nil
}

type fakeDHCPLeases struct {
	items []map[string]any
}

func (collector fakeDHCPLeases) Leases(context.Context) ([]map[string]any, error) {
	return collector.items, nil
}

type fakeVPPCounters struct {
	dashboard  map[string]any
	policyHits []map[string]any
}

func (collector fakeVPPCounters) Dashboard(context.Context) (map[string]any, error) {
	return collector.dashboard, nil
}

func (collector fakeVPPCounters) PolicyHits(context.Context) ([]map[string]any, error) {
	return collector.policyHits, nil
}

type fakeTopTelemetry struct {
	sessions []map[string]any
	domains  []map[string]any
}

func (collector fakeTopTelemetry) TopSessions(context.Context) ([]map[string]any, error) {
	return collector.sessions, nil
}

func (collector fakeTopTelemetry) TopDomains(context.Context) ([]map[string]any, error) {
	return collector.domains, nil
}

func TestRuntimePreviewRedactsPPPoESecrets(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-preview-pppoe-secret-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	payload, hash, err := persistence.MarshalPayload(map[string]any{"id": "wan-pppoe", "type": "pppoe", "interface_id": "eth0", "username": "pppoe-user", "password": "pppoe-secret", "mtu": 1460, "mru": 1460})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(ctx, persistence.ConfigDocument{ResourceType: "wan_link", ResourceID: "wan-pppoe", Payload: payload, PayloadHash: hash, UpdatedAt: fixedClock()().UTC()}); err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store), WithClock(fixedClock()))
	res := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if res.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, required := range []string{"pppoe_peers", "wan-pppoe", "pppoe-user", `"mtu":1460`, `"mru":1460`} {
		if !strings.Contains(body, required) {
			t.Fatalf("preview response missing %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{"pppoe-secret", "\"password\"", "\"content\"", "\"audit_summary\""} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("preview response leaked %q: %s", forbidden, body)
		}
	}
}

func TestFactoryLANInterfaceDoesNotCreateDataDefaults(t *testing.T) {
	t.Setenv("LY_ROUTE_LAN_INTERFACE", "ens192")
	server := New()
	interfaces := request(t, server, http.MethodGet, "/api/v1/interfaces")
	if interfaces.Code != http.StatusOK || strings.Contains(interfaces.Body.String(), `"gateway":"lan"`) {
		t.Fatalf("interfaces response = %d %s", interfaces.Code, interfaces.Body.String())
	}
	dhcp := request(t, server, http.MethodGet, "/api/v1/dhcp/servers")
	if dhcp.Code != http.StatusOK || strings.Contains(dhcp.Body.String(), `"interface_id":"ens192"`) {
		t.Fatalf("dhcp response = %d %s", dhcp.Code, dhcp.Body.String())
	}
}

func TestManagementNetworkCanBeReadAndUpdated(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-management-network-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	t.Setenv("LY_ROUTE_LAN_INTERFACE", "enp1s0")
	t.Setenv("LY_ROUTE_LAN_CIDR", "10.1.18.125/24")
	t.Setenv("LY_ROUTE_MANAGEMENT_GATEWAY", "10.1.18.1")
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	get := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/management/network", "", cookie)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"interface_id":"enp1s0"`) || !strings.Contains(get.Body.String(), `"cidr":"10.1.18.125/24"`) || !strings.Contains(get.Body.String(), `"gateway":"10.1.18.1"`) {
		t.Fatalf("management network get = %d %s", get.Code, get.Body.String())
	}
	invalid := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/management/network", `{"interface_id":"enp1s0","cidr":"192.168.99.1/24","gateway":"10.0.0.1","confirm_change":true}`, cookie)
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "gateway must be inside management subnet") {
		t.Fatalf("management network invalid = %d %s", invalid.Code, invalid.Body.String())
	}
	spoofed := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/management/network", `{"interface_id":"enp9s0","cidr":"192.168.99.1/24","confirm_change":true}`, cookie)
	if spoofed.Code != http.StatusUnprocessableEntity || !strings.Contains(spoofed.Body.String(), "management interface is immutable") {
		t.Fatalf("management interface spoof = %d %s", spoofed.Code, spoofed.Body.String())
	}
	post := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/management/network", `{"interface_id":"enp1s0","cidr":"192.168.99.1/24","gateway":"192.168.99.254","dhcp_pool_start":"192.168.99.100","dhcp_pool_end":"192.168.99.199","dns":"192.168.99.1","confirm_change":true}`, cookie)
	if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), `"cidr":"192.168.99.1/24"`) || !strings.Contains(post.Body.String(), `"runtime_state":"desired_not_applied"`) {
		t.Fatalf("management network post = %d %s", post.Code, post.Body.String())
	}
}

func TestRuntimeApplyWithoutConfiguredServiceRuntimeReportsUnavailableAndAudits(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-apply-unavailable-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	unauthorized := requestBody(t, server, http.MethodPost, "/api/v1/runtime/apply", `{}`)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized apply status = %d, want 401: %s", unauthorized.Code, unauthorized.Body.String())
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/runtime/apply", `{}`, login.Result().Cookies()[0])
	if res.Code != http.StatusAccepted {
		t.Fatalf("apply status = %d, want 202: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{"unavailable", "service runtime controller is not configured", "smartdns", "xray", "persistence"} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("unavailable apply response missing %q: %s", required, res.Body.String())
		}
	}
	events, err := store.AuditEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Resource == "/api/v1/runtime/apply" && event.Action == "apply" && event.Status == "failure" {
			found = true
		}
	}
	if !found {
		t.Fatalf("runtime apply failure audit not persisted: %#v", events)
	}
}

func TestRuntimeApplyUsesConfiguredServiceRuntimeAndStatusExposesLastApply(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-apply-success-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	saveTestProxyNode(t, store)
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
		serviceRuntime.SmartDNS: {Service: serviceRuntime.SmartDNS, Available: true},
		serviceRuntime.Kea:      {Service: serviceRuntime.Kea, Available: true},
		serviceRuntime.Xray:     {Service: serviceRuntime.Xray, Available: true},
		serviceRuntime.PPPd:     {Service: serviceRuntime.PPPd, Available: true},
		serviceRuntime.VPP:      {Service: serviceRuntime.VPP, Available: true},
	}}
	receiptPath := filepath.Join(t.TempDir(), "vpp-apply-receipt.json")
	server := New(
		WithStore(store),
		WithClock(fixedClock()),
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}),
		WithVPPReceiptPath(receiptPath),
		WithProxyEgress(testProxyEgressWithNode()),
		WithFlowIntent(flow.NewIntent("default", []flow.Rule{
			flow.NewRule("classify-default", flow.RuleGranularity, flow.Classify("best-effort")),
		})),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	applyRes := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/runtime/apply", `{}`, login.Result().Cookies()[0])
	if applyRes.Code != http.StatusAccepted {
		t.Fatalf("runtime apply status = %d, want 202 degraded until live dataplane adapters are configured: %s", applyRes.Code, applyRes.Body.String())
	}
	for _, required := range []string{"degraded", "runtime apply completed with degraded components"} {
		if !strings.Contains(applyRes.Body.String(), required) {
			t.Fatalf("runtime apply response missing %q: %s", required, applyRes.Body.String())
		}
	}
	if got := strings.Join(controller.applied, ","); got != "smartdns,xray" {
		t.Fatalf("applied services = %s, want management/service plane only", got)
	}
	if err := os.WriteFile(receiptPath, []byte(`{"status":"applied","dry_run":false,"operations":[{"name":"vpp.dataplane.attach","resource":"ly-route-lan","results":[{"command":"create interface af_xdp host-if ens32 name ly-route-lan zero-copy","status":"applied","hook":"af_xdp","mode":"zero_copy"}]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	status := request(t, server, http.MethodGet, "/api/v1/runtime/status")
	if status.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", status.Code, status.Body.String())
	}
	for _, required := range []string{"last_apply", "degraded", "unavailable", "persistence", "apply_receipt", "readback_at", "affected_capability"} {
		if !strings.Contains(status.Body.String(), required) {
			t.Fatalf("status response missing %q: %s", required, status.Body.String())
		}
	}
}

func TestRuntimePreviewIncludesLANBridgeVPPOperation(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-lan-bridge-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	bridge := `{"id":"lan-bridge-1","name":"lan桥1","kind":"lan_bridge","type":"lan_bridge","gateway_role":"lan","bridge_members":["eth1","eth2"],"cidr":"192.168.30.1/24","dns_servers":["192.168.30.1"]}`
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", bridge, cookie); res.Code != http.StatusOK {
		t.Fatalf("create LAN bridge status=%d body=%s", res.Code, res.Body.String())
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "vpp.l2.bridge-domain") || !strings.Contains(preview.Body.String(), "lan-bridge-1") {
		t.Fatalf("runtime preview missing LAN bridge VPP operation: status=%d body=%s", preview.Code, preview.Body.String())
	}
}

func TestRuntimePreviewLocksLANBridgeContainingManagementInterface(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-management-bridge-preview-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	bridge := `{"id":"lan-bridge-1","kind":"lan_bridge","type":"lan_bridge","gateway_role":"lan","bridge_members":["eth0","eth1"],"cidr":"192.168.30.1/24"}`
	if res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/interfaces", bridge, login.Result().Cookies()[0]); res.Code != http.StatusOK {
		t.Fatalf("create bridge status=%d body=%s", res.Code, res.Body.String())
	}
	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"dataplane_state":"dataplane_locked"`) || !strings.Contains(preview.Body.String(), "management_excluded_from_operations") {
		t.Fatalf("management bridge did not lock: status=%d body=%s", preview.Code, preview.Body.String())
	}
	if !strings.Contains(preview.Body.String(), `"vpp_operations":null`) {
		t.Fatalf("management bridge emitted VPP operations: %s", preview.Body.String())
	}
}

func TestRuntimeApplyProofRefreshUsesExplicitAssignments(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "eth0", "name": "eth0"}, {"id": "eth1", "name": "eth1"}}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-vpp-proof-refresh-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := fixedClock()().UTC()
	if err := store.SaveConfig(ctx, configDocument(t, "interface", "eth1", map[string]any{"id": "eth1", "interface_id": "eth1", "gateway_role": "lan"}, now)); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	proofPath := filepath.Join(directory, "proof.json")
	probePath := filepath.Join(directory, "probe.sh")
	probe := "#!/bin/sh\nset -eu\nprintf '{\"management_interface\":\"%s\",\"management_shared\":\"%s\",\"interfaces\":\"%s\"}\\n' \"$LY_ROUTE_MANAGEMENT_INTERFACE\" \"$LY_ROUTE_MANAGEMENT_SHARED\" \"$LY_ROUTE_VPP_DATA_INTERFACES\" > \"$LY_ROUTE_VPP_CAPABILITY_PROOF\"\n"
	if err := os.WriteFile(probePath, []byte(probe), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_VPP_PROBE_COMMAND", probePath)
	t.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", proofPath)
	server := New(WithStore(store), WithClock(fixedClock()))

	if err := server.refreshVPPNativeProof(ctx); err != nil {
		t.Fatal(err)
	}
	proofBytes, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatal(err)
	}
	proofText := string(proofBytes)
	if !strings.Contains(proofText, `"management_interface":"eth0"`) || !strings.Contains(proofText, `"management_shared":"false"`) || !strings.Contains(proofText, `"interfaces":"eth1"`) {
		t.Fatalf("refreshed proof input = %s", proofText)
	}
}

func TestRuntimeApplyProofRefreshCarriesSharedManagementAssignment(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any { return []map[string]any{{"id": "eth0", "name": "eth0"}} }
	t.Cleanup(func() { hostInterfaceInventory = previous })
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-vpp-shared-proof-refresh-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := fixedClock()().UTC()
	if err := store.SaveConfig(ctx, configDocument(t, "management_network", "management-network", map[string]any{"id": "management-network", "interface_id": "eth0", "mode": "shared_lan", "cidr": "10.10.10.254/24"}, now)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(ctx, configDocument(t, "interface", "eth0", map[string]any{"id": "eth0", "interface_id": "eth0", "gateway_role": "lan"}, now)); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	proofPath := filepath.Join(directory, "proof.json")
	probePath := filepath.Join(directory, "probe.sh")
	probe := "#!/bin/sh\nset -eu\nprintf '{\"management_interface\":\"%s\",\"management_shared\":\"%s\",\"interfaces\":\"%s\"}\\n' \"$LY_ROUTE_MANAGEMENT_INTERFACE\" \"$LY_ROUTE_MANAGEMENT_SHARED\" \"$LY_ROUTE_VPP_DATA_INTERFACES\" > \"$LY_ROUTE_VPP_CAPABILITY_PROOF\"\n"
	if err := os.WriteFile(probePath, []byte(probe), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_VPP_PROBE_COMMAND", probePath)
	t.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", proofPath)
	server := New(WithStore(store), WithClock(fixedClock()))

	if err := server.refreshVPPNativeProof(ctx); err != nil {
		t.Fatal(err)
	}
	proofBytes, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatal(err)
	}
	proofText := string(proofBytes)
	if !strings.Contains(proofText, `"management_interface":"eth0"`) || !strings.Contains(proofText, `"management_shared":"true"`) || !strings.Contains(proofText, `"interfaces":"eth0"`) {
		t.Fatalf("shared refreshed proof input = %s", proofText)
	}
}

func TestRuntimeStatusReportsVPPUnavailableWhileDataplaneLocked(t *testing.T) {
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
		serviceRuntime.SmartDNS: {Service: serviceRuntime.SmartDNS, Available: true},
		serviceRuntime.Kea:      {Service: serviceRuntime.Kea, Available: true},
		serviceRuntime.Xray:     {Service: serviceRuntime.Xray, Available: true},
		serviceRuntime.PPPd:     {Service: serviceRuntime.PPPd, Available: true},
		serviceRuntime.VPP:      {Service: serviceRuntime.VPP, Available: true},
	}}
	server := New(
		WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}),
		WithVPPReceiptPath(filepath.Join(t.TempDir(), "missing-vpp-receipt.json")),
	)
	res := request(t, server, http.MethodGet, "/api/v1/runtime/status")
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{"vpp", "unavailable", "no VPP operations rendered"} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("status response missing %q: %s", required, res.Body.String())
		}
	}
}

func TestRuntimeStatusCarriesReceiptReadbackFreshnessAndCapability(t *testing.T) {
	server := New(WithClock(fixedClock()), WithVPPReceiptPath(filepath.Join(t.TempDir(), "missing-receipt.json")))
	res := request(t, server, http.MethodGet, "/api/v1/runtime/status")
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", res.Code, res.Body.String())
	}
	var body struct {
		Components []RuntimeComponentState `json:"components"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Components) == 0 {
		t.Fatal("status returned no components")
	}
	for _, component := range body.Components {
		if component.TransactionID == "" || component.Capability == "" || component.ApplyReceipt.Status == "" || component.ReadbackAt.IsZero() || component.Fresh {
			t.Fatalf("component lacks explicit incomplete evidence: %#v", component)
		}
	}
}

func TestRuntimePreviewReportsNativeReadyWithoutForwardingOperations(t *testing.T) {
	// Given
	server := New(WithClock(fixedClock()))

	// When
	res := request(t, server, http.MethodGet, "/api/v1/runtime/preview")

	// Then
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{`"dataplane_state":"native_ready"`, `"vpp_operations":[]`} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("preview missing %q: %s", required, res.Body.String())
		}
	}
}

func TestRuntimePreviewRequiresAndSelectsProductionSmartQoS(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-dpdk-hqos-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	previousInventory := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "eth0", "name": "eth0"}, {"id": "eth1", "name": "eth1"}, {"id": "eth2", "name": "eth2"}}
	}
	t.Cleanup(func() { hostInterfaceInventory = previousInventory })
	for _, document := range []persistence.ConfigDocument{
		configDocument(t, "interface", "eth1", map[string]any{"id": "eth1", "interface_id": "eth1", "gateway_role": "lan", "cidr": "192.168.10.1/24", "smart_qos_download_kbps": 100000}, fixedClock()()),
		configDocument(t, "interface", "eth2", map[string]any{"id": "eth2", "interface_id": "eth2", "gateway_role": "wan", "cidr": "203.0.113.2/30", "smart_qos_upload_kbps": 50000}, fixedClock()()),
	} {
		if err := store.SaveConfig(ctx, document); err != nil {
			t.Fatal(err)
		}
	}
	now := fixedClock()().UTC()
	proofPath := filepath.Join(t.TempDir(), "proof.json")
	proof := fmt.Sprintf(`{"management_interface":"eth0","proofs":[{"linux_interface":"eth1","candidates":[{"tier":"vpp_dpdk","hook":"dpdk","mode":"vfio_pci","source":"runtime_probe","runtime_verified":true,"native":false,"high_performance":true,"observed_at":%q,"valid_until":%q,"performance_score":80,"pci_address":"0000:03:00.0","kernel_driver":"ixgbe","iommu_group":"17","iommu_protected":true,"vfio_available":true,"hugepages_available":true,"dpdk_plugin_available":true,"smart_qos_plugin_available":true}]},{"linux_interface":"eth2","candidates":[{"tier":"vpp_dpdk","hook":"dpdk","mode":"vfio_pci","source":"runtime_probe","runtime_verified":true,"native":false,"high_performance":true,"observed_at":%q,"valid_until":%q,"performance_score":80,"pci_address":"0000:04:00.0","kernel_driver":"ixgbe","iommu_group":"18","iommu_protected":true,"vfio_available":true,"hugepages_available":true,"dpdk_plugin_available":true,"smart_qos_plugin_available":true}]}]}`, now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339), now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(proofPath, []byte(proof), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", proofPath)
	server := New(
		WithStore(store),
		WithClock(fixedClock()),
		WithSmartQoSRuntime(fakeSmartQoSObserver{}, true),
		WithGatewayTransaction(preparedGatewayTransaction{}),
	)

	preview := request(t, server, http.MethodGet, "/api/v1/runtime/preview")
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"dataplane_state":"smart_qos_ready"`) {
		t.Fatalf("smart QoS preview = %d %s", preview.Code, preview.Body.String())
	}
	if !strings.Contains(preview.Body.String(), `"tier":"vpp_dpdk"`) || strings.Contains(preview.Body.String(), `"vpp_operations":null`) {
		t.Fatalf("DPDK operations not carried into smart QoS runtime plan: %s", preview.Body.String())
	}
	for _, required := range []string{"set ly-route smart-qos interface lyroute-eth1 rate 100000 host-isolation destination", "set ly-route smart-qos interface lyroute-eth2 rate 50000 host-isolation source"} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("smart QoS preview missing %q: %s", required, preview.Body.String())
		}
	}
}

type preparedGatewayTransaction struct{}

func (preparedGatewayTransaction) Run(context.Context, apply.Plan) (apply.GatewayTransactionResult, error) {
	return apply.GatewayTransactionResult{}, nil
}

func (preparedGatewayTransaction) Rollback(context.Context, apply.Plan) error { return nil }

func (preparedGatewayTransaction) GatewayResourceNames() []string { return nil }

func (preparedGatewayTransaction) HasDataplaneController() bool { return true }

func TestRuntimeStatusReconcilesIncompletePersistedApplyAsDegraded(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-reconcile-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	payload, hash, err := persistence.MarshalPayload(RuntimeApplyResult{Status: "committed", RuntimeState: "running", TransactionID: "txn-incomplete", AppliedAt: fixedClock()()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveApply(ctx, persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: "snapshot-txn-incomplete", SourceTransactionID: "txn-incomplete", Payload: payload, PayloadHash: hash, CreatedAt: fixedClock()()}}); err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store), WithClock(fixedClock()))
	res := request(t, server, http.MethodGet, "/api/v1/runtime/status")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "incomplete persisted runtime evidence") || !strings.Contains(res.Body.String(), "degraded") {
		t.Fatalf("reconciliation response = %d %s", res.Code, res.Body.String())
	}
}

func TestRuntimeStatusReconcilesStaleReadbackAsDegraded(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-runtime-stale-reconcile-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := fixedClock()()
	transactionID := "txn-stale"
	result := RuntimeApplyResult{
		Status:        "committed",
		RuntimeState:  "running",
		TransactionID: transactionID,
		Receipt:       apply.ApplyReceipt{TransactionID: transactionID, Capability: "runtime", Status: "applied", AppliedAt: now.Add(-10 * time.Minute)},
		Readback:      apply.Readback{TransactionID: transactionID, Capability: "runtime", Timestamp: now.Add(-10 * time.Minute), Fresh: true},
		AppliedAt:     now.Add(-10 * time.Minute),
	}
	payload, hash, err := persistence.MarshalPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveApply(ctx, persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: "snapshot-txn-stale", SourceTransactionID: transactionID, Payload: payload, PayloadHash: hash, CreatedAt: result.AppliedAt}}); err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store), WithClock(fixedClock()))
	res := request(t, server, http.MethodGet, "/api/v1/runtime/status")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "stale persisted runtime readback") || strings.Contains(res.Body.String(), `"state":"running"`) {
		t.Fatalf("stale reconciliation response = %d %s", res.Code, res.Body.String())
	}
}

func TestRuntimeStatusReconcilesSuccessfulConfigApplyReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-config-apply-reconcile-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	saveExplicitDataInterface(t, store, "eth1")
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}}
	server := New(WithStore(store), WithClock(fixedClock()), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	applyResponse := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/apply", `{}`, login.Result().Cookies()[0])
	if applyResponse.Code != http.StatusOK {
		t.Fatalf("config apply = %d %s", applyResponse.Code, applyResponse.Body.String())
	}
	var applied struct {
		TransactionID string `json:"transaction_id"`
	}
	if err := json.Unmarshal(applyResponse.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}

	restarted := New(WithStore(store), WithClock(fixedClock()), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	status := request(t, restarted, http.MethodGet, "/api/v1/runtime/status")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), applied.TransactionID) || strings.Contains(status.Body.String(), "incomplete persisted runtime evidence") {
		t.Fatalf("restarted status = %d %s", status.Code, status.Body.String())
	}
}

func TestGatewayConfigApplyTransactionReturnsDataplaneLockedWithoutCommittingGeneration(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-config-apply-locked-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}}
	t.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", filepath.Join(t.TempDir(), "missing-proof.json"))
	server := New(WithStore(store), WithClock(fixedClock()), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	before, err := store.RuntimeSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/config/apply", `{}`, login.Result().Cookies()[0])
	after, err := store.RuntimeSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	management := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/management/network", "", login.Result().Cookies()[0])

	// Then
	if response.Code != http.StatusLocked {
		t.Fatalf("config apply status = %d, want 423: %s", response.Code, response.Body.String())
	}
	for _, required := range []string{`"status":"dataplane_locked"`, `"runtime_state":"degraded"`, "data_assignment_present"} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("locked response missing %q: %s", required, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), `"status":"committed"`) || len(controller.applied) != 0 || len(after) != len(before) {
		t.Fatalf("locked apply mutated forwarding state: response=%s applied=%#v snapshots=%d->%d", response.Body.String(), controller.applied, len(before), len(after))
	}
	if management.Code != http.StatusOK || !strings.Contains(management.Body.String(), `"interface_id":"eth0"`) {
		t.Fatalf("management became unreachable: %d %s", management.Code, management.Body.String())
	}
}

func TestRuntimeApplySerializesConcurrentTransactions(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	server.runtimeApplyMu.Lock()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_ = authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/runtime/apply", `{}`, cookie)
		close(done)
	}()
	<-started
	select {
	case <-done:
		server.runtimeApplyMu.Unlock()
		t.Fatal("runtime apply bypassed transaction serialization")
	default:
	}
	server.runtimeApplyMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serialized runtime apply did not resume")
	}
}

func TestPolicyHitsUseVPPApplyReceiptReadback(t *testing.T) {
	receiptPath := filepath.Join(t.TempDir(), "vpp-apply-receipt.json")
	receipt := `{"status":"applied","dry_run":false,"operations":[{"name":"vpp.route-policy","resource":"route-lan-direct","results":[{"command":"set ip table 100","status":"applied"},{"command":"show ip fib table 100","status":"applied"}]},{"name":"vpp.security-acl","resource":"acl-wan-deny","results":[{"command":"set acl-plugin acl index 10 deny","status":"applied"}]},{"name":"vpp.dataplane.attach","resource":"ly-route-lan","results":[{"command":"create interface af_xdp host-if eth1 name ly-route-lan zero-copy","status":"applied","hook":"af_xdp","mode":"zero_copy"}]}]}`
	if err := os.WriteFile(receiptPath, []byte(receipt), 0600); err != nil {
		t.Fatal(err)
	}
	server := New(WithVPPReceiptPath(receiptPath), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/policy-hits", "", login.Result().Cookies()[0])
	if res.Code != http.StatusOK {
		t.Fatalf("policy hits status = %d, want 200: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, required := range []string{"vpp_policy_readback", "route-lan-direct", "acl-wan-deny", "applied_commands", "hit_source", "vpp_apply_receipt"} {
		if !strings.Contains(body, required) {
			t.Fatalf("policy hits response missing %q: %s", required, body)
		}
	}
	if strings.Contains(body, "ly-route-lan") {
		t.Fatalf("policy hits should not include dataplane probe operations: %s", body)
	}
}

func TestVPPCounterCollectorBacksDashboardPolicyHitsAndTrafficTrend(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithVPPCounters(fakeVPPCounters{dashboard: map[string]any{"sessions": 42, "throughput_bps": 123456, "proxy_egress_bps": 6543}, policyHits: []map[string]any{{"id": "route-video", "hits": 99}}}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	dashboard := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/dashboard", "", cookie)
	if dashboard.Code != http.StatusOK || !strings.Contains(dashboard.Body.String(), `"sessions":42`) || !strings.Contains(dashboard.Body.String(), "vpp_counters") {
		t.Fatalf("dashboard response = %d %s", dashboard.Code, dashboard.Body.String())
	}
	hits := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/policy-hits", "", cookie)
	if hits.Code != http.StatusOK || !strings.Contains(hits.Body.String(), "route-video") || !strings.Contains(hits.Body.String(), `"hits":99`) || !strings.Contains(hits.Body.String(), "vpp_counter_collector") {
		t.Fatalf("policy hits response = %d %s", hits.Code, hits.Body.String())
	}
	trend := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/telemetry/traffic-trend?window=24h&points=12", "", cookie)
	for _, required := range []string{`"degraded":true`, `"points":12`, `"sampling_interval_seconds":300`, `"logical_egresses":[]`, `"state":"unavailable"`, "gateway telemetry collector is not configured"} {
		if trend.Code != http.StatusOK || !strings.Contains(trend.Body.String(), required) {
			t.Fatalf("traffic trend response missing %q: %d %s", required, trend.Code, trend.Body.String())
		}
	}
}

func TestRuntimeStatusDistinguishesRenderedRunningAndUnavailable(t *testing.T) {
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
		serviceRuntime.SmartDNS: {Service: serviceRuntime.SmartDNS, Available: true},
		serviceRuntime.Xray:     {Service: serviceRuntime.Xray, Available: false, Reason: "xray inactive"},
	}}
	server := New(WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	res := request(t, server, http.MethodGet, "/api/v1/runtime/status")
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, required := range []string{"degraded", "xray inactive", "unavailable", "local store is not configured", "apply_receipt", "fresh"} {
		if !strings.Contains(body, required) {
			t.Fatalf("status response missing %q: %s", required, body)
		}
	}
	if strings.Contains(body, `"state":"running"`) {
		t.Fatalf("status claims running without complete receipt/readback: %s", body)
	}
}

func TestCapabilitiesRouteReportsExplicitRuntimeDegradation(t *testing.T) {
	store, err := persistence.Open(context.Background(), "file:httpapi-capabilities-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	res := request(t, New(WithStore(store)), http.MethodGet, "/api/v1/capabilities")
	if res.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, want 200: %s", res.Code, res.Body.String())
	}
	payload := res.Body.String()
	for _, required := range []string{"vpp", "smartdns", "kea", "xray", "pppoe", "persistence", "available", "degraded"} {
		if !strings.Contains(payload, required) {
			t.Fatalf("capabilities response missing %q: %s", required, payload)
		}
	}
	if strings.Contains(payload, "transparent_proxy_handoff") {
		t.Fatalf("capabilities response exposed research-gated handoff: %s", payload)
	}
}

func TestProxyEgressRouteExposesWANPresentationWithoutPhysicalFields(t *testing.T) {
	res := request(t, New(), http.MethodGet, "/api/v1/proxy/egress")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	payload := res.Body.String()
	for _, required := range []string{"proxy_egress", "wan", "vpp_service_interface", "xray", "vpp_to_service", "degraded"} {
		if !strings.Contains(payload, required) {
			t.Fatalf("proxy payload missing %q: %s", required, payload)
		}
	}
	for _, forbidden := range []string{"mac_address", "link_speed", "pppoe_password", "physical_interface_id"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("proxy payload leaked %q: %s", forbidden, payload)
		}
	}
}

func TestProxyEgressPluralRouteRemainsReadOnlyAlias(t *testing.T) {
	res := request(t, New(), http.MethodGet, "/api/v1/proxy/egresses")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "proxy_egress") {
		t.Fatalf("proxy alias response missing proxy_egress: %s", res.Body.String())
	}
}

func TestProxyEgressPostPersistsLocalStoreConfig(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-egress-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	underlay := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/wan-links", `{"id":"wan-underlay","name":"Underlay","enabled":true,"type":"static"}`, login.Result().Cookies()[0])
	if underlay.Code != http.StatusOK {
		t.Fatalf("underlay status=%d body=%s", underlay.Code, underlay.Body.String())
	}
	body := `{"id":"proxy-media","kind":"egress","name":"Media Proxy","enabled":true,"semantic_type":"proxy_egress","display_list":"wan","proxy_profile_id":"xray-tproxy-outbound","underlay_wan_id":"wan-underlay"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/proxy/egresses", bytes.NewBufferString(body))
	req.AddCookie(login.Result().Cookies()[0])
	created := httptest.NewRecorder()
	server.Handler().ServeHTTP(created, req)
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200: %s", created.Code, created.Body.String())
	}
	for _, required := range []string{"proxy-media", "proxy_egress", "wan", "xray", "wan-underlay", "degraded"} {
		if !strings.Contains(created.Body.String(), required) {
			t.Fatalf("create response missing %q: %s", required, created.Body.String())
		}
	}

	listed := request(t, server, http.MethodGet, "/api/v1/proxy/egresses")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", listed.Code, listed.Body.String())
	}
	if !strings.Contains(listed.Body.String(), "proxy-media") || !strings.Contains(listed.Body.String(), "Media Proxy") || !strings.Contains(listed.Body.String(), "vpp_to_service") || !strings.Contains(listed.Body.String(), "wan-underlay") {
		t.Fatalf("list response missing persisted proxy egress: %s", listed.Body.String())
	}
	deleted := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/proxy/egresses/proxy-media", ``, login.Result().Cookies()[0])
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestProxyEgressDefaultDeletePersistsTombstone(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-egress-default-delete-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	deleted := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/proxy/egresses/proxy-egress-default", ``, login.Result().Cookies()[0])
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete default status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	listed := request(t, server, http.MethodGet, "/api/v1/proxy/egresses")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), "proxy-egress-default") {
		t.Fatalf("default proxy egress reappeared after delete: %s", listed.Body.String())
	}
}

func TestProxyEgressPostRejectsUnknownPhysicalWANFields(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-egress-reject-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/proxy/egresses", bytes.NewBufferString(`{"id":"proxy-media","semantic_type":"proxy_egress","mac_address":"00:11:22:33:44:55"}`))
	req.AddCookie(login.Result().Cookies()[0])
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown physical WAN field: %s", res.Code, res.Body.String())
	}
}

func TestProxyEgressWriteRequiresAdmin(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-egress-auth-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret", ReadonlyUsername: "readonly", ReadonlyPassword: "readonly-secret"}), WithClock(fixedClock()))
	unauth := requestBody(t, server, http.MethodPost, "/api/v1/proxy/egresses", `{}`)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", unauth.Code)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"readonly","password":"readonly-secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/proxy/egresses", bytes.NewBufferString(`{}`))
	req.AddCookie(login.Result().Cookies()[0])
	readonly := httptest.NewRecorder()
	server.Handler().ServeHTTP(readonly, req)
	if readonly.Code != http.StatusForbidden {
		t.Fatalf("readonly status = %d, want 403: %s", readonly.Code, readonly.Body.String())
	}
}

func TestFlowRouteExposesCurrentIntentAndDegradedCapability(t *testing.T) {
	res := request(t, New(), http.MethodGet, "/api/v1/flow-control/runtime")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	payload := res.Body.String()
	for _, required := range []string{"classify-default", "classify", "vpp_qos_push", "degraded"} {
		if !strings.Contains(payload, required) {
			t.Fatalf("flow payload missing %q: %s", required, payload)
		}
	}
	if strings.Contains(payload, "connection_limit") {
		t.Fatalf("flow payload leaked connection_limit: %s", payload)
	}
}

func TestFlowIntentRouteRemainsReadOnlyAlias(t *testing.T) {
	res := request(t, New(), http.MethodGet, "/api/v1/flow-control/intents/default")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "classify-default") {
		t.Fatalf("flow alias response missing classify-default: %s", res.Body.String())
	}
}

func TestGatewayTrafficControlRouteIsContractAlignedReadOnlyAlias(t *testing.T) {
	res := request(t, New(), http.MethodGet, "/api/v1/gateway/traffic-control")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	payload := res.Body.String()
	for _, required := range []string{"classify-default", "vpp_qos_push", "degraded"} {
		if !strings.Contains(payload, required) {
			t.Fatalf("traffic-control payload missing %q: %s", required, payload)
		}
	}
	for _, forbidden := range []string{"connection_limit", "connection-limit", "max_connections"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("traffic-control payload leaked %q: %s", forbidden, payload)
		}
	}
}

func TestDNSPoliciesPersistCompileAndRenderSmartDNS(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-policy-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	body := `{"id":"default","kind":"policy","name":"Default DNS","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"direct-local","domains":["lan.example"],"outcome":{"kind":"direct","upstream_id":"dns-direct-default"}},{"id":"proxy-media","domains":["media.example"],"outcome":{"kind":"proxy","proxy_egress_id":"proxy-egress-default"}}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dns/policies", bytes.NewBufferString(body))
	req.AddCookie(login.Result().Cookies()[0])
	create := httptest.NewRecorder()
	server.Handler().ServeHTTP(create, req)
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200: %s", create.Code, create.Body.String())
	}
	for _, required := range []string{"smartdns", "proxy_dns_request", "proxy-egress-default", "degraded"} {
		if !strings.Contains(create.Body.String(), required) {
			t.Fatalf("create response missing %q: %s", required, create.Body.String())
		}
	}

	list := request(t, server, http.MethodGet, "/api/v1/dns/policies")
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), "media.example") || !strings.Contains(list.Body.String(), "proxy_dns_request") {
		t.Fatalf("list response missing persisted DNS render: %s", list.Body.String())
	}
	item := request(t, server, http.MethodGet, "/api/v1/dns/policies/default")
	if item.Code != http.StatusOK {
		t.Fatalf("item status = %d, want 200: %s", item.Code, item.Body.String())
	}
	if !strings.Contains(item.Body.String(), "media.example") || !strings.Contains(item.Body.String(), "proxy_dns_request") {
		t.Fatalf("item response missing persisted DNS render: %s", item.Body.String())
	}
	customBody := `{"id":"custom","kind":"policy","name":"Custom DNS","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"direct-custom","domains":["custom.example"],"outcome":{"kind":"direct","upstream_id":"dns-direct-default"}}]}}`
	customCreate := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", customBody, login.Result().Cookies()[0])
	if customCreate.Code != http.StatusOK {
		t.Fatalf("custom create status = %d, want 200: %s", customCreate.Code, customCreate.Body.String())
	}
	customItem := request(t, server, http.MethodGet, "/api/v1/dns/policies/custom")
	if customItem.Code != http.StatusOK || !strings.Contains(customItem.Body.String(), "custom.example") {
		t.Fatalf("custom item status = %d body = %s", customItem.Code, customItem.Body.String())
	}
	deleted := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/dns/policies/custom", "", login.Result().Cookies()[0])
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := request(t, server, http.MethodGet, "/api/v1/dns/policies/custom")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted item status = %d, want 404: %s", missing.Code, missing.Body.String())
	}
}

func TestDNSResolveDefaultsToNODATANoDefaultUpstream(t *testing.T) {
	server := New(WithClock(fixedClock()))
	res := request(t, server, http.MethodGet, "/api/v1/dns/resolve?domain=unknown.example")
	if res.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, want 200: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{`"policy_id":"default"`, `"matched":false`, `"answer":"NODATA"`, `"rcode":"NOERROR"`, `"kind":"reject"`} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("default-deny response missing %q: %s", required, res.Body.String())
		}
	}
	if strings.Contains(res.Body.String(), "direct_resolver") || strings.Contains(res.Body.String(), "proxy_egress_resolver") {
		t.Fatalf("default-deny response selected an upstream resolver: %s", res.Body.String())
	}
}

func TestDNSResolveUsesStoredPolicyOrderSuffixMatchAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-resolve-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	body := `{"id":"default","kind":"policy","name":"Default DNS","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"reject-video","source_prefixes":["any"],"domain_suffixes":["video.example"],"outcome":{"kind":"reject"}},{"id":"direct-example","source_prefixes":["any"],"domain_suffixes":["example"],"outcome":{"kind":"direct","upstream_id":"dns-direct-default"}}]}}`
	create := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", body, login.Result().Cookies()[0])
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200: %s", create.Code, create.Body.String())
	}
	matched := request(t, server, http.MethodGet, "/api/v1/dns/resolve?domain=cdn.video.example&source_ip=192.0.2.10")
	if matched.Code != http.StatusOK || !strings.Contains(matched.Body.String(), `"rule_id":"reject-video"`) || !strings.Contains(matched.Body.String(), `"answer":"NODATA"`) {
		t.Fatalf("ordered suffix resolve = %d %s", matched.Code, matched.Body.String())
	}
	if strings.Contains(matched.Body.String(), `"rule_id":"direct-example"`) || strings.Contains(matched.Body.String(), `"continue_rules":true`) {
		t.Fatalf("ordered suffix resolve fell through to lower rule: %s", matched.Body.String())
	}
	unavailable := request(t, server, http.MethodGet, "/api/v1/dns/resolve?domain=plain.example&source_ip=192.0.2.10&unavailable_resolvers=upstream%3Adns-direct-default")
	if unavailable.Code != http.StatusOK || !strings.Contains(unavailable.Body.String(), `"rule_id":"direct-example"`) || !strings.Contains(unavailable.Body.String(), `"answer":"NODATA"`) || !strings.Contains(unavailable.Body.String(), "selected resolver is unavailable") {
		t.Fatalf("unavailable resolver resolve = %d %s", unavailable.Code, unavailable.Body.String())
	}
	if strings.Contains(unavailable.Body.String(), `"continue_rules":true`) {
		t.Fatalf("unavailable resolver resolve continued to lower rules: %s", unavailable.Body.String())
	}
}

func TestDNSPolicyRejectsUnavailableDomainSetBeforeSave(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-domain-set-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	policy := `{"id":"managed","kind":"policy","name":"Managed DNS","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"managed-rule","source_prefixes":["192.0.2.0/24"],"domain_set_ids":["managed-domains"],"outcome":{"kind":"reject"}}]}}`

	missing := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", policy, cookie)
	if missing.Code != http.StatusUnprocessableEntity || !strings.Contains(missing.Body.String(), `unavailable domain set \"managed-domains\"`) {
		t.Fatalf("missing domain set status=%d body=%s", missing.Code, missing.Body.String())
	}
	if _, err := store.Policy(ctx, "dns-policy", "managed"); err == nil {
		t.Fatal("policy with unavailable domain set was persisted")
	}

	group := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups", `{"id":"managed-domains","kind":"domain","name":"Managed Domains","enabled":true,"entries":["portal.example",".updates.example"]}`, cookie)
	if group.Code != http.StatusOK {
		t.Fatalf("domain set create status=%d body=%s", group.Code, group.Body.String())
	}
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", policy, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("policy with available domain set status=%d body=%s", created.Code, created.Body.String())
	}
}

func TestDNSResolveExpandsAnySourceAndDomainSetSelectors(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-domain-resolve-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	group := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/objects/groups", `{"id":"resolve-domains","kind":"domain","name":"Resolve Domains","enabled":true,"entries":["example.test",".suffix.test"]}`, cookie)
	if group.Code != http.StatusOK {
		t.Fatalf("domain set status=%d body=%s", group.Code, group.Body.String())
	}
	policy := `{"id":"default","kind":"policy","name":"Default DNS","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"domain-set-rule","source_prefixes":["any"],"domain_set_ids":["resolve-domains"],"outcome":{"kind":"direct","upstream_id":"dns-direct-default"}}]}}`
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", policy, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", created.Code, created.Body.String())
	}
	matched := request(t, server, http.MethodGet, "/api/v1/dns/resolve?domain=example.test&source_ip=192.0.2.99")
	if matched.Code != http.StatusOK || !strings.Contains(matched.Body.String(), `"rule_id":"domain-set-rule"`) {
		t.Fatalf("domain-set resolve = %d %s", matched.Code, matched.Body.String())
	}
	suffix := request(t, server, http.MethodGet, "/api/v1/dns/resolve?domain=www.suffix.test&source_ip=198.51.100.99")
	if suffix.Code != http.StatusOK || !strings.Contains(suffix.Body.String(), `"rule_id":"domain-set-rule"`) {
		t.Fatalf("domain suffix resolve = %d %s", suffix.Code, suffix.Body.String())
	}
}

func TestDNSPolicyRejectsImplicitFallbackAndUnsupportedClaimsWithZeroWrites(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-strict-negative-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	tests := []struct {
		id         string
		policy     string
		wantStatus int
		wantCode   string
	}{
		{id: "implicit-upstream", policy: `{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"implicit","domains":["example.test"],"outcome":{"kind":"direct"}}]}`, wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_dns_policy"},
		{id: "ambiguous-egress", policy: `{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"ambiguous","domains":["example.test"],"outcome":{"kind":"direct","upstream_id":"dns-primary","wan_egress_id":"wan-primary"}}]}`, wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_dns_policy"},
		{id: "lower-rule-fallback", policy: `{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"fallback","domains":["example.test"],"outcome":{"kind":"reject","continue_rules":true}}]}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{id: "generic-route", policy: `{"engine":"smartdns","route_table":100,"miss":{"kind":"reject"},"rules":[]}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{id: "generic-nat", policy: `{"engine":"smartdns","nat":true,"miss":{"kind":"reject"},"rules":[]}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{id: "doh-claim", policy: `{"engine":"smartdns","doh":true,"miss":{"kind":"reject"},"rules":[]}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{id: "dpi-claim", policy: `{"engine":"smartdns","dpi":true,"miss":{"kind":"reject"},"rules":[]}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			body := `{"id":"` + tt.id + `","kind":"policy","name":"negative","enabled":true,"policy":` + tt.policy + `}`
			res := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", body, cookie)
			if res.Code != tt.wantStatus || !strings.Contains(res.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Fatalf("status=%d body=%s, want %d/%s", res.Code, res.Body.String(), tt.wantStatus, tt.wantCode)
			}
			if _, err := store.Policy(ctx, "dns-policy", tt.id); err == nil {
				t.Fatalf("rejected DNS policy %q was persisted", tt.id)
			}
		})
	}
	events, err := store.AuditEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	failures := 0
	for _, event := range events {
		if event.Resource == "/api/v1/dns/policies" && event.Action == "create" && event.Status == "failure" {
			failures++
		}
	}
	if failures != len(tests) {
		t.Fatalf("DNS rejection audit failures = %d, want %d: %#v", failures, len(tests), events)
	}
}

func TestDNSRuleUpdateVerifiesChecksumSwitchesPolicyAndRetainsRollback(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-rule-update-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	initial := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/policies", `{"id":"default","kind":"policy","name":"Default DNS","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"old-direct","domains":["old.example"],"outcome":{"kind":"direct","upstream_id":"dns-direct-default"}}]}}`, cookie)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial policy status = %d, want 200: %s", initial.Code, initial.Body.String())
	}
	rules := []dns.Rule{{ID: "fixed-portal", SourcePrefixes: []string{"192.168.88.0/24"}, Domains: []string{"portal.example"}, Outcome: dns.FixedAnswer("192.168.88.1")}}
	rulesPayload, rulesHash, err := persistence.MarshalPayload(rules)
	if err != nil {
		t.Fatal(err)
	}
	mismatch := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/rule-updates", `{"policy_id":"default","expected_sha256":"bad","rules":`+string(rulesPayload)+`}`, cookie)
	if mismatch.Code != http.StatusUnprocessableEntity || !strings.Contains(mismatch.Body.String(), "checksum_mismatch") {
		t.Fatalf("checksum mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	update := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/dns/rule-updates", `{"policy_id":"default","expected_sha256":"`+rulesHash+`","rules":`+string(rulesPayload)+`}`, cookie)
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"rollback_retained":true`) || !strings.Contains(update.Body.String(), rulesHash) {
		t.Fatalf("rule update status=%d body=%s", update.Code, update.Body.String())
	}
	resolved := request(t, server, http.MethodGet, "/api/v1/dns/resolve?domain=portal.example&source_ip=192.168.88.20")
	if resolved.Code != http.StatusOK || !strings.Contains(resolved.Body.String(), `"rule_id":"fixed-portal"`) || !strings.Contains(resolved.Body.String(), `"answer":"FIXED"`) || !strings.Contains(resolved.Body.String(), "192.168.88.1") {
		t.Fatalf("post-update resolve = %d %s", resolved.Code, resolved.Body.String())
	}
	rollback, err := store.Config(ctx, "dns_rule_update_rollback", "default")
	if err != nil {
		t.Fatalf("rollback metadata missing: %v", err)
	}
	if !strings.Contains(string(rollback.Payload), "old-direct") || !strings.Contains(string(rollback.Payload), rulesHash) {
		t.Fatalf("rollback metadata = %s", rollback.Payload)
	}
}

func TestDNSPolicyWriteRequiresAdminSession(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-policy-auth-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret", ReadonlyUsername: "readonly", ReadonlyPassword: "readonly-secret"}), WithClock(fixedClock()))
	unauth := requestBody(t, server, http.MethodPost, "/api/v1/dns/policies", `{}`)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", unauth.Code)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"readonly","password":"readonly-secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("readonly login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dns/policies", bytes.NewBufferString(`{}`))
	req.AddCookie(login.Result().Cookies()[0])
	readonly := httptest.NewRecorder()
	server.Handler().ServeHTTP(readonly, req)
	if readonly.Code != http.StatusForbidden {
		t.Fatalf("readonly status = %d, want 403: %s", readonly.Code, readonly.Body.String())
	}
}

func TestUnknownAPIRouteReturnsStructuredError(t *testing.T) {
	res := request(t, New(), http.MethodGet, "/api/v1/missing")
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.Code)
	}
	var body ErrorResponse
	decode(t, res, &body)
	if body.Error.Code != "not_found" || body.Error.Message != "unknown API route" || body.Error.RequestID == "" {
		t.Fatalf("error body = %#v", body)
	}
}

func TestMethodNotAllowedReturnsStructuredError(t *testing.T) {
	res := request(t, New(), http.MethodPost, "/api/v1/health")
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.Code)
	}
	var body ErrorResponse
	decode(t, res, &body)
	if body.Error.Code != "method_not_allowed" || body.Error.RequestID == "" {
		t.Fatalf("error body = %#v", body)
	}
}

func TestLoginCreatesSecureSessionCookieAndSessionRoute(t *testing.T) {
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret", CookieSecure: true}),
		WithClock(fixedClock()),
	)
	res := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v, want one session cookie", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || cookie.Value == "" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %#v", cookie)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(cookie)
	sessionRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(sessionRes, req)
	if sessionRes.Code != http.StatusOK || !strings.Contains(sessionRes.Body.String(), "admin") {
		t.Fatalf("session response = %d %s", sessionRes.Code, sessionRes.Body.String())
	}

	events, err := server.auditEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "login" || events[0].Status != "success" || events[0].Actor != "admin" {
		t.Fatalf("audit events = %#v", events)
	}
}

func TestFailedLoginAndLogoutAreAudited(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	failed := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"wrong"}`)
	if failed.Code != http.StatusUnauthorized {
		t.Fatalf("failed login status = %d, want 401", failed.Code)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", login.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(login.Result().Cookies()[0])
	logout := httptest.NewRecorder()
	server.Handler().ServeHTTP(logout, req)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", logout.Code)
	}

	events, err := server.auditEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"login:failure", "login:success", "logout:success"}
	if len(events) != len(want) {
		t.Fatalf("audit events = %#v, want %d", events, len(want))
	}
	for i, event := range events {
		got := event.Action + ":" + event.Status
		if got != want[i] {
			t.Fatalf("event %d = %s, want %s: %#v", i, got, want[i], events)
		}
	}
}

func TestAuthUsersRouteListsStaticAccountsWithoutPasswords(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{
		AdminUsername:    "admin",
		AdminPassword:    "secret",
		ReadonlyUsername: "readonly",
		ReadonlyPassword: "readonly-secret",
	}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/users", nil)
	req.AddCookie(login.Result().Cookies()[0])
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("users status = %d, want 200: %s", res.Code, res.Body.String())
	}
	payload := res.Body.String()
	for _, required := range []string{"admin", "readonly", "enabled"} {
		if !strings.Contains(payload, required) {
			t.Fatalf("users payload missing %q: %s", required, payload)
		}
	}
	for _, forbidden := range []string{"secret", "readonly-secret", "password"} {
		if strings.Contains(strings.ToLower(payload), forbidden) {
			t.Fatalf("users payload leaked %q: %s", forbidden, payload)
		}
	}
}

func TestAuthUsersCRUDAndStoredLogin(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-auth-users-crud-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("admin login = %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	create := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/auth/users", `{"username":"operator","role":"readonly","password":"RouteSmoke1"}`, cookie)
	if create.Code != http.StatusOK || !strings.Contains(create.Body.String(), "operator") || strings.Contains(strings.ToLower(create.Body.String()), "password") {
		t.Fatalf("create user = %d %s", create.Code, create.Body.String())
	}
	operatorLogin := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"operator","password":"RouteSmoke1"}`)
	if operatorLogin.Code != http.StatusOK || !strings.Contains(operatorLogin.Body.String(), `"role":"readonly"`) {
		t.Fatalf("operator login = %d %s", operatorLogin.Code, operatorLogin.Body.String())
	}
	patch := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/auth/users/operator", `{"role":"admin","password":"RouteSmoke2"}`, cookie)
	if patch.Code != http.StatusOK || !strings.Contains(patch.Body.String(), `"role":"admin"`) {
		t.Fatalf("patch user = %d %s", patch.Code, patch.Body.String())
	}
	oldLogin := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"operator","password":"RouteSmoke1"}`)
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login = %d, want 401: %s", oldLogin.Code, oldLogin.Body.String())
	}
	newLogin := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"operator","password":"RouteSmoke2"}`)
	if newLogin.Code != http.StatusOK || !strings.Contains(newLogin.Body.String(), `"role":"admin"`) {
		t.Fatalf("new password login = %d %s", newLogin.Code, newLogin.Body.String())
	}
	deleteAdmin := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/auth/users/admin", ``, cookie)
	if deleteAdmin.Code != http.StatusUnprocessableEntity || !strings.Contains(deleteAdmin.Body.String(), "protected_user") {
		t.Fatalf("delete protected admin = %d %s", deleteAdmin.Code, deleteAdmin.Body.String())
	}
	deleted := authenticatedJSONRequest(t, server, http.MethodDelete, "/api/v1/auth/users/operator", ``, cookie)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete operator = %d %s", deleted.Code, deleted.Body.String())
	}
	deletedLogin := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"operator","password":"RouteSmoke2"}`)
	if deletedLogin.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user login = %d, want 401: %s", deletedLogin.Code, deletedLogin.Body.String())
	}
}

func TestReadonlySessionCannotUseProtectedWriteRoute(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{
		AdminUsername:    "admin",
		AdminPassword:    "secret",
		ReadonlyUsername: "readonly",
		ReadonlyPassword: "readonly-secret",
	}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"readonly","password":"readonly-secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("readonly login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", bytes.NewBufferString(`{}`))
	req.AddCookie(login.Result().Cookies()[0])
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("readonly apply status = %d, want 403: %s", res.Code, res.Body.String())
	}
	events, err := server.auditEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Actor != "readonly" || last.Role != "readonly" || last.Action != "apply" || last.Status != "denied" {
		t.Fatalf("audit events = %#v", events)
	}
}

func TestProtectedWriteRouteRequiresSessionAndAuditsDeniedApply(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	res := requestBody(t, server, http.MethodPost, "/api/v1/config/apply", `{}`)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", res.Code, res.Body.String())
	}
	events, err := server.auditEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "apply" || events[0].Status != "denied" || events[0].Resource != "/api/v1/config/apply" {
		t.Fatalf("audit events = %#v", events)
	}
}

func TestAuthenticatedConfigApplyRunsPipelineAndAuditsRollback(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-config-apply-pipeline-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	saveExplicitDataInterface(t, store, "eth1")
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()), WithStore(store))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", login.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", bytes.NewBufferString(`{}`))
	req.AddCookie(login.Result().Cookies()[0])
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", res.Code, res.Body.String())
	}
	for _, required := range []string{"apply_failed", "rollback", "live runtime apply runner is not configured", "validate", "compile", "snapshot"} {
		if !strings.Contains(res.Body.String(), required) {
			t.Fatalf("apply response missing %q: %s", required, res.Body.String())
		}
	}
	events, err := store.AuditEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	joined := make([]string, 0, len(events))
	for _, event := range events {
		joined = append(joined, event.Action+":"+event.Status)
	}
	if !strings.Contains(strings.Join(joined, ","), "rollback:rollback") || !strings.Contains(strings.Join(joined, ","), "apply:failure") {
		t.Fatalf("audit events = %#v", events)
	}
	if !strings.Contains(res.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("rollback response lacks successful first-generation cleanup: %s", res.Body.String())
	}
}

func TestAuthenticatedConfigApplyCommitsWithServiceRuntime(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-config-apply-runtime-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	saveExplicitDataInterface(t, store, "eth1")
	saveTestProxyNode(t, store)
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}}
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(fixedClock()),
		WithStore(store),
		WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}),
		WithProxyEgress(testProxyEgressWithNode()),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", login.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", bytes.NewBufferString(`{}`))
	req.AddCookie(login.Result().Cookies()[0])
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "committed") || !strings.Contains(res.Body.String(), "health-check") {
		t.Fatalf("apply response missing commit evidence: %s", res.Body.String())
	}
	if got := strings.Join(controller.applied, ","); got != "vpp,linux-routing,smartdns,xray,nftables" {
		t.Fatalf("applied services = %s, want full proven forwarding plan", got)
	}
	if len(controller.rolledBack) != 0 {
		t.Fatalf("unexpected rollback services = %#v", controller.rolledBack)
	}
}

func TestAuditEventsRouteReadsPersistedRedactedEvents(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-audit-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()), WithStore(store))

	failed := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"wrong"}`)
	if failed.Code != http.StatusUnauthorized {
		t.Fatalf("failed login status = %d, want 401", failed.Code)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", login.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/telemetry/audit-events", nil)
	req.AddCookie(login.Result().Cookies()[0])
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("audit status = %d, want 200: %s", res.Code, res.Body.String())
	}
	payload := res.Body.String()
	for _, forbidden := range []string{"password", "secret", "credential", "token"} {
		if strings.Contains(strings.ToLower(payload), forbidden) {
			t.Fatalf("audit response leaked %q: %s", forbidden, payload)
		}
	}
	if !strings.Contains(payload, "login") || !strings.Contains(payload, "failure") || !strings.Contains(payload, "success") {
		t.Fatalf("audit response missing expected persisted auth events: %s", payload)
	}
}

func TestManagementNetworkSharedLANContract(t *testing.T) {
	store, err := persistence.Open(context.Background(), "file:httpapi-management-shared-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	before := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/management/network", "", cookie)
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"mode":"exclusive"`) {
		t.Fatalf("default management mode = %d: %s", before.Code, before.Body.String())
	}
	shared := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/management/network", `{"confirm_change":true,"mode":"shared_lan","interface_id":"eth0","cidr":"10.10.10.254/24","gateway":"10.10.10.254"}`, cookie)
	if shared.Code != http.StatusOK || !strings.Contains(shared.Body.String(), `"mode":"shared_lan"`) {
		t.Fatalf("shared management response = %d: %s", shared.Code, shared.Body.String())
	}
	after := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/management/network", "", cookie)
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"interface_id":"eth0"`) || !strings.Contains(after.Body.String(), `"management_ip":"10.10.10.254"`) {
		t.Fatalf("shared management readback = %d: %s", after.Code, after.Body.String())
	}
	invalid := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/management/network", `{"confirm_change":true,"mode":"shared"}`, cookie)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid management mode status = %d, want 422: %s", invalid.Code, invalid.Body.String())
	}
}

func request(t *testing.T, server *Server, method, path string) *httptest.ResponseRecorder {
	return requestBody(t, server, method, path, "")
}

func requestBody(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	return res
}

func authenticatedJSONRequest(t *testing.T, server *Server, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(cookie)
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	return res
}

func TestProxyXrayRuntimeStatusRestartAndLogs(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-xray-runtime-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	saveTestProxyNode(t, store)
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}}, logs: map[serviceRuntime.ServiceName]string{serviceRuntime.Xray: "xray ready\nvless://secret@example"}}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}), WithProxyEgress(testProxyEgressWithNode()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/xray/status", "", cookie)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"state":"available"`) {
		t.Fatalf("xray status=%d body=%s", status.Code, status.Body.String())
	}
	logs := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/xray/logs", "", cookie)
	if logs.Code != http.StatusOK || !strings.Contains(logs.Body.String(), "xray ready") || strings.Contains(logs.Body.String(), "vless://secret") {
		t.Fatalf("xray logs=%d body=%s", logs.Code, logs.Body.String())
	}
	restart := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/xray/restart", `{}`, cookie)
	if restart.Code != http.StatusOK || !strings.Contains(restart.Body.String(), `"status":"restarted"`) || !strings.Contains(restart.Body.String(), `"runtime_state":"running"`) || !strings.Contains(restart.Body.String(), "/etc/xray/config.json") {
		t.Fatalf("xray restart=%d body=%s", restart.Code, restart.Body.String())
	}
	if len(controller.applied) == 0 || controller.applied[len(controller.applied)-1] != "xray" {
		t.Fatalf("xray restart did not call service controller: %#v", controller.applied)
	}
}

func TestProxyXrayRuntimeReportsDegradedWithoutServiceRuntime(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	res := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/xray/status", "", login.Result().Cookies()[0])
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "xray service runtime is not configured") {
		t.Fatalf("xray degraded status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestFirmwareUpdateStageAcceptsFullPackageWithChecksum(t *testing.T) {
	stageDir := t.TempDir()
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithFirmwareStageDir(stageDir), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	firmware := testUpgradePackage(t, product.Gateway().ID(), false)
	hash := fmt.Sprintf("%x", sha256.Sum256(firmware))
	res := authenticatedMultipartRequest(t, server, "/api/v1/firmware/update/stage", map[string][]byte{
		"firmware": firmware,
	}, login.Result().Cookies()[0])
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"staged":true`) || !strings.Contains(res.Body.String(), hash) || !strings.Contains(res.Body.String(), "config_backup_path") {
		t.Fatalf("firmware stage status=%d body=%s", res.Code, res.Body.String())
	}
	stagedFiles, err := filepath.Glob(filepath.Join(stageDir, "upgrade-*.tar.zst"))
	if err != nil || len(stagedFiles) != 1 {
		t.Fatalf("staged firmware files = %#v err=%v, want one server-named package", stagedFiles, err)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "ly-route-config-backup.json")); err != nil {
		t.Fatalf("config backup missing: %v", err)
	}
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/firmware/update/status", "", login.Result().Cookies()[0])
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), hash) {
		t.Fatalf("firmware status=%d body=%s", status.Code, status.Body.String())
	}
}

func TestFirmwareUpdateStageRejectsInvalidPackageChecksum(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithFirmwareStageDir(t.TempDir()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	res := authenticatedMultipartRequest(t, server, "/api/v1/firmware/update/stage", map[string][]byte{
		"firmware": testUpgradePackage(t, product.Gateway().ID(), true),
	}, login.Result().Cookies()[0])
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "invalid_upgrade_package") {
		t.Fatalf("firmware mismatch status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestFirmwareUpdateInstallRequiresStagedFirmwareAndConfirmation(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithFirmwareStageDir(t.TempDir()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	missingConfirm := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/firmware/update/install", `{}`, cookie)
	if missingConfirm.Code != http.StatusUnprocessableEntity || !strings.Contains(missingConfirm.Body.String(), "install_not_confirmed") {
		t.Fatalf("install without confirmation status=%d body=%s", missingConfirm.Code, missingConfirm.Body.String())
	}
	notStaged := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/firmware/update/install", `{"confirm_install":true}`, cookie)
	if notStaged.Code != http.StatusConflict || !strings.Contains(notStaged.Body.String(), "firmware_not_staged") {
		t.Fatalf("install without staged firmware status=%d body=%s", notStaged.Code, notStaged.Body.String())
	}
}

func TestFirmwareUpdateInstallStartsAsyncPackageInstaller(t *testing.T) {
	stageDir := t.TempDir()
	var started firmwareInstallInvocation
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithFirmwareStageDir(stageDir), WithFirmwareInstallStart(func(invocation firmwareInstallInvocation) error {
		started = invocation
		return nil
	}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200: %s", login.Code, login.Body.String())
	}
	firmware := testUpgradePackage(t, product.Gateway().ID(), false)
	stage := authenticatedMultipartRequest(t, server, "/api/v1/firmware/update/stage", map[string][]byte{
		"firmware": firmware,
	}, login.Result().Cookies()[0])
	if stage.Code != http.StatusOK {
		t.Fatalf("firmware stage status=%d body=%s", stage.Code, stage.Body.String())
	}
	install := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/firmware/update/install", `{"confirm_install":true,"target_dir":"/usr/lib/ly-route","reboot":true}`, login.Result().Cookies()[0])
	if install.Code != http.StatusAccepted || !strings.Contains(install.Body.String(), `"installing":true`) {
		t.Fatalf("firmware install status=%d body=%s", install.Code, install.Body.String())
	}
	script := started.Args[1]
	for _, forbidden := range []string{"dd of=", "blockdev --flushbufs", "target_disk"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("install command contains forbidden disk writer %q: %s", forbidden, script)
		}
	}
	for _, required := range []string{"sha256sum", "zstd -dc", "tar -x", "manifest.json", "ly-route-upgrade", "install -m 0755", "/usr/lib/ly-route/ly-route-control", "/usr/lib/ly-route/vpp-apply", "systemctl restart ly-route-control-api.service", "reboot -f"} {
		if !strings.Contains(script, required) {
			t.Fatalf("install command missing %q: %s", required, script)
		}
	}
	if started.Args[6] != "true" {
		t.Fatalf("reboot argument = %q, want true", started.Args[6])
	}
}

func TestProductFirmwareUpdateStageRejectsCrossProductBeforeConfigBackup(t *testing.T) {
	// Given
	stageDir := t.TempDir()
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithFirmwareStageDir(stageDir))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}

	// When
	response := authenticatedMultipartRequest(t, server, "/api/v1/firmware/update/stage", map[string][]byte{"firmware": testUpgradePackage(t, product.Orchestrator().ID(), false)}, login.Result().Cookies()[0])

	// Then
	if response.Code != http.StatusUnprocessableEntity || !containsAll(response.Body.String(), "invalid_upgrade_package", "product", "orchestrator", "gateway") {
		t.Fatalf("cross-product upgrade status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(stageDir, "ly-route-config-backup.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config backup exists after rejected preflight: %v", err)
	}
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/firmware/update/status", "", login.Result().Cookies()[0])
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), `"staged":true`) {
		t.Fatalf("firmware status mutated after rejected preflight: %d %s", status.Code, status.Body.String())
	}
}

func testUpgradePackage(t *testing.T, productID product.ID, corruptChecksum bool) []byte {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd is required to build test upgrade package")
	}
	root := filepath.Join(t.TempDir(), "package")
	mustWrite := func(path, body string, mode os.FileMode) {
		t.Helper()
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", fullPath, err)
		}
		if err := os.WriteFile(fullPath, []byte(body), mode); err != nil {
			t.Fatalf("write %s: %v", fullPath, err)
		}
	}
	mustWrite("usr/lib/ly-route/ly-route-control", "#!/bin/sh\nexit 0\n", 0o755)
	mustWrite("usr/lib/ly-route/vpp-apply", "#!/bin/sh\nexit 0\n", 0o755)
	mustWrite("opt/ly-route/admin/app.js", "console.log('upgrade');\n", 0o644)
	mustWrite("etc/nginx/conf.d/ly-route-admin.conf", "server { listen 443 ssl; }\n", 0o644)
	checksumFor := func(path string) string {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		hash := sha256.Sum256(body)
		return hex.EncodeToString(hash[:])
	}
	controlHash := checksumFor("usr/lib/ly-route/ly-route-control")
	vppApplyHash := checksumFor("usr/lib/ly-route/vpp-apply")
	if corruptChecksum {
		controlHash = strings.Repeat("0", sha256.Size*2)
	}
	manifest := fmt.Sprintf(`{
  "package_type": "ly-route-upgrade",
	"product": %q,
  "suite": "bookworm",
  "arch": "amd64",
  "commit": "test-commit",
  "created_at": "2026-07-19T00:00:00Z",
  "install_root": "/usr/lib/ly-route",
  "services": ["ly-route-control-api.service", "nginx.service"],
  "checksums": {
    "usr/lib/ly-route/ly-route-control": %q,
    "usr/lib/ly-route/vpp-apply": %q,
    "opt/ly-route/admin/app.js": %q,
    "etc/nginx/conf.d/ly-route-admin.conf": %q
  }
}
`, productID.String(), controlHash, vppApplyHash, checksumFor("opt/ly-route/admin/app.js"), checksumFor("etc/nginx/conf.d/ly-route-admin.conf"))
	mustWrite("manifest.json", manifest, 0o644)
	artifact := filepath.Join(t.TempDir(), "upgrade.tar.zst")
	cmd := exec.Command("sh", "-c", fmt.Sprintf("tar --numeric-owner -C %q -cpf - . | zstd -q -19 -o %q", root, artifact))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test upgrade package: %v: %s", err, strings.TrimSpace(string(output)))
	}
	body, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("read upgrade package: %v", err)
	}
	return body
}

func decode(t *testing.T, res *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
		t.Fatalf("decode response: %v: %s", err, res.Body.String())
	}
}

func configDocument(t *testing.T, resourceType, resourceID string, payload map[string]any, updatedAt time.Time) persistence.ConfigDocument {
	t.Helper()
	raw, hash, err := persistence.MarshalPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	return persistence.ConfigDocument{ResourceType: resourceType, ResourceID: resourceID, Payload: raw, PayloadHash: hash, UpdatedAt: updatedAt}
}

func saveExplicitDataInterface(t *testing.T, store *persistence.Store, interfaceNames ...string) {
	t.Helper()
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		items := []map[string]any{{"id": "eth0", "name": "eth0"}}
		for _, interfaceName := range interfaceNames {
			items = append(items, map[string]any{"id": interfaceName, "name": interfaceName})
		}
		return items
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	for _, interfaceName := range interfaceNames {
		document := configDocument(t, "interface", interfaceName, map[string]any{"id": interfaceName, "interface_id": interfaceName, "gateway_role": "lan"}, fixedClock()())
		if err := store.SaveConfig(context.Background(), document); err != nil {
			t.Fatal(err)
		}
	}
}

func authenticatedMultipartRequest(t *testing.T, server *Server, path string, files map[string][]byte, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for field, content := range files {
		part, err := writer.CreateFormFile(field, field)
		if err != nil {
			t.Fatalf("create multipart field %s: %v", field, err)
		}
		if _, err := io.Copy(part, bytes.NewReader(content)); err != nil {
			t.Fatalf("write multipart field %s: %v", field, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.AddCookie(cookie)
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	return res
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }
}

func testProxyEgressWithNode() proxy.Egress {
	egress := proxy.NewProxyEgress("proxy-egress-default", "xray-tproxy-outbound")
	egress.NodeID = "proxy-node-test"
	return egress
}

func saveTestProxyNode(t *testing.T, store *persistence.Store) {
	t.Helper()
	payload := map[string]any{
		"id":       "proxy-node-test",
		"name":     "Test proxy node",
		"enabled":  true,
		"protocol": "trojan",
		"address":  "192.0.2.200",
		"port":     443,
	}
	if err := store.SaveConfigWithSecrets(context.Background(), configDocument(t, "proxy_node", "proxy-node-test", payload, fixedClock()()), map[string]string{"secret": "test-password"}); err != nil {
		t.Fatal(err)
	}
}

type httpServiceController struct {
	applied             []string
	stopped             []string
	rolledBack          []string
	appliedArtifacts    map[serviceRuntime.ServiceName][]serviceRuntime.RenderedArtifact
	rolledBackArtifacts map[serviceRuntime.ServiceName][]serviceRuntime.RenderedArtifact
	receiptArtifacts    []serviceRuntime.RenderedArtifact
	readbackArtifacts   []serviceRuntime.RenderedArtifact
	health              map[serviceRuntime.ServiceName]serviceRuntime.Health
	rollbackErrs        map[serviceRuntime.ServiceName]error
	logs                map[serviceRuntime.ServiceName]string
	receiptErr          error
	readbackErr         error
	applyErrs           map[serviceRuntime.ServiceName]error
	xrayStates          []serviceRuntime.XrayBalancerState
	xrayStateErr        error
}

func (controller *httpServiceController) XrayBalancerStates(_ context.Context, _ []string) ([]serviceRuntime.XrayBalancerState, error) {
	return append([]serviceRuntime.XrayBalancerState(nil), controller.xrayStates...), controller.xrayStateErr
}

func (controller *httpServiceController) Stop(_ context.Context, service serviceRuntime.ServiceName, artifacts []serviceRuntime.RenderedArtifact) error {
	controller.stopped = append(controller.stopped, string(service))
	if len(artifacts) == 0 {
		return errors.New("expected stop artifacts")
	}
	return nil
}

func (controller *httpServiceController) Receipt(_ context.Context, request RuntimeEvidenceRequest) (apply.ApplyReceipt, error) {
	controller.receiptArtifacts = append([]serviceRuntime.RenderedArtifact(nil), request.Artifacts...)
	if controller.receiptErr != nil {
		return apply.ApplyReceipt{}, controller.receiptErr
	}
	now := fixedClock()()
	return apply.ApplyReceipt{TransactionID: request.TransactionID, Capability: request.Capability, Status: apply.ReceiptApplied, AppliedAt: now}, nil
}

func (controller *httpServiceController) Readback(_ context.Context, request RuntimeEvidenceRequest) (apply.Readback, error) {
	controller.readbackArtifacts = append([]serviceRuntime.RenderedArtifact(nil), request.Artifacts...)
	if controller.readbackErr != nil {
		return apply.Readback{}, controller.readbackErr
	}
	return apply.Readback{TransactionID: request.TransactionID, Capability: request.Capability, Timestamp: fixedClock()(), Fresh: true}, nil
}

func (controller *httpServiceController) ReloadOrRestart(_ context.Context, service serviceRuntime.ServiceName, artifacts []serviceRuntime.RenderedArtifact) error {
	controller.applied = append(controller.applied, string(service))
	if controller.appliedArtifacts == nil {
		controller.appliedArtifacts = map[serviceRuntime.ServiceName][]serviceRuntime.RenderedArtifact{}
	}
	controller.appliedArtifacts[service] = append([]serviceRuntime.RenderedArtifact(nil), artifacts...)
	if len(artifacts) == 0 {
		return errors.New("expected service artifacts")
	}
	if err := controller.applyErrs[service]; err != nil {
		return err
	}
	return nil
}

func (controller *httpServiceController) Status(_ context.Context, service serviceRuntime.ServiceName) (serviceRuntime.Health, error) {
	if controller.health != nil {
		if health, ok := controller.health[service]; ok {
			return health, nil
		}
	}
	return serviceRuntime.Health{Service: service, Available: false, Reason: "service inactive"}, nil
}

func (controller *httpServiceController) Rollback(_ context.Context, service serviceRuntime.ServiceName, artifacts []serviceRuntime.RenderedArtifact) error {
	controller.rolledBack = append(controller.rolledBack, string(service))
	if controller.rolledBackArtifacts == nil {
		controller.rolledBackArtifacts = map[serviceRuntime.ServiceName][]serviceRuntime.RenderedArtifact{}
	}
	controller.rolledBackArtifacts[service] = append([]serviceRuntime.RenderedArtifact(nil), artifacts...)
	if err := controller.rollbackErrs[service]; err != nil {
		return err
	}
	if len(artifacts) == 0 {
		return errors.New("expected rollback artifacts")
	}
	return nil
}

func (controller *httpServiceController) Logs(_ context.Context, service serviceRuntime.ServiceName, _ int) (string, error) {
	if controller.logs != nil {
		if logs, ok := controller.logs[service]; ok {
			return logs, nil
		}
	}
	return "", errors.New("logs unavailable")
}

func TestApplyCapabilityFailuresAddsUnlistedFailedService(t *testing.T) {
	now := fixedClock()()
	components := []RuntimeComponentState{{Name: "persistence", State: "running", Available: true}}
	failures := []apply.CapabilityFailureEvidence{{Capability: string(serviceRuntime.Nftables), Reason: "live readback failed"}}

	got := applyCapabilityFailures(components, failures, "runtime-failed-service", now)
	if len(got) != 2 {
		t.Fatalf("components = %#v", got)
	}
	failure := got[1]
	if failure.Name != "nftables_tproxy" || failure.State != "degraded" || failure.Available || failure.Reason != "live readback failed" {
		t.Fatalf("failure component = %#v", failure)
	}
	if failure.ApplyReceipt.Status != apply.ReceiptFailed || failure.ApplyReceipt.TransactionID != "runtime-failed-service" {
		t.Fatalf("failure receipt = %#v", failure.ApplyReceipt)
	}
}
