package httpapi

import (
	"reflect"
	"testing"
)

func TestDNSServiceResolverServersUsesBootstrapOnlyForDoH(t *testing.T) {
	bootstrap := []string{"1.1.1.1", "8.8.8.8"}
	if got := dnsServiceResolverServers([]string{"https://resolver.example/dns-query"}, bootstrap); !reflect.DeepEqual(got, bootstrap) {
		t.Fatalf("DoH resolver set = %#v, want %#v", got, bootstrap)
	}
	wantMixed := []string{"9.9.9.9", "1.1.1.1", "8.8.8.8"}
	if got := dnsServiceResolverServers([]string{"9.9.9.9", "https://resolver.example/dns-query"}, bootstrap); !reflect.DeepEqual(got, wantMixed) {
		t.Fatalf("mixed resolver set = %#v, want %#v", got, wantMixed)
	}
	wantIP := []string{"223.5.5.5"}
	if got := dnsServiceResolverServers(wantIP, bootstrap); !reflect.DeepEqual(got, wantIP) {
		t.Fatalf("IP resolver set = %#v, want %#v", got, wantIP)
	}
}

func TestDNSDoHHostnameAcceptsHostnamesOnly(t *testing.T) {
	for server, want := range map[string]string{
		"https://dns.alidns.com/dns-query": "dns.alidns.com",
		"h3://cloudflare-dns.com/dns-query": "cloudflare-dns.com",
		"https://1.1.1.1/dns-query":       "",
		"8.8.8.8":                          "",
	} {
		if got := dnsDoHHostname(server); got != want {
			t.Fatalf("dnsDoHHostname(%q) = %q, want %q", server, got, want)
		}
	}
}
