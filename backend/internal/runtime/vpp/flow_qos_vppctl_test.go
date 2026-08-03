package vpp

import "testing"

func TestNormalizeDynamicACLTagPlacesTagBeforeRules(t *testing.T) {
	input := "set acl-plugin acl permit src 192.0.2.1/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 443 tag ly-route-flow_200"
	want := "set acl-plugin acl tag ly-route-flow_200 permit src 192.0.2.1/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 443"
	if got := normalizeDynamicACLTag(input); got != want {
		t.Fatalf("normalized command = %q, want %q", got, want)
	}
}
