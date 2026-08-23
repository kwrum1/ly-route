package proxy

import (
	"context"
	"crypto/tls"
	"net/netip"
	"testing"
)

func TestSubscriptionFetcherAddressGate(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "::1", "fe80::1"} {
		if allowedSubscriptionAddress(netip.MustParseAddr(value)) {
			t.Fatalf("private subscription endpoint %s was allowed", value)
		}
	}
	if !allowedSubscriptionAddress(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public subscription endpoint was rejected")
	}
}

func TestSubscriptionFetcherAllowsPrivateFixtureOnlyWhenExplicitlyEnabled(t *testing.T) {
	privateAddress := netip.MustParseAddr("10.1.18.100")
	if allowedSubscriptionAddress(privateAddress) {
		t.Fatal("private fixture endpoint was allowed without the explicit test switch")
	}
	t.Setenv("LY_ROUTE_ALLOW_PRIVATE_SUBSCRIPTION_ENDPOINTS", "1")
	if !allowedSubscriptionAddress(privateAddress) {
		t.Fatal("private fixture endpoint was rejected with the explicit test switch")
	}
}

func TestSubscriptionFetcherRequiresHTTPSAndNoUserinfo(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/sub", "https://user@example.com/sub", "not-a-url"} {
		if _, err := validateSubscriptionEndpoint(context.Background(), endpoint); err == nil {
			t.Fatalf("invalid endpoint %q was accepted", endpoint)
		}
	}
}

func TestSubscriptionFetcherAcceptsSelfSignedTLS(t *testing.T) {
	config := subscriptionTLSConfig()
	if !config.InsecureSkipVerify {
		t.Fatal("subscription TLS must accept self-signed certificates")
	}
	if config.MinVersion < tls.VersionTLS12 {
		t.Fatalf("minimum TLS version = %#x, want TLS 1.2 or newer", config.MinVersion)
	}
}
