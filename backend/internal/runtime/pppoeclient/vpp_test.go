package pppoeclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProgramVPPConfiguresAndRemovesNATInsideInterfaces(t *testing.T) {
	temp := t.TempDir()
	logPath := filepath.Join(temp, "vppctl.log")
	binary := filepath.Join(temp, "vppctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$LY_ROUTE_TEST_LOG\"\nif [ \"$1\" = create ] && [ \"$2\" = pppoe ] && [ \"$3\" = session ]; then printf 'pppoe_session7\\n'; fi\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_LOG", logPath)
	session := Session{ID: 7, LocalAddress: "10.0.0.2", RemoteAddress: "10.0.0.1", ACMAC: MAC{0, 1, 2, 3, 4, 5}}
	programmed, err := ProgramVPP(context.Background(), VPPConfig{Binary: binary, InstallDefaultRoute: true, EnableNAT: true, NATInsideInterfaces: []string{"lyroute-lan0"}}, session)
	if err != nil {
		t.Fatal(err)
	}
	if programmed.Interface != "pppoe_session7" {
		t.Fatalf("programmed interface = %q", programmed.Interface)
	}
	if err := programmed.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(content)
	for _, want := range []string{"ip route del 0.0.0.0/0 via 10.0.0.1 pppoe_session7", "ip route add 0.0.0.0/0 via 10.0.0.1 pppoe_session7", "set interface nat44 in lyroute-lan0", "set interface nat44 out pppoe_session7 output-feature del", "set interface nat44 out pppoe_session7 output-feature", "set interface nat44 in lyroute-lan0 del"} {
		if !strings.Contains(log, want) {
			t.Fatalf("VPP command log missing %q:\n%s", want, log)
		}
	}
}

func TestVPPRoutePathOmitsUnspecifiedPPPoENextHop(t *testing.T) {
	if got := strings.Join(vppRoutePath("0.0.0.0", "pppoe_session0"), " "); got != "via pppoe_session0" {
		t.Fatalf("unspecified IPv4 route path = %q", got)
	}
	if got := vppPeerRouteCommand("0.0.0.0", "pppoe_session0"); got != nil {
		t.Fatalf("unspecified peer route = %#v, want nil", got)
	}
	if got := strings.Join(vppRoutePath("10.0.0.1", "pppoe_session0"), " "); got != "via 10.0.0.1 pppoe_session0" {
		t.Fatalf("specified route path = %q", got)
	}
}
