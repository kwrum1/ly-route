package service

import (
	"strings"
	"testing"
)

func TestRenderVPPUnderlayReadinessUsesInterfaceInventoryLookup(t *testing.T) {
	rendered, err := renderVPPUnderlayReadiness([]string{"pppoe-runtime:wan-primary"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, `show interface address "$underlay_if"`) {
		t.Fatalf("readiness probe passed an interface argument to VPP:\n%s", rendered)
	}
	for _, required := range []string{
		`show interface address 2>/dev/null | tr -d '\r' | grep -A1 -E`,
		`^$underlay_if `,
		`grep -Eq '^  L3 [0-9]'`,
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("readiness probe missing %q:\n%s", required, rendered)
		}
	}
}
