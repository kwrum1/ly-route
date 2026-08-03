package vpp

import (
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestBuildOperationsDefaultWANDenyNeverAttachesToLAN(t *testing.T) {
	acl := trafficpolicy.SecurityACL{
		ID:     "sec-acl-default-deny-wan",
		Action: "deny",
		Match: trafficpolicy.Match{
			Sources:      []string{"0.0.0.0/0"},
			Destinations: []string{"0.0.0.0/0"},
			Protocols:    []string{"any"},
			Direction:    "input",
		},
	}
	commands := strings.Join(securityACLCommands(acl, "lyroute-eth2"), "\n")
	if !strings.Contains(commands, "intfc lyroute-eth2") || strings.Contains(commands, "intfc lyroute-eth1") {
		t.Fatalf("default WAN deny attachment = %q", commands)
	}
}
