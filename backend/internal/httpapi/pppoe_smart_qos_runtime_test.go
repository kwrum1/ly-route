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
)

func TestRuntimePreviewSmartQoSUsesPPPoEUnderlayAndWANDownloadForLAN(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-pppoe-smart-qos-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	previousInventory := hostInterfaceInventory
	hostInterfaceInventory = func() []map[string]any {
		return []map[string]any{{"id": "eth0", "name": "eth0"}, {"id": "eth1", "name": "eth1"}, {"id": "eth2", "name": "eth2"}}
	}
	t.Cleanup(func() { hostInterfaceInventory = previousInventory })

	now := fixedClock()()
	for _, document := range []persistence.ConfigDocument{
		configDocument(t, "interface", "eth1", map[string]any{
			"id": "eth1", "interface_id": "eth1", "gateway_role": "lan", "cidr": "192.168.10.1/24",
		}, now),
		configDocument(t, "wan_link", "pppoe-wan", map[string]any{
			"id": "pppoe-wan", "interface_id": "eth2", "gateway_role": "wan", "type": "pppoe", "wan_type": "pppoe",
			"username": "subscriber", "pppoe_password": "secret", "smart_qos_download_kbps": 1000000, "smart_qos_upload_kbps": 500000,
			"ipv4": map[string]any{"mode": "pppoe"},
		}, now),
		configDocument(t, "proxy_egress", "proxy-wan", map[string]any{
			"id": "proxy-wan", "semantic_type": "proxy_egress", "display_list": "wan", "runtime_profile": "xray-vpp-service",
			"underlay_wan_id": "pppoe-wan", "capture_path": "vpp_service_interface", "engine": "xray", "handoff": "vpp_to_service", "listener_mode": "vpp-service",
		}, now),
	} {
		if err := store.SaveConfig(ctx, document); err != nil {
			t.Fatal(err)
		}
	}

	proofPath := filepath.Join(t.TempDir(), "proof.json")
	observedAt := now.Add(-time.Minute).Format(time.RFC3339)
	validUntil := now.Add(time.Minute).Format(time.RFC3339)
	proof := fmt.Sprintf(`{"management_interface":"eth0","proofs":[{"linux_interface":"eth1","candidates":[{"tier":"vpp_dpdk","hook":"dpdk","mode":"vfio_pci","source":"runtime_probe","runtime_verified":true,"native":false,"high_performance":true,"observed_at":%q,"valid_until":%q,"performance_score":80,"pci_address":"0000:03:00.0","kernel_driver":"ixgbe","iommu_group":"17","iommu_protected":true,"vfio_available":true,"hugepages_available":true,"dpdk_plugin_available":true,"smart_qos_plugin_available":true}]},{"linux_interface":"eth2","candidates":[{"tier":"vpp_dpdk","hook":"dpdk","mode":"vfio_pci","source":"runtime_probe","runtime_verified":true,"native":false,"high_performance":true,"observed_at":%q,"valid_until":%q,"performance_score":80,"pci_address":"0000:04:00.0","kernel_driver":"ixgbe","iommu_group":"18","iommu_protected":true,"vfio_available":true,"hugepages_available":true,"dpdk_plugin_available":true,"smart_qos_plugin_available":true}]}]}`, observedAt, validUntil, observedAt, validUntil)
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
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200: %s", preview.Code, preview.Body.String())
	}
	for _, required := range []string{
		`"dataplane_state":"smart_qos_ready"`,
		`"underlay_wan_id":"pppoe-wan"`,
		"set ly-route smart-qos interface lyroute-eth1 rate 1000000 host-isolation destination",
		"set ly-route smart-qos interface lyroute-eth2 rate 500000 host-isolation source",
	} {
		if !strings.Contains(preview.Body.String(), required) {
			t.Fatalf("PPPoE smart QoS preview missing %q: %s", required, preview.Body.String())
		}
	}
	if strings.Contains(preview.Body.String(), "set interface ip address lyroute-eth2") || strings.Contains(preview.Body.String(), "set dhcp client intfc lyroute-eth2") {
		t.Fatalf("PPPoE underlay received an invented IP assignment: %s", preview.Body.String())
	}
}
