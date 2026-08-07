package httpapi

import (
	"slices"
	"testing"

	"ly-route/backend/internal/runtime/vpp"
)

func TestProxyServiceReturnRoutesIncludesMatchingDNSHosts(t *testing.T) {
	assignments := []vpp.AddressAssignment{
		{Role: "lan", CIDR: "192.168.88.1/24"},
		{Role: "wan", CIDR: "10.67.0.10/32"},
	}
	networks := []vpp.DNSServiceNetwork{
		{WANEgressID: "proxy-wan", HostAddress: "198.19.235.62"},
		{WANEgressID: "wan-pppoe", HostAddress: "198.19.28.18"},
		{WANEgressID: "proxy-wan", HostAddress: "not-an-address"},
	}

	routes := proxyServiceReturnRoutes(assignments, networks, "proxy-wan")
	want := []string{"192.168.88.0/24", "198.19.235.62/32"}
	if !slices.Equal(routes, want) {
		t.Fatalf("proxy return routes = %v, want %v", routes, want)
	}
}

func TestProxyServiceReturnRoutesDeduplicatesDNSHosts(t *testing.T) {
	networks := []vpp.DNSServiceNetwork{
		{WANEgressID: "proxy-wan", HostAddress: "198.19.235.62"},
		{WANEgressID: "proxy-wan", HostAddress: "198.19.235.62"},
	}

	routes := proxyServiceReturnRoutes(nil, networks, "proxy-wan")
	want := []string{"198.19.235.62/32"}
	if !slices.Equal(routes, want) {
		t.Fatalf("proxy return routes = %v, want %v", routes, want)
	}
}
