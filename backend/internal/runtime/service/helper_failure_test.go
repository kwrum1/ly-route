package service

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestFilesystemController_VPP_apply_fails_when_helper_is_missing(t *testing.T) {
	// Given
	const helper = "/usr/lib/ly-route/vpp-apply-default"
	runner := &fakeRunner{runErrs: map[string]error{helper: exec.ErrNotFound}}
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}
	artifacts := []RenderedArtifact{NewArtifact(VPP, "/var/lib/ly-route/vpp/operations.json", `{"operations":[{"Name":"vpp.interface.address","Resource":"wan0"}]}`, "restart")}

	// When
	err := controller.ReloadOrRestart(context.Background(), VPP, artifacts)

	// Then
	if err == nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("VPP apply error = %v, want missing helper failure", err)
	}
}

func TestFilesystemController_policy_routing_apply_fails_when_helper_is_missing(t *testing.T) {
	// Given
	const helper = "/usr/lib/ly-route/policy-routing-apply-default"
	runner := &fakeRunner{runErrs: map[string]error{helper: exec.ErrNotFound}}
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}
	content := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"ip rule add fwmark 0x7/0xffffffff table 1701 priority 1702",
		"ip route replace default dev lo scope link table 1701",
		"",
	}, "\n")
	artifacts := []RenderedArtifact{NewArtifact(LinuxRouting, "/var/lib/ly-route/policy-routing/apply.sh", content, "restart")}

	// When
	err := controller.ReloadOrRestart(context.Background(), LinuxRouting, artifacts)

	// Then
	if err == nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("policy routing apply error = %v, want missing helper failure", err)
	}
}
