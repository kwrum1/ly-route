package httpapi

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestCanonicalIPv4AddressesIsStableAndDeduplicated(t *testing.T) {
	addresses := []netip.Addr{
		netip.MustParseAddr("104.16.249.249"),
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("104.16.248.249"),
		netip.MustParseAddr("104.16.249.249"),
	}
	if got, want := canonicalIPv4Addresses(addresses), []string{"104.16.248.249", "104.16.249.249"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical IPv4 addresses = %#v, want %#v", got, want)
	}
}
