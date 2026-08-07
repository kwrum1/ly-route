package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
	serviceRuntime "ly-route/backend/internal/runtime/service"
	"ly-route/backend/internal/runtime/vpp"
)

type productionVPPProof struct {
	store  *persistence.Store
	server *httptest.Server
	client *http.Client
	cookie *http.Cookie
	clock  apply.Clock
	trace  string
	state  string
	lcp    string
}

func newProductionVPPProof(t *testing.T, seedPrior bool) *productionVPPProof {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	store, err := persistence.Open(ctx, filepath.Join(directory, "proof.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("LY_ROUTE_MANAGEMENT_INTERFACE", "eth0")
	useVPPProof(t, "eth0")
	refreshProductionProofWindow(t)
	previousInventory := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "eth0", "name": "eth0"}, {"id": "eth1", "name": "eth1"}, {"id": "eth2", "name": "eth2"}}
	}
	t.Cleanup(func() { hostInterfaceInventory = previousInventory })
	saveProductionProofConfig(t, store)
	clock := apply.Clock(time.Now)
	compiler := New(WithStore(store), WithClock(clock), WithFlowIntent(productionProofFlow()))
	plan, err := compiler.buildRuntimePlan(ctx, "proof-plan")
	if err != nil {
		t.Fatal(err)
	}
	proof := &productionVPPProof{store: store, client: &http.Client{Timeout: 10 * time.Second}, clock: clock, trace: filepath.Join(directory, "vppctl.trace"), state: filepath.Join(directory, "interface.state"), lcp: filepath.Join(directory, "lcp.state")}
	if seedPrior {
		seedProductionProofPrior(t, store, compiler.proxyEgress, productionProofFlow(), plan.GatewayPlan)
	}
	if err := os.WriteFile(proof.state, []byte("prior\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proof.lcp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	natState := filepath.Join(directory, "nat.state")
	if err := os.WriteFile(natState, []byte("empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := writeProductionVPPCTL(t, directory, plan.GatewayPlan)
	t.Setenv("FAKE_VPPCTL_TRACE", proof.trace)
	t.Setenv("FAKE_VPPCTL_STATE", proof.state)
	t.Setenv("FAKE_VPPCTL_LCP_STATE", proof.lcp)
	t.Setenv("FAKE_VPPCTL_NAT_STATE", natState)
	controller := &productionProofServiceController{clock: clock}
	transaction := apply.NewProductionGatewayTransaction(vpp.Adapter{Client: vpp.NewVPPCTLClient(binaryPath)}, clock)
	server := New(WithStore(store), WithClock(clock), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithFlowIntent(productionProofFlow()), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}), WithGatewayTransaction(transaction))
	proof.server = httptest.NewServer(server.Handler())
	t.Cleanup(proof.server.Close)
	login := proof.request(t, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`, nil)
	if login.StatusCode != http.StatusOK || len(login.Cookies()) == 0 {
		t.Fatalf("login status = %d", login.StatusCode)
	}
	proof.cookie = login.Cookies()[0]
	return proof
}

type productionProofServiceController struct {
	clock apply.Clock
}

func (controller *productionProofServiceController) Receipt(_ context.Context, request RuntimeEvidenceRequest) (apply.ApplyReceipt, error) {
	return apply.ApplyReceipt{TransactionID: request.TransactionID, Capability: request.Capability, Status: apply.ReceiptApplied, AppliedAt: controller.clock()}, nil
}

func (controller *productionProofServiceController) Readback(_ context.Context, request RuntimeEvidenceRequest) (apply.Readback, error) {
	return apply.Readback{TransactionID: request.TransactionID, Capability: request.Capability, Timestamp: controller.clock(), Fresh: true}, nil
}

func (*productionProofServiceController) ReloadOrRestart(context.Context, serviceRuntime.ServiceName, []serviceRuntime.RenderedArtifact) error {
	return nil
}

func (*productionProofServiceController) Status(_ context.Context, service serviceRuntime.ServiceName) (serviceRuntime.Health, error) {
	return serviceRuntime.Health{Service: service, Available: true}, nil
}

func (*productionProofServiceController) Rollback(context.Context, serviceRuntime.ServiceName, []serviceRuntime.RenderedArtifact) error {
	return nil
}

func (*productionProofServiceController) Logs(context.Context, serviceRuntime.ServiceName, int) (string, error) {
	return "", nil
}

func refreshProductionProofWindow(t *testing.T) {
	t.Helper()
	path := os.Getenv("LY_ROUTE_VPP_CAPABILITY_PROOF")
	proof, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	updated := strings.ReplaceAll(string(proof), "2026-06-06T11:59:00Z", now.Add(-time.Minute).Format(time.RFC3339))
	updated = strings.ReplaceAll(updated, "2026-06-06T12:01:00Z", now.Add(time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (proof *productionVPPProof) request(t *testing.T, method, path, body string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, proof.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	response, err := proof.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func saveProductionProofConfig(t *testing.T, store *persistence.Store) {
	t.Helper()
	now := fixedClock()()
	documents := []persistence.ConfigDocument{
		configDocument(t, "interface", "eth1", map[string]any{"id": "eth1", "interface_id": "eth1", "gateway_role": "lan", "cidr": "192.0.2.2/24"}, now),
		configDocument(t, "interface", "eth2", map[string]any{"id": "eth2", "interface_id": "eth2", "gateway_role": "wan", "cidr": "198.51.100.2/24"}, now),
		configDocument(t, "interface", "wan0", map[string]any{"id": "wan0", "interface_id": "wan0", "gateway_role": "wan", "cidr": "198.51.100.2/30", "gateway": "198.51.100.1"}, now),
		configDocument(t, "interface", "wan1", map[string]any{"id": "wan1", "interface_id": "wan1", "gateway_role": "wan", "cidr": "203.0.113.2/30", "gateway": "203.0.113.1"}, now),
		configDocument(t, "interface_bond", "bond-proof", map[string]any{"id": "bond-proof", "mode": "active-backup", "members": []any{"eth1"}}, now),
		configDocument(t, "wan_group", "wan-primary", map[string]any{"id": "wan-primary", "wan_members": []any{"wan0", "wan1"}, "member_weights": map[string]any{"wan0": float64(2), "wan1": float64(1)}}, now),
		configDocument(t, "route_policy", "route-office", map[string]any{"id": "route-office", "priority": float64(100), "action": "route", "egress": "wan-primary", "match": map[string]any{"sources": []any{"10.0.0.0/24"}, "destinations": []any{"0.0.0.0/0"}, "protocols": []any{"tcp"}, "source_ports": []any{"any"}, "dest_ports": []any{"443"}}}, now),
		configDocument(t, "security_acl", "acl-guest", map[string]any{"id": "acl-guest", "priority": float64(50), "action": "deny", "match": map[string]any{"sources": []any{"192.168.88.0/24"}, "destinations": []any{"0.0.0.0/0"}, "protocols": []any{"tcp"}, "source_ports": []any{"any"}, "dest_ports": []any{"443"}, "direction": "input"}}, now),
		configDocument(t, "nat_static", "static-main", map[string]any{"id": "static-main", "external_address": "203.0.113.10", "internal_address": "192.168.88.10", "wan_link": "wan0"}, now),
		configDocument(t, "port_map", "web", map[string]any{"id": "web", "protocol": "tcp", "external_address": "203.0.113.10", "external_port": float64(8443), "internal_host": "192.168.88.20", "internal_port": float64(8443), "wan_link": "wan0", "hairpin": true}, now),
	}
	for _, document := range documents {
		if err := store.SaveConfig(context.Background(), document); err != nil {
			t.Fatal(err)
		}
	}
}

func productionProofFlow() flow.Intent {
	return flow.Intent{ID: "proof", Rules: []flow.Rule{{ID: "voice", Granularity: flow.ClassGranularity, Class: "voice", Actions: []flow.Action{{Kind: flow.ActionRemark, DSCP: "46"}}}}}
}

func seedProductionProofPrior(t *testing.T, store *persistence.Store, proxyEgress proxy.Egress, intent flow.Intent, desired vpp.Plan) {
	t.Helper()
	prior := vpp.Plan{NativePath: desired.NativePath, Interfaces: append([]vpp.InterfaceState(nil), desired.Interfaces...)}
	prior.Interfaces = prior.Interfaces[:1]
	payload, hash, err := persistence.MarshalPayload(apply.SnapshotPayload{Proxy: proxyEgress, Flow: intent, GatewayPlan: &prior})
	if err != nil {
		t.Fatal(err)
	}
	err = store.SaveApply(context.Background(), persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: "snapshot-proof-prior", SourceTransactionID: "txn-proof-prior", Payload: payload, PayloadHash: hash, CreatedAt: fixedClock()().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
}

func decodeProofSnapshot(t *testing.T, snapshot persistence.RuntimeSnapshot) apply.SnapshotPayload {
	t.Helper()
	var payload apply.SnapshotPayload
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func proofStableID(value string, minimum, span int) int {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return minimum + int(binary.BigEndian.Uint32(digest[:4])%uint32(span))
}

func proofQoSMap(mapID, input, output int) string {
	zeroes := make([]string, 256)
	for index := range zeroes {
		zeroes[index] = "0"
	}
	rows := make([]string, len(zeroes))
	copy(rows, zeroes)
	rows[input] = strconv.Itoa(output)
	return fmt.Sprintf("Map-ID:%d\n  ext:[%s]\n  VLAN:[%s]\n  MPLS:[%s]\n  IP:[%s]\n", mapID, strings.Join(zeroes, ","), strings.Join(zeroes, ","), strings.Join(zeroes, ","), strings.Join(rows, ","))
}
