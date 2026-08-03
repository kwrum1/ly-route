package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/apply"
)

func TestProductionGatewayHTTPCommitsOnlyAfterEightVPPCTLReadbacksAndRestarts(t *testing.T) {
	// Given
	proof := newProductionVPPProof(t, true)

	// When
	response := proof.request(t, http.MethodPost, "/api/v1/config/apply", `{}`, proof.cookie)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"committed"`) {
		t.Fatalf("apply = %d %s", response.StatusCode, body)
	}
	snapshots, err := proof.store.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want prior plus committed", len(snapshots))
	}
	payload := decodeProofSnapshot(t, snapshots[0])
	wantResources := []string{"interfaces", "bonds", "wan-groups", "routes", "acls", "qos", "nat44", "port-maps"}
	if len(payload.GatewayEvidence) != len(wantResources) {
		t.Fatalf("gateway evidence count = %d", len(payload.GatewayEvidence))
	}
	for index, evidence := range payload.GatewayEvidence {
		if evidence.Resource != wantResources[index] || evidence.TransactionID != snapshots[0].SourceTransactionID || evidence.BeforeHash == "" || evidence.AfterHash == "" || evidence.ApplyReceipt.Status != apply.ReceiptApplied || !evidence.Readback.Fresh {
			t.Fatalf("gateway evidence[%d] = %#v", index, evidence)
		}
		t.Logf("evidence resource=%s transaction=%s before=%s after=%s deleted=%t", evidence.Resource, evidence.TransactionID, evidence.BeforeHash, evidence.AfterHash, evidence.Deleted)
	}
	t.Logf("http_status=%d transaction=%s payload_hash=%s", response.StatusCode, snapshots[0].SourceTransactionID, snapshots[0].PayloadHash)
	traceBytes, err := os.ReadFile(proof.trace)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(traceBytes)
	firstShow := strings.Index(trace, "show interface address")
	firstMutation := strings.Index(trace, "set interface state")
	wanApply := strings.Index(trace, "ip route add table")
	routeApply := strings.Index(trace, "abf policy add")
	if firstShow < 0 || firstMutation < 0 || firstShow > firstMutation || wanApply < 0 || routeApply < 0 || wanApply > routeApply {
		t.Fatalf("production command order is incomplete:\n%s", trace)
	}
	t.Logf("vppctl_trace:\n%s", trace)
	restarted := New(WithStore(proof.store), WithClock(proof.clock))
	restartServer := httptest.NewServer(restarted.Handler())
	t.Cleanup(restartServer.Close)
	restartResponse, err := proof.client.Get(restartServer.URL + "/api/v1/runtime/status")
	if err != nil {
		t.Fatal(err)
	}
	defer restartResponse.Body.Close()
	restartBody, err := io.ReadAll(restartResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if restartResponse.StatusCode != http.StatusOK || !strings.Contains(string(restartBody), `"status":"committed"`) || !strings.Contains(string(restartBody), `"runtime_state":"running"`) {
		t.Fatalf("restart status = %d %s", restartResponse.StatusCode, restartBody)
	}
	t.Logf("restart_status=%d committed=true running=true", restartResponse.StatusCode)
}

func TestProductionGatewayHTTPFailurePreservesExactPriorAndNeverRuns(t *testing.T) {
	// Given
	proof := newProductionVPPProof(t, true)
	prior, err := proof.store.RuntimeSnapshot(context.Background(), "snapshot-proof-prior")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_VPPCTL_FAIL", "qos egress map id")

	// When
	response := proof.request(t, http.MethodPost, "/api/v1/config/apply", `{}`, proof.cookie)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if response.StatusCode != http.StatusAccepted || strings.Contains(string(body), `"status":"committed"`) || strings.Contains(string(body), `"runtime_state":"running"`) {
		t.Fatalf("failed apply = %d %s", response.StatusCode, body)
	}
	snapshots, err := proof.store.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].PayloadHash != prior.PayloadHash || !reflect.DeepEqual(snapshots[0].Payload, prior.Payload) {
		t.Fatalf("failed apply changed prior snapshot: %#v", snapshots)
	}
	t.Logf("failure_status=%d prior_hash=%s preserved=true", response.StatusCode, prior.PayloadHash)
	traceBytes, err := os.ReadFile(proof.trace)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(traceBytes)
	for _, required := range []string{"delete acl-plugin", "abf policy del", "ip table del", "delete bond", "set interface state"} {
		if !strings.Contains(trace, required) {
			t.Fatalf("rollback trace lacks %q:\n%s", required, trace)
		}
	}
	t.Logf("failure_vppctl_trace:\n%s", trace)
}

func TestProductionGatewayHTTPFirstGenerationCommitsAndLockedPathNeverCommits(t *testing.T) {
	// Given
	first := newProductionVPPProof(t, false)

	// When
	committed := first.request(t, http.MethodPost, "/api/v1/config/apply", `{}`, first.cookie)
	committedBody, err := io.ReadAll(committed.Body)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if committed.StatusCode != http.StatusOK || !strings.Contains(string(committedBody), `"status":"committed"`) {
		t.Fatalf("first generation = %d %s", committed.StatusCode, committedBody)
	}
	t.Logf("first_generation_status=%d committed=true", committed.StatusCode)
	snapshots, err := first.store.RuntimeSnapshots(context.Background())
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("first generation snapshots = %#v, error = %v", snapshots, err)
	}

	locked := newProductionVPPProof(t, false)
	t.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", filepath.Join(t.TempDir(), "missing-proof.json"))
	lockedResponse := locked.request(t, http.MethodPost, "/api/v1/config/apply", `{}`, locked.cookie)
	health := locked.request(t, http.MethodGet, "/api/v1/health", "", nil)
	management := locked.request(t, http.MethodGet, "/api/v1/management/network", "", locked.cookie)
	if lockedResponse.StatusCode != http.StatusLocked || health.StatusCode != http.StatusOK || management.StatusCode != http.StatusOK {
		t.Fatalf("locked/health/management statuses = %d/%d/%d", lockedResponse.StatusCode, health.StatusCode, management.StatusCode)
	}
	t.Logf("locked_status=%d health_status=%d management_status=%d", lockedResponse.StatusCode, health.StatusCode, management.StatusCode)
}

func TestProductionGatewayHTTPFirstGenerationFailureCleansIntroducedState(t *testing.T) {
	// Given
	proof := newProductionVPPProof(t, false)
	t.Setenv("FAKE_VPPCTL_FAIL", "qos egress map id")

	// When
	response := proof.request(t, http.MethodPost, "/api/v1/config/apply", `{}`, proof.cookie)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if response.StatusCode != http.StatusAccepted || strings.Contains(string(body), `"status":"committed"`) {
		t.Fatalf("failed first generation = %d %s", response.StatusCode, body)
	}
	trace, err := os.ReadFile(proof.trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"set interface ip address lyroute-eth1 192.0.2.2/24 del", "delete acl-plugin acl index", "abf policy del", "delete interface af_xdp"} {
		if !strings.Contains(string(trace), command) {
			t.Fatalf("first-generation cleanup lacks %q:\n%s", command, trace)
		}
	}
	snapshots, err := proof.store.RuntimeSnapshots(context.Background())
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("failed first generation persisted snapshots = %#v, error = %v", snapshots, err)
	}
}

func TestProductionGatewayPersistedEvidenceHasCanonicalIdentities(t *testing.T) {
	proof := newProductionVPPProof(t, true)
	response := proof.request(t, http.MethodPost, "/api/v1/config/apply", `{}`, proof.cookie)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("apply = %d %s", response.StatusCode, body)
	}
	snapshots, err := proof.store.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeProofSnapshot(t, snapshots[0])
	encoded, err := json.Marshal(payload.GatewayEvidence)
	if err != nil || !strings.Contains(string(encoded), `"resource":"port-maps"`) {
		t.Fatalf("persisted evidence = %s, error = %v", encoded, err)
	}
}

func TestProductionGatewayHTTPLastResourceDeletionPersistsAndReconciles(t *testing.T) {
	// Given
	proof := newProductionVPPProof(t, true)
	first := proof.request(t, http.MethodPost, "/api/v1/config/apply", `{}`, proof.cookie)
	if first.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(first.Body)
		t.Fatalf("initial apply = %d %s", first.StatusCode, body)
	}
	if err := proof.store.DeleteConfig(context.Background(), "port_map", "web"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proof.trace, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	response := proof.request(t, http.MethodPost, "/api/v1/config/apply", `{}`, proof.cookie)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"committed"`) {
		t.Fatalf("deletion apply = %d %s", response.StatusCode, body)
	}
	snapshots, err := proof.store.RuntimeSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeProofSnapshot(t, snapshots[0])
	var deletion *apply.GatewayResourceEvidence
	for index := range payload.GatewayEvidence {
		if payload.GatewayEvidence[index].Resource == "port-maps" {
			deletion = &payload.GatewayEvidence[index]
		}
	}
	if len(payload.GatewayEvidence) != 8 || deletion == nil || !deletion.Deleted || len(deletion.After.NAT.PortMappings) != 0 {
		t.Fatalf("deletion evidence = %#v", payload.GatewayEvidence)
	}
	trace, err := os.ReadFile(proof.trace)
	if err != nil || !strings.Contains(string(trace), "nat44 add static mapping tcp local 192.168.88.20 8443 external 203.0.113.10 8443 del\nshow nat44 static mappings") {
		t.Fatalf("deletion trace = %s, error = %v", trace, err)
	}
	restarted := New(WithStore(proof.store), WithClock(proof.clock))
	result := restarted.runtimeEvidence()
	if result.Status != RuntimeStatusCommitted || result.RuntimeState != RuntimeStateRunning {
		t.Fatalf("deletion restart = %#v", result)
	}
	t.Logf("deletion_status=%d evidence=%d deleted_resource=%s restart=%s/%s trace=%q", response.StatusCode, len(payload.GatewayEvidence), deletion.Resource, result.Status, result.RuntimeState, trace)
}
