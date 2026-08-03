package service

import (
	"context"
	"strings"
	"testing"
)

func TestFilesystemControllerReadsXrayBalancerSelection(t *testing.T) {
	tag := "subscription-main-fastest"
	runner := &fakeRunner{outputs: map[string]string{
		"xray api bi --server=127.0.0.1:10085 " + tag: "  - Selecting Override:\n    1\n  - Selects:\n    1   subscription-main-node-b\n",
	}}
	states, err := (FilesystemController{Runner: runner}).XrayBalancerStates(context.Background(), []string{tag, tag})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Tag != tag || len(states[0].SelectedOutboundTags) != 1 || states[0].SelectedOutboundTags[0] != "subscription-main-node-b" {
		t.Fatalf("unexpected Xray balancer state: %#v", states)
	}
}

func TestFilesystemControllerRejectsXrayBalancerWithoutHealthySelection(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{"xray api bi --server=127.0.0.1:10085 subscription-main-fastest": "  - Selects:\n"}}
	_, err := (FilesystemController{Runner: runner}).XrayBalancerStates(context.Background(), []string{"subscription-main-fastest"})
	if err == nil || !strings.Contains(err.Error(), "no healthy selected outbound") {
		t.Fatalf("missing selection did not fail closed: %v", err)
	}
}

func TestFilesystemControllerRejectsNonLoopbackXrayAPI(t *testing.T) {
	_, err := (FilesystemController{Runner: &fakeRunner{}, XrayAPIAddress: "192.0.2.10:10085"}).XrayBalancerStates(context.Background(), []string{"main"})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback Xray API was accepted: %v", err)
	}
}
