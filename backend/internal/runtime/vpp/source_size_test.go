package vpp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSnapshotLifecycleSourceFilesStayBelowPureLOCLimit(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	for _, name := range []string{
		"acl_qos_lifecycle.go",
		"interface_bond_apply.go",
		"interface_lifecycle.go",
		"nat44_port_map_lifecycle.go",
		"nat44_readback.go",
		"route_wan_group_lifecycle.go",
		"snapshot_types.go",
		"snapshot_dispatch.go",
		"snapshot_selection.go",
		"vppctl_client.go",
		"vppctl_decode.go",
		"vppctl_decode_acl.go",
		"vppctl_decode_interfaces.go",
		"vppctl_decode_nat.go",
		"vppctl_decode_policy.go",
		"vppctl_decode_qos.go",
	} {
		path := filepath.Join(filepath.Dir(currentFile), name)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := pureLOC(string(source)); got >= 250 {
			t.Fatalf("%s pure LOC = %d, want < 250", name, got)
		}
	}
}

func pureLOC(source string) int {
	lines := strings.Split(source, "\n")
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
			count++
		}
	}
	return count
}
