package vpp

import (
	"fmt"
	"strings"
	"testing"
)

func TestVerifyDNSTransparentReadbackRequiresNativePlugin(t *testing.T) {
	interception := DNSTransparentInterception{
		LANInterface: "lyroute-lan0",
		IPv4Prefixes: []string{"192.168.50.0/24"},
		IPv6Prefixes: []string{"2001:db8:100::/64"},
	}
	results := []VPPCTLCommandResult{
		{Command: "show ly-route dns-intercept", Stdout: "enabled 1 interface lyroute-lan0 fib-index 1\n"},
		{Command: fmt.Sprintf("show ip fib table %d", dnsIPv4TableID), Stdout: "0.0.0.0/0\n192.168.50.0/24\n"},
	}
	if err := verifyDNSTransparentReadback(interception, results); err != nil {
		t.Fatalf("valid native DNS interception readback: %v", err)
	}
	results[0].Stdout = "enabled 0 interface lyroute-lan0 fib-index 1\n"
	if err := verifyDNSTransparentReadback(interception, results); err == nil {
		t.Fatal("missing native DNS interception should fail readback")
	}
}

func TestDNSTransparentCommandsUseNativeInterceptorOnly(t *testing.T) {
	commands := strings.Join(dnsTransparentCommands(DNSTransparentInterception{
		LANInterface: "lyroute-lan0",
		IPv4Prefixes: []string{"192.168.50.1/24"},
	}), "\n")
	if strings.Contains(commands, "abf ") || strings.Contains(commands, "acl-plugin") {
		t.Fatalf("DNS interception must not rebuild ABF/ACL state: %s", commands)
	}
	for _, required := range []string{"set ly-route dns-intercept interface lyroute-lan0 table 100", "ip route add table 100 0.0.0.0/0 via local", "show ly-route dns-intercept"} {
		if !strings.Contains(commands, required) {
			t.Fatalf("DNS interception commands missing %q: %s", required, commands)
		}
	}
}
