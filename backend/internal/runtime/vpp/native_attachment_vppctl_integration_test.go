package vpp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestAFXDPZeroCopyAttachmentFailClosedVPPIntegration(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LY_ROUTE_NATIVE_VPPCTL"))
	if binary == "" {
		t.Skip("set LY_ROUTE_NATIVE_VPPCTL to a stock VPP 25.10 vppctl wrapper")
	}
	now := time.Now().UTC()
	proof := CapabilityProof{
		Hook: NativeHookAFXDP, Mode: NativeModeZeroCopy, Source: ProofSourceRuntimeProbe,
		RuntimeVerified: true, Native: true, HighPerformance: true,
		ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute),
	}
	plan := Plan{RequestID: "native-vpp-fail-closed", NativePath: NativePathRequest{
		ManagementInterface: "mgmt0", Now: now,
		Assignments: []NativeAssignment{{LinuxInterface: "testxdp0", Explicit: true, Proof: proof}},
	}}

	_, err := (Adapter{Client: NewVPPCTLClient(binary)}).ApplySupplemental(context.Background(), plan, SupplementalInterfaces)
	if err == nil || !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("unsupported zero-copy attachment error = %v, want semantic fail-closed readback", err)
	}
	output, commandErr := exec.Command(binary, "show", "interface").CombinedOutput()
	if commandErr != nil {
		t.Fatalf("show interface: %v: %s", commandErr, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if len(strings.Fields(line)) > 0 && strings.Fields(line)[0] == "lyroute-testxdp0" {
			t.Fatalf("failed zero-copy attachment left a VPP interface behind:\n%s", output)
		}
	}
}
