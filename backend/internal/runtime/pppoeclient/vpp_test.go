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
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$LY_ROUTE_TEST_LOG\"\nif [ \"$1\" = help ] && [ \"$2\" = create ] && [ \"$3\" = pppoe ]; then printf 'encap-interface\\n'; fi\nif [ \"$1\" = show ] && [ \"$2\" = pppoe ] && [ \"$3\" = session ]; then printf '[0] sw-if-index 9 client-ip 10.0.0.2 session-id 7 encap-if-index 1\\n    local-mac 00:0a:0b:0c:0d:0e client-mac 00:01:02:03:04:05\\n'; fi\nif [ \"$1\" = show ] && [ \"$2\" = interface ]; then printf 'lyroute-wan0 1 up 9000/0/0/0\\npppoe_session7 9 up 0/0/0/0\\n'; fi\nif [ \"$1\" = show ] && [ \"$2\" = ip ] && [ \"$3\" = fib ]; then printf '005056b5ca9a000c29165b1e88641100000700000021\\nstacked-on:\\n  [@1]: lyroute-wan0-tx-dpo:\\n'; fi\nif [ \"$1\" = create ] && [ \"$2\" = pppoe ] && [ \"$3\" = session ] && [ \"$NF\" != del ]; then printf 'pppoe_session7\\n'; fi\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_LOG", logPath)
	session := Session{ID: 7, LocalAddress: "10.0.0.2", RemoteAddress: "10.0.0.1", ACMAC: MAC{0, 1, 2, 3, 4, 5}}
	programmed, err := ProgramVPP(context.Background(), VPPConfig{Binary: binary, WANInterface: "lyroute-wan0", InstallDefaultRoute: true, EnableNAT: true, NATInsideInterfaces: []string{"lyroute-lan0"}}, session)
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
	for _, want := range []string{"ip route del 0.0.0.0/0 via 10.0.0.1 pppoe_session7", "ip route del 10.0.0.1/32 via 10.0.0.1 pppoe_session7", "create pppoe session client-ip 10.0.0.2 session-id 7 client-mac 00:01:02:03:04:05 encap-interface lyroute-wan0 decap-vrf-id 0 del", "ip route add 0.0.0.0/0 via 10.0.0.1 pppoe_session7", "set interface nat44 in pppoe_session7 output-feature", "nat44 add interface address pppoe_session7", "nat44 add interface address pppoe_session7 del", "set interface nat44 in pppoe_session7 output-feature del"} {
		if !strings.Contains(log, want) {
			t.Fatalf("VPP command log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "set interface nat44 in lyroute-lan0 out pppoe_session7\n") {
		t.Fatalf("endpoint-dependent NAT was installed before multi-WAN route selection:\n%s", log)
	}
	if strings.Contains(log, "nat44 add address 10.0.0.2") {
		t.Fatalf("PPPoE output-feature must use the interface-bound address, not a global pool address:\n%s", log)
	}
}

func TestFullConeNATActivationPreservesOtherPPPoEWANs(t *testing.T) {
	commands := natPluginActivationCommands(true)
	parts := make([]string, 0, len(commands))
	for _, command := range commands {
		parts = append(parts, strings.Join(command, " "))
	}
	log := strings.Join(parts, "\n")
	if strings.Contains(log, "nat44 ei plugin disable") {
		t.Fatalf("full-cone activation resets all connected WANs:\n%s", log)
	}
	if strings.Contains(log, "nat44 plugin disable") {
		t.Fatalf("full-cone activation resets the gateway-wide NAT plugin:\n%s", log)
	}
	if !strings.Contains(log, "nat44 ei plugin enable") {
		t.Fatalf("full-cone activation must enable NAT44-EI:\n%s", log)
	}
}

func TestPPPoERewriteUsesExpectedWAN(t *testing.T) {
	output := "005056b5ca9a000c29165b1e88641100000700000021\nstacked-on:\n  [@3]: lyroute-ens35-tx-dpo:"
	if !pppoeRewriteUsesWAN(output, "lyroute-ens35") {
		t.Fatal("expected FIB rewrite to match its configured WAN")
	}
	if pppoeRewriteUsesWAN(output, "lyroute-ens33") {
		t.Fatal("accepted PPPoE rewrite stacked on a different WAN")
	}
}

func TestPPPoERewriteProbeRejectsSessionStackedOnDifferentWAN(t *testing.T) {
	temp := t.TempDir()
	binary := filepath.Join(temp, "vppctl")
	script := `#!/bin/sh
if [ "$1" = show ] && [ "$2" = ip ] && [ "$3" = fib ]; then
  printf '886411000001\nstacked-on:\n  [@1]: lyroute-ens33-tx-dpo:\n'
fi
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	matches, err := pppoeRewriteMatchesSession(context.Background(), VPPConfig{
		Binary: binary, WANInterface: "lyroute-ens35", ExplicitWAN: true,
	}, Session{ID: 1, RemoteAddress: "10.68.0.1"}, "pppoe_session8")
	if err != nil {
		t.Fatal(err)
	}
	if matches {
		t.Fatal("accepted PPPoE session stacked on a different WAN")
	}
}

func TestConfigureDelegatedPrefixUsesInfiniteRALifetime(t *testing.T) {
	temp := t.TempDir()
	logPath := filepath.Join(temp, "vppctl.log")
	binary := filepath.Join(temp, "vppctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$LY_ROUTE_TEST_LOG\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_LOG", logPath)
	err := configureDelegatedPrefix(context.Background(), VPPConfig{Binary: binary, IPv6PrefixGroup: "pd", IPv6LANInterfaces: []string{"lyroute-lan0"}}, "2001:db8:100::/56")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "ip6 nd lyroute-lan0 prefix 2001:db8:100::/64 infinite") {
		t.Fatalf("RA prefix command missing explicit infinite lifetime:\n%s", content)
	}
	for _, want := range []string{
		"ip6 nd lyroute-lan0 no ra-managed-config-flag",
		"ip6 nd lyroute-lan0 no ra-other-config-flag",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("RA command log missing %q:\n%s", want, content)
		}
	}
	remove := strings.Index(string(content), "ip6 nd lyroute-lan0 no prefix 2001:db8:100::/64")
	create := strings.Index(string(content), "ip6 nd lyroute-lan0 prefix 2001:db8:100::/64 infinite")
	if remove < 0 || create < 0 || remove > create {
		t.Fatalf("RA prefix must remove VPP's synthesized no-autoconfig entry before creating the SLAAC entry:\n%s", content)
	}
}

func TestCreateFreshPPPoESessionReleasesStaleSlotsAfterVerifiedAllocation(t *testing.T) {
	temp := t.TempDir()
	logPath := filepath.Join(temp, "vppctl.log")
	statePath := filepath.Join(temp, "vppctl.state")
	binary := filepath.Join(temp, "vppctl")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LY_ROUTE_TEST_LOG"
count=0
[ ! -f "$LY_ROUTE_TEST_STATE" ] || count=$(cat "$LY_ROUTE_TEST_STATE")
if [ "$1" = create ] && [ "$2" = pppoe ] && [ "$3" = session ] && [ "$NF" != del ]; then
  case "$count" in
    0) printf 'pppoe_session0\n' ;;
    1) printf 'pppoe_session0\n' ;;
    *) printf 'pppoe_session1\n' ;;
  esac
  count=$((count + 1))
  printf '%s' "$count" > "$LY_ROUTE_TEST_STATE"
fi
if [ "$1" = show ] && [ "$2" = ip ] && [ "$3" = fib ]; then
  if [ "$count" -ge 3 ]; then
    printf '005056b5ca9a000c29165b1e88641100000700000021\n'
  else
    printf '005056b5ca9a000c29165b1e88641100000100000021\n'
  fi
fi
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_LOG", logPath)
	t.Setenv("LY_ROUTE_TEST_STATE", statePath)
	session := Session{ID: 7, LocalAddress: "10.0.0.2", RemoteAddress: "10.0.0.1", ACMAC: MAC{0, 1, 2, 3, 4, 5}}
	interfaceName, staleSlots, err := createFreshVPPPPPoESession(context.Background(), VPPConfig{Binary: binary}, session)
	if err != nil {
		t.Fatal(err)
	}
	if interfaceName != "pppoe_session1" || len(staleSlots) != 0 {
		t.Fatalf("fresh allocation = %q %#v", interfaceName, staleSlots)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "client-ip 198.18.254.254 session-id 65535 client-mac 00:01:02:03:04:05 decap-vrf-id 0 del") || !strings.Contains(string(content), "client-ip 198.18.254.1 session-id 65534 client-mac 00:01:02:03:04:05 decap-vrf-id 0 del") {
		t.Fatalf("verified allocation did not release every stale slot:\n%s", content)
	}
	if !strings.Contains(string(content), "client-ip 198.18.254.254 session-id 65535") {
		t.Fatalf("initial PPPoE slot was not reserved before the negotiated session:\n%s", content)
	}
}

func TestCreateFreshPPPoESessionRetriesProbeParseError(t *testing.T) {
	temp := t.TempDir()
	logPath := filepath.Join(temp, "vppctl.log")
	statePath := filepath.Join(temp, "vppctl.state")
	binary := filepath.Join(temp, "vppctl")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LY_ROUTE_TEST_LOG"
count=0
[ ! -f "$LY_ROUTE_TEST_STATE" ] || count=$(cat "$LY_ROUTE_TEST_STATE")
if [ "$1" = create ] && [ "$2" = pppoe ] && [ "$3" = session ] && [ "$NF" != del ]; then
  case "$count" in
    0) printf 'pppoe_session0\n' ;;
    1) printf 'pppoe_session0\n' ;;
    2) printf 'pppoe_session1\n' ;;
    *) printf 'pppoe_session1\n' ;;
  esac
  count=$((count + 1))
  printf '%s' "$count" > "$LY_ROUTE_TEST_STATE"
fi
if [ "$1" = ip ] && [ "$2" = route ] && [ "$3" = add ] && [ "$count" -eq 2 ]; then
  printf 'ip route: parse error\n' >&2
  exit 1
fi
if [ "$1" = show ] && [ "$2" = ip ] && [ "$3" = fib ] && [ "$count" -ge 4 ]; then
  printf '005056b5ca9a000c29165b1e88641100000700000021\n'
fi
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_LOG", logPath)
	t.Setenv("LY_ROUTE_TEST_STATE", statePath)
	session := Session{ID: 7, LocalAddress: "10.0.0.2", RemoteAddress: "10.0.0.1", ACMAC: MAC{0, 1, 2, 3, 4, 5}}
	interfaceName, staleSlots, err := createFreshVPPPPPoESession(context.Background(), VPPConfig{Binary: binary}, session)
	if err != nil {
		t.Fatal(err)
	}
	if interfaceName != "pppoe_session1" || len(staleSlots) != 0 {
		t.Fatalf("probe retry allocation = %q %#v", interfaceName, staleSlots)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(content)
	for _, want := range []string{
		"create pppoe session client-ip 198.18.254.254 session-id 65535 client-mac 00:01:02:03:04:05 decap-vrf-id 0 del",
		"create pppoe session client-ip 198.18.254.1 session-id 65534 client-mac 00:01:02:03:04:05 decap-vrf-id 0 del",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("probe parse error did not release stale slot %q:\n%s", want, log)
		}
	}
}

func TestParseVPPPPPoESessions(t *testing.T) {
	output := `[0] sw-if-index 9 client-ip 10.0.0.2 session-id 7 encap-if-index 1
	    local-mac 00:0a:0b:0c:0d:0e client-mac 00:01:02:03:04:05
[1] sw-if-index 10 client-ip 10.0.0.3 session-id 8 encap-if-index 1
    local-mac 00:0a:0b:0c:0d:0e client-mac 00:01:02:03:04:06`
	entries, err := parseVPPPPPoESessions(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].SessionID != 7 || entries[0].ClientMAC != "00:01:02:03:04:05" || entries[0].SwIfIndex != 9 || entries[0].EncapIfIndex != 1 {
		t.Fatalf("sessions = %#v, want two parsed entries", entries)
	}
}

func TestParseVPPInterfaceIndexesResolvesRenamedSession(t *testing.T) {
	output := `              Name               Idx    State  MTU (L3/IP4/IP6/MPLS)
lyroute-wan0                     1      up          9000/0/0/0
pppoe_session_ly12345678         9      up             0/0/0/0`
	byIndex, byName := parseVPPInterfaceIndexes(output)
	if byIndex[9] != "pppoe_session_ly12345678" || byName["lyroute-wan0"] != 1 {
		t.Fatalf("interface indexes = %#v %#v", byIndex, byName)
	}
}

func TestProgramVPPKeepsNegotiatedSessionNameBeforeProgrammingRoutes(t *testing.T) {
	temp := t.TempDir()
	logPath := filepath.Join(temp, "vppctl.log")
	binary := filepath.Join(temp, "vppctl")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LY_ROUTE_TEST_LOG"
if [ "$1" = help ]; then printf 'encap-interface\n'; fi
if [ "$1" = show ] && [ "$2" = pppoe ]; then printf '[7] sw-if-index 9 client-ip 10.0.0.2 session-id 7 encap-if-index 1\n    local-mac 00:0a:0b:0c:0d:0e client-mac 00:01:02:03:04:05\n'; fi
if [ "$1" = show ] && [ "$2" = interface ]; then printf 'lyroute-wan0 1 up 9000/0/0/0\npppoe_session7 9 up 0/0/0/0\n'; fi
if [ "$1" = show ] && [ "$2" = ip ] && [ "$3" = fib ]; then printf '005056b5ca9a000c29165b1e88641100000700000021\nstacked-on:\n  [@1]: lyroute-wan0-tx-dpo:\n'; fi
if [ "$1" = create ] && [ "$2" = pppoe ] && [ "$3" = session ] && [ "$NF" != del ]; then printf 'pppoe_session7\n'; fi
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_LOG", logPath)
	session := Session{ID: 7, LocalAddress: "10.0.0.2", RemoteAddress: "10.0.0.1", ACMAC: MAC{0, 1, 2, 3, 4, 5}}
	programmed, err := ProgramVPP(context.Background(), VPPConfig{Binary: binary, WANInterface: "lyroute-wan0", SessionInterface: "pppoe_session_ly12345678", InstallDefaultRoute: true}, session)
	if err != nil {
		t.Fatal(err)
	}
	if programmed.Interface != "pppoe_session7" {
		t.Fatalf("programmed interface = %q", programmed.Interface)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(content)
	if strings.Contains(log, "set interface name") || !strings.Contains(log, "ip route add 0.0.0.0/0 via 10.0.0.1 pppoe_session7") {
		t.Fatalf("negotiated interface was not used directly:\n%s", log)
	}
}

func TestProgramVPPKeepsLegacyPoolSlotName(t *testing.T) {
	temp := t.TempDir()
	logPath := filepath.Join(temp, "vppctl.log")
	binary := filepath.Join(temp, "vppctl")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LY_ROUTE_TEST_LOG"
if [ "$1" = help ]; then printf 'encap-interface\n'; fi
if [ "$1" = show ] && [ "$2" = pppoe ]; then printf '[7] sw-if-index 9 client-ip 10.0.0.2 session-id 7 encap-if-index 1\n    local-mac 00:0a:0b:0c:0d:0e client-mac 00:01:02:03:04:05\n'; fi
if [ "$1" = show ] && [ "$2" = interface ]; then printf 'lyroute-wan0 1 up 9000/0/0/0\nlegacy_pppoe_slot 9 up 0/0/0/0\n'; fi
if [ "$1" = show ] && [ "$2" = ip ] && [ "$3" = fib ]; then printf '005056b5ca9a000c29165b1e88641100000700000021\nstacked-on:\n  [@1]: lyroute-wan0-tx-dpo:\n'; fi
if [ "$1" = create ] && [ "$2" = pppoe ] && [ "$3" = session ] && [ "$NF" != del ]; then printf 'legacy_pppoe_slot\n'; fi
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_LOG", logPath)
	session := Session{ID: 7, LocalAddress: "10.0.0.2", RemoteAddress: "10.0.0.1", ACMAC: MAC{0, 1, 2, 3, 4, 5}}
	programmed, err := ProgramVPP(context.Background(), VPPConfig{Binary: binary, WANInterface: "lyroute-wan0", SessionInterface: "pppoe_session_ly12345678"}, session)
	if err != nil {
		t.Fatal(err)
	}
	if programmed.Interface != "legacy_pppoe_slot" {
		t.Fatalf("programmed interface = %q", programmed.Interface)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "set interface name") {
		t.Fatalf("legacy pool slot was renamed:\n%s", content)
	}
}

func TestRemoveVPPPPoESessionsKeepsOtherWANOnDifferentAC(t *testing.T) {
	temp := t.TempDir()
	logPath := filepath.Join(temp, "vppctl.log")
	binary := filepath.Join(temp, "vppctl")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LY_ROUTE_TEST_LOG"
if [ "$1" = show ] && [ "$2" = pppoe ]; then
  printf '[0] sw-if-index 9 client-ip 10.0.0.2 session-id 7 encap-if-index 1\n    local-mac 00:0a:0b:0c:0d:0e client-mac 00:01:02:03:04:05\n'
  printf '[1] sw-if-index 10 client-ip 10.0.0.3 session-id 8 encap-if-index 2\n    local-mac 00:0a:0b:0c:0d:0f client-mac 00:01:02:03:04:06\n'
fi
if [ "$1" = show ] && [ "$2" = interface ]; then
  printf 'lyroute-wan0 1 up 9000/0/0/0\npppoe_session_lyblue 9 up 0/0/0/0\nlyroute-wan1 2 up 9000/0/0/0\npppoe_session_lygreen 10 up 0/0/0/0\n'
fi
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LY_ROUTE_TEST_LOG", logPath)
	session := Session{LocalAddress: "10.0.0.9", RemoteAddress: "10.0.0.1", ACMAC: MAC{0, 1, 2, 3, 4, 5}}
	config := VPPConfig{Binary: binary, WANInterface: "lyroute-wan0", ExplicitWAN: true}
	if err := removeVPPPPPoESessions(context.Background(), config, session); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(content)
	if !strings.Contains(log, "client-ip 10.0.0.2 session-id 7") {
		t.Fatalf("selected WAN session was not removed:\n%s", log)
	}
	if strings.Contains(log, "client-ip 10.0.0.3 session-id 8") || strings.Contains(log, "client-mac 00:01:02:03:04:06") {
		t.Fatalf("other independent WAN session was removed:\n%s", log)
	}
}

func TestParseVPPPPPoEABFPoliciesFindsOnlyRequestedInterface(t *testing.T) {
	output := `abf:[4]: policy:12037 acl:5
     path-list:[147] locks:1 flags:shared,no-uRPF
      path:[170] pl-index:147 ip4 weight=1 pref=0 attached-nexthop:
        10.67.0.1 pppoe_session0 (p2p)
abf:[5]: policy:16469 acl:3
     path-list:[148] locks:1 flags:shared,no-uRPF
      path:[171] pl-index:148 ip4 weight=1 pref=0 attached-nexthop:
        10.67.0.1 pppoe_session1 (p2p)`
	policies, err := parseVPPPPPoEABFPolicies(output, "pppoe_session0")
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].PolicyID != 12037 || policies[0].ACLID != 5 || policies[0].NextHop != "10.67.0.1" {
		t.Fatalf("parsed ABF policies = %#v", policies)
	}
}

func TestAFPacketLinuxInterfaceReadsHostInterfaceIdentity(t *testing.T) {
	hardware := `lyroute-ens33                      1     up   host-ens33
  Ethernet address 02:fe:d3:cb:3d:72
  Linux PACKET socket interface v3`
	if got := afPacketLinuxInterface(hardware, "lyroute-ens33"); got != "ens33" {
		t.Fatalf("AF_PACKET host interface = %q, want ens33", got)
	}
	if got := afPacketLinuxInterface("VIRTIO interface", "lyroute-ens33"); got != "" {
		t.Fatalf("non-AF_PACKET host interface = %q, want empty", got)
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

func TestVPPPPoEFIBTablesFindsEveryReferencedTable(t *testing.T) {
	output := `ipv4-VRF:0, fib_index:0
0.0.0.0/0
  [0] via 10.0.0.1 pppoe_session0
ipv4-VRF:100, fib_index:1
0.0.0.0/0
  [0] via local0
ipv4-VRF:38136, fib_index:4
0.0.0.0/0
  [0] via 10.0.0.1 pppoe_session0
ipv4-VRF:93153, fib_index:5
10.0.0.1/32
  [0] via 10.0.0.1 pppoe_session0`
	got := vppPPPoEFIBTables(output, "pppoe_session0")
	if len(got) != 3 || got[0] != 0 || got[1] != 38136 || got[2] != 93153 {
		t.Fatalf("FIB tables = %#v, want [0 38136 93153]", got)
	}
}
