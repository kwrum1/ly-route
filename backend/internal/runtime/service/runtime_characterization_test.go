package service

import (
	"context"
	"strings"
	"testing"
)

func TestRuntimeCharacterization_preserves_full_apply_and_rollback_order(t *testing.T) {
	// Given
	controller := &fakeController{}
	runtime := Runtime{Controller: controller}
	artifacts := []RenderedArtifact{
		NewArtifact(IPv6RA, "/etc/radvd.conf", "interface lan0 {};", "reload"),
		NewArtifact(PPPd, "/etc/ppp/peers/ly-route", "peer", "restart"),
		NewArtifact(VPP, "/var/lib/ly-route/vpp/operations.json", "{}", "restart"),
		NewArtifact(LinuxRouting, "/var/lib/ly-route/policy-routing/apply.sh", "#!/bin/sh\n", "restart"),
		NewArtifact(Nftables, "/etc/nftables.conf", "flush ruleset", "reload"),
		NewArtifact(Xray, "/etc/xray/config.json", "{}", "restart"),
		NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", "{}", "restart"),
		NewArtifact(SmartDNS, "/etc/smartdns/conf.d/ly-route-default.conf", "address #", "reload"),
	}

	// When
	if err := runtime.Apply(context.Background(), artifacts); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Rollback(context.Background(), artifacts); err != nil {
		t.Fatal(err)
	}

	// Then
	if got, want := strings.Join(controller.applied, ","), "smartdns,kea,xray,nftables,linux-routing,vpp,pppd,ipv6-ra"; got != want {
		t.Fatalf("apply order = %s, want %s", got, want)
	}
	if got, want := strings.Join(controller.rolledBack, ","), "ipv6-ra,pppd,vpp,linux-routing,nftables,xray,kea,smartdns"; got != want {
		t.Fatalf("rollback order = %s, want %s", got, want)
	}
}
