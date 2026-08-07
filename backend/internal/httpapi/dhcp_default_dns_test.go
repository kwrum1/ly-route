package httpapi

import (
	"reflect"
	"testing"
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
