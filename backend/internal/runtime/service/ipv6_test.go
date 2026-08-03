package service

import (
	"errors"
	"strings"
	"testing"
)

func TestRenderIPv6RA_delegated_prefix_assigns_LAN_64(t *testing.T) {
	// Given
	plan := IPv6RAPlan{Interface: "lan0", DelegatedPrefix: "2001:db8:100::/56"}

	// When
	artifacts, err := RenderIPv6RA(plan)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Service != IPv6RA || artifacts[0].Path != "/etc/radvd.conf" {
		t.Fatalf("RA artifacts = %#v", artifacts)
	}
	for _, required := range []string{"interface lan0", "prefix 2001:db8:100::/64", "AdvSendAdvert on"} {
		if !strings.Contains(artifacts[0].Content, required) {
			t.Fatalf("RA content missing %q: %s", required, artifacts[0].Content)
		}
	}
}

func TestRenderIPv6RA_prefix_too_small_prevents_RA(t *testing.T) {
	// Given
	plan := IPv6RAPlan{Interface: "lan0", DelegatedPrefix: "2001:db8:100::/65"}

	// When
	artifacts, err := RenderIPv6RA(plan)

	// Then
	if !errors.Is(err, ErrIPv6PrefixTooSmall) {
		t.Fatalf("RenderIPv6RA error = %v, want ErrIPv6PrefixTooSmall", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("RA artifacts = %#v, want none", artifacts)
	}
}
