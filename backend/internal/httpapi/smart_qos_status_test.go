package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/vpp"
)

func TestSmartQoSStatusIsImmutableAndFailsClosedWithoutProof(t *testing.T) {
	server := New(WithClock(fixedClock()))
	status := request(t, server, http.MethodGet, "/api/v1/flow-control/smart-qos")
	if status.Code != http.StatusOK {
		t.Fatalf("GET smart QoS status = %d: %s", status.Code, status.Body.String())
	}
	for _, required := range []string{`"enabled":true`, `"mutable":false`, `"configuration_mode":"built_in"`, `"runtime_state":"locked"`, `"low_level_controls":[]`} {
		if !strings.Contains(status.Body.String(), required) {
			t.Fatalf("smart QoS status missing %q: %s", required, status.Body.String())
		}
	}

	mutation := requestBody(t, server, http.MethodPost, "/api/v1/flow-control/smart-qos", `{"queue_limit":1024}`)
	if mutation.Code != http.StatusMethodNotAllowed || !strings.Contains(mutation.Body.String(), "read-only") {
		t.Fatalf("POST smart QoS status = %d: %s", mutation.Code, mutation.Body.String())
	}
}

func TestSmartQoSStatusReportsQualifiedTierButPendingRuntimeReadback(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "eth0", "name": "eth0"}, {"id": "eth1", "name": "eth1"}}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-smart-qos-status-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := fixedClock()().UTC()
	if err := store.SaveConfig(ctx, configDocument(t, "interface", "eth1", map[string]any{"id": "eth1", "interface_id": "eth1", "gateway_role": "lan"}, now)); err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(t.TempDir(), "proof.json")
	proof := fmt.Sprintf(`{"management_interface":"eth0","proofs":[{"linux_interface":"eth1","candidates":[{"tier":"vpp_dpdk","hook":"dpdk","mode":"vfio_pci","source":"runtime_probe","runtime_verified":true,"native":false,"high_performance":true,"observed_at":%q,"valid_until":%q,"performance_score":80,"pci_address":"0000:03:00.0","kernel_driver":"ixgbe","iommu_group":"17","iommu_protected":true,"vfio_available":true,"hugepages_available":true,"dpdk_plugin_available":true,"hqos_available":true,"smart_qos_plugin_available":true}]}]}`, now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(proofPath, []byte(proof), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", proofPath)
	server := New(WithStore(store), WithClock(fixedClock()))

	status := request(t, server, http.MethodGet, "/api/v1/flow-control/smart-qos")
	if status.Code != http.StatusOK {
		t.Fatalf("GET smart QoS status = %d: %s", status.Code, status.Body.String())
	}
	for _, required := range []string{`"runtime_state":"adapter_pending"`, `"selected_dataplane_tier":"vpp_dpdk"`, `"implementation":"ly_route_vpp_smart_qos"`, "no active runtime readback"} {
		if !strings.Contains(status.Body.String(), required) {
			t.Fatalf("qualified smart QoS status missing %q: %s", required, status.Body.String())
		}
	}
	verified := New(WithStore(store), WithClock(fixedClock()), WithSmartQoSRuntime(fakeSmartQoSObserver{}, true))
	running := request(t, verified, http.MethodGet, "/api/v1/flow-control/smart-qos")
	if running.Code != http.StatusOK || !strings.Contains(running.Body.String(), `"runtime_state":"running"`) || !strings.Contains(running.Body.String(), "active and verified") {
		t.Fatalf("verified smart QoS status = %d: %s", running.Code, running.Body.String())
	}
}

func TestSmartQoSStatusUsesExpiredProofOnlyForLiveReadback(t *testing.T) {
	previous := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "eth0", "name": "eth0"}, {"id": "eth1", "name": "eth1"}}
	}
	t.Cleanup(func() { hostInterfaceInventory = previous })
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-smart-qos-expired-proof-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := fixedClock()().UTC()
	if err := store.SaveConfig(ctx, configDocument(t, "interface", "eth1", map[string]any{"id": "eth1", "interface_id": "eth1", "gateway_role": "lan"}, now)); err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(t.TempDir(), "proof.json")
	proof := fmt.Sprintf(`{"management_interface":"eth0","proofs":[{"linux_interface":"eth1","candidates":[{"tier":"vpp_dpdk","hook":"dpdk","mode":"vfio_pci","source":"runtime_probe","runtime_verified":true,"native":false,"high_performance":true,"observed_at":%q,"valid_until":%q,"performance_score":80,"pci_address":"0000:03:00.0","kernel_driver":"ixgbe","iommu_group":"17","iommu_protected":true,"vfio_available":true,"hugepages_available":true,"dpdk_plugin_available":true,"hqos_available":true,"smart_qos_plugin_available":true}]}]}`, now.Add(-10*time.Minute).Format(time.RFC3339), now.Add(-9*time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(proofPath, []byte(proof), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_VPP_CAPABILITY_PROOF", proofPath)

	withoutReadback := New(WithStore(store), WithClock(fixedClock()))
	locked := request(t, withoutReadback, http.MethodGet, "/api/v1/flow-control/smart-qos")
	if !strings.Contains(locked.Body.String(), `"runtime_state":"locked"`) {
		t.Fatalf("expired proof without live readback must remain locked: %s", locked.Body.String())
	}

	withReadback := New(WithStore(store), WithClock(fixedClock()), WithSmartQoSRuntime(fakeSmartQoSObserver{}, true))
	running := request(t, withReadback, http.MethodGet, "/api/v1/flow-control/smart-qos")
	if !strings.Contains(running.Body.String(), `"runtime_state":"running"`) || !strings.Contains(running.Body.String(), `"selected_dataplane_tier":"vpp_dpdk"`) {
		t.Fatalf("expired proof with successful live readback = %s", running.Body.String())
	}
}

type fakeSmartQoSObserver struct{ err error }

func (observer fakeSmartQoSObserver) VerifySmartQoS(context.Context, vpp.NativePath) error {
	return observer.err
}
