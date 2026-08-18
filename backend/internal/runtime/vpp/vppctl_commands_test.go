package vpp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVPPRouteBatchUsesBoundedChunks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell")
	}
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "batch-sizes")
	binary := filepath.Join(tempDir, "vppctl")
	script := "#!/bin/sh\n[ \"$1\" = exec ] || exit 2\nwc -l < \"$2\" >> \"$LY_ROUTE_TEST_BATCH_LOG\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_BATCH_LOG", logPath)

	commands := make([]string, vppRouteBatchChunkSize*2+3)
	for index := range commands {
		commands[index] = fmt.Sprintf("ip route add table 100 192.0.2.%d/32 via local0", index%256)
	}
	results, err := (vppctlChannel{binary: binary}).doVPPRouteBatch(context.Background(), commands)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("batch results = %d, want 3", len(results))
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%d\n%d\n3", vppRouteBatchChunkSize, vppRouteBatchChunkSize)
	if got := strings.TrimSpace(string(logged)); got != want {
		t.Fatalf("batch sizes = %q, want %q", got, want)
	}
}
