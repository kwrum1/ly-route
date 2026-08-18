package httpapi

import (
	"context"
	"reflect"
	"testing"

	"ly-route/backend/internal/runtime/vpp"
)

func TestDHCPNameServersDefaultsToLANRouter(t *testing.T) {
	routers := []string{"192.168.88.1"}
	if got := dhcpNameServers(nil, routers); !reflect.DeepEqual(got, routers) {
		t.Fatalf("DHCP name servers = %#v, want %#v", got, routers)
	}
}

func TestDHCPNameServersPreservesExplicitConfiguration(t *testing.T) {
	configured := []string{"223.5.5.5"}
	if got := dhcpNameServers(configured, []string{"192.168.88.1"}); !reflect.DeepEqual(got, configured) {
		t.Fatalf("DHCP name servers = %#v, want %#v", got, configured)
	}
}

func TestDHCPLANControlInterfaceDerivesRouterFromLANAddress(t *testing.T) {
	server := &Server{}
	controlInterface, router, ok := server.dhcpLANControlInterface(context.Background(), "ens34", []vpp.AddressAssignment{{
		ID:             "ens34",
		LinuxInterface: "ens34",
		VPPInterface:   "lyroute-ens34",
		CIDR:           "192.168.50.1/24",
		Role:           "lan",
	}})
	if !ok {
		t.Fatal("LAN assignment was not resolved")
	}
	if controlInterface != "lylan-ens34" {
		t.Fatalf("control interface = %q, want lylan-ens34", controlInterface)
	}
	if router != "192.168.50.1" {
		t.Fatalf("router = %q, want 192.168.50.1", router)
	}
}
