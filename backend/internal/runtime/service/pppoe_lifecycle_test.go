package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFilesystemControllerStopPPPoEStopsEveryInstanceAndVerifiesInterfacesGone(t *testing.T) {
	artifacts, err := RenderPPPoEConfig([]PPPoEPeer{
		{ID: "wan-blue", Interface: "eth7", Username: "blue", Password: "secret"},
		{ID: "wan-green", Interface: "eth8", Username: "green", Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{outputs: map[string]string{
		"vppctl show pppoe session": "No pppoe sessions configured...",
	}}
	controller := FilesystemController{Runner: runner}
	if err := controller.Stop(context.Background(), PPPd, artifacts); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(runner.commands, "\n")
	for _, required := range []string{
		"systemctl stop ly-route-pppoe@ly-route-wan-blue.service",
		"systemctl stop ly-route-pppoe@ly-route-wan-green.service",
		"systemctl stop ly-route-pppoe.target",
		"vppctl show pppoe session",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("PPPoE stop commands missing %q:\n%s", required, commands)
		}
	}
}

func TestFilesystemControllerApplyPPPoEActivatesTargetBeforeInstances(t *testing.T) {
	artifacts, err := RenderPPPoEConfig([]PPPoEPeer{{ID: "wan-blue", Interface: "eth7", Username: "blue", Password: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	controller := FilesystemController{Runner: runner}
	if err := controller.runApplyCommand(context.Background(), PPPd, artifacts); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl start ly-route-pppoe.target",
		"systemctl reload-or-restart ly-route-pppoe@ly-route-wan-blue.service",
	}
	if len(runner.commands) != len(want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
	for index := range want {
		if runner.commands[index] != want[index] {
			t.Fatalf("commands = %#v, want %#v", runner.commands, want)
		}
	}
}

func TestFilesystemControllerStopPPPoEContinuesAcrossInstanceFailures(t *testing.T) {
	artifacts, err := RenderPPPoEConfig([]PPPoEPeer{
		{ID: "wan-blue", Interface: "eth7", Username: "blue", Password: "secret"},
		{ID: "wan-green", Interface: "eth8", Username: "green", Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{runErrs: map[string]error{
		"systemctl stop ly-route-pppoe@ly-route-wan-blue.service": errors.New("unit refused stop"),
	}}
	err = (FilesystemController{Runner: runner}).Stop(context.Background(), PPPd, artifacts)
	if err == nil || !strings.Contains(err.Error(), "wan-blue") {
		t.Fatalf("expected explicit instance stop failure, got %v", err)
	}
	if !strings.Contains(strings.Join(runner.commands, "\n"), "systemctl stop ly-route-pppoe@ly-route-wan-green.service") {
		t.Fatalf("second PPPoE instance was not stopped after first failure: %#v", runner.commands)
	}
}

func TestRuntimeStopFailsClosedWithoutVerifiedStopController(t *testing.T) {
	err := (Runtime{Controller: &fakeController{}}).Stop(context.Background(), PPPd, []RenderedArtifact{NewArtifact(PPPd, "/etc/ly-route/pppoe/ly-route-wan.json", `{}`, "restart")})
	if err == nil || !strings.Contains(err.Error(), "does not support verified stop") {
		t.Fatalf("unsupported stop did not fail closed: %v", err)
	}
}
