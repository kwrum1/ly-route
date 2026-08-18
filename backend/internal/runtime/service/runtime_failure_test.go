package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/apply"
)

func TestFilesystemController_missing_daemon_runner_restores_prior_artifact(t *testing.T) {
	// Given
	root := t.TempDir()
	path := filepath.Join(root, "etc/kea/kea-dhcp4.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("prior"), 0o640); err != nil {
		t.Fatal(err)
	}
	controller := FilesystemController{RootDir: root}

	// When
	err := controller.ReloadOrRestart(context.Background(), Kea, []RenderedArtifact{
		NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", "desired", "restart"),
	})

	// Then
	if err == nil {
		t.Fatal("ReloadOrRestart succeeded without a daemon runner")
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got, want := string(content), "prior"; got != want {
		t.Fatalf("artifact after failed apply = %q, want %q", got, want)
	}
}

func TestFilesystemController_failed_apply_restores_prior_receipt_and_clears_rollback_metadata(t *testing.T) {
	root := t.TempDir()
	artifactPath := filepath.Join(root, "etc/kea/kea-dhcp4.conf")
	receiptPath := filepath.Join(root, "var/lib/ly-route/service-runtime/receipt-kea.json")
	for _, path := range []string{artifactPath, receiptPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(artifactPath, []byte("prior-config"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{runErrs: map[string]error{
		"systemctl start kea-dhcp4-server.service": errors.New("kea start failed"),
	}, runErrCounts: map[string]int{"systemctl start kea-dhcp4-server.service": 1}}
	controller := FilesystemController{RootDir: root, Runner: runner}
	if err := controller.saveApplyRecord(Kea, []RenderedArtifact{
		NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", "prior-config", "restart"),
	}, "prior-transaction"); err != nil {
		t.Fatal(err)
	}
	priorReceipt, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}

	err = controller.ReloadOrRestart(context.Background(), Kea, []RenderedArtifact{
		NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", "desired-config", "restart"),
	})

	if err == nil || !strings.Contains(err.Error(), "kea start failed") {
		t.Fatalf("apply error = %v", err)
	}
	for path, want := range map[string]string{
		artifactPath: "prior-config",
		receiptPath:  string(priorReceipt),
	} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got := string(content); got != want {
			t.Fatalf("%s after failed apply = %q, want %q", path, got, want)
		}
	}
	rollbackPath := filepath.Join(root, "var/lib/ly-route/service-runtime/rollback-kea.json")
	if _, statErr := os.Stat(rollbackPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed transaction left rollback metadata: %v", statErr)
	}
}

func TestFilesystemController_failed_apply_discards_stale_prior_receipt(t *testing.T) {
	root := t.TempDir()
	artifactPath := filepath.Join(root, "etc/kea/kea-dhcp4.conf")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("prior-config"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{runErrs: map[string]error{
		"systemctl start kea-dhcp4-server.service": errors.New("kea start failed"),
	}}
	controller := FilesystemController{RootDir: root, Runner: runner}
	if err := controller.saveApplyRecord(Kea, []RenderedArtifact{
		NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", "different-config", "restart"),
	}, "stale-transaction"); err != nil {
		t.Fatal(err)
	}

	err := controller.ReloadOrRestart(context.Background(), Kea, []RenderedArtifact{
		NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", "desired-config", "restart"),
	})

	if err == nil || !strings.Contains(err.Error(), "kea start failed") {
		t.Fatalf("apply error = %v", err)
	}
	content, readErr := os.ReadFile(artifactPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(content); got != "prior-config" {
		t.Fatalf("artifact after failed apply = %q", got)
	}
	if _, statErr := os.Stat(controller.applyRecordPath(Kea)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale receipt survived failed apply: %v", statErr)
	}
}

func TestFilesystemController_receipt_and_readback_bind_live_artifact_hashes(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	runner := &fakeRunner{
		health:  map[ServiceName]Health{Kea: {Service: Kea, Available: true}},
		outputs: map[string]string{"kea-dhcp4 -t /etc/kea/kea-dhcp4.conf": "configuration test passed"},
	}
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner, Now: func() time.Time { return now }}
	artifacts := []RenderedArtifact{NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", "desired", "restart")}
	request := EvidenceRequest{TransactionID: "txn-kea", Capability: "dhcp", Artifacts: artifacts}
	if err := controller.ReloadOrRestart(context.Background(), Kea, artifacts); err != nil {
		t.Fatal(err)
	}

	// When
	receipt, receiptErr := controller.Receipt(context.Background(), request)
	readback, readbackErr := controller.Readback(context.Background(), request)

	// Then
	if receiptErr != nil || readbackErr != nil {
		t.Fatalf("evidence errors: receipt=%v readback=%v", receiptErr, readbackErr)
	}
	if receipt.TransactionID != request.TransactionID || receipt.Capability != request.Capability || receipt.Status != apply.ReceiptApplied || receipt.AppliedAt != now {
		t.Fatalf("receipt = %#v", receipt)
	}
	if readback.TransactionID != request.TransactionID || readback.Capability != request.Capability || !readback.Fresh || readback.Timestamp != now {
		t.Fatalf("readback = %#v", readback)
	}

	path := filepath.Join(controller.RootDir, "etc/kea/kea-dhcp4.conf")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Fatalf("Kea artifact mode = %o, want %o for the unprivileged daemon", got, want)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Readback(context.Background(), request); err == nil {
		t.Fatal("readback accepted an artifact whose content no longer matches the apply receipt")
	}
}

func TestFilesystemController_persist_only_VPP_plan_is_rollback_safe(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "var/lib/ly-route/vpp/operations.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("prior-plan"), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	controller := FilesystemController{RootDir: root, Runner: runner}
	artifact := NewArtifact(VPP, "/var/lib/ly-route/vpp/operations.json", "committed-plan", ReloadModePersistOnly)

	if err := controller.ReloadOrRestart(context.Background(), VPP, []RenderedArtifact{artifact}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "committed-plan"; got != want {
		t.Fatalf("persisted VPP plan = %q, want %q", got, want)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("persist-only VPP plan executed commands: %#v", runner.commands)
	}
	request := EvidenceRequest{TransactionID: "txn-persist-vpp", Capability: "vpp", Artifacts: []RenderedArtifact{artifact}}
	if _, err := controller.Receipt(context.Background(), request); err != nil {
		t.Fatalf("persist-only VPP receipt: %v", err)
	}
	if _, err := controller.Readback(context.Background(), request); err != nil {
		t.Fatalf("persist-only VPP readback used the legacy live helper receipt: %v", err)
	}

	if err := controller.Rollback(context.Background(), VPP, []RenderedArtifact{artifact}); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "prior-plan"; got != want {
		t.Fatalf("rolled back VPP plan = %q, want %q", got, want)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("persist-only VPP rollback executed commands: %#v", runner.commands)
	}
}

func TestRuntime_daemon_failure_degrades_only_affected_capability(t *testing.T) {
	// Given
	keaFailure := errors.New("kea daemon rejected configuration")
	controller := &fakeController{applyErrs: map[ServiceName]error{Kea: keaFailure}}
	runtime := Runtime{Controller: controller}
	artifacts := []RenderedArtifact{
		NewArtifact(SmartDNS, "/etc/smartdns/conf.d/ly-route-default.conf", "address #", "reload"),
		NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", "{}", "restart"),
		NewArtifact(Xray, "/etc/xray/config.json", "{}", "restart"),
	}

	// When
	report := runtime.ApplyCapabilities(context.Background(), artifacts)

	// Then
	if !errors.Is(report.Err(), keaFailure) {
		t.Fatalf("apply error = %v, want Kea failure", report.Err())
	}
	if got, want := strings.Join(controller.applied, ","), "smartdns,kea,xray"; got != want {
		t.Fatalf("attempted services = %s, want %s", got, want)
	}
	if got, want := strings.Join(artifactServiceNames(report.AppliedArtifacts), ","), "smartdns,xray"; got != want {
		t.Fatalf("applied capabilities = %s, want %s", got, want)
	}
	if len(report.Failures) != 1 || report.Failures[0].Service != Kea {
		t.Fatalf("capability failures = %#v, want only Kea", report.Failures)
	}
}

func TestFilesystemController_failed_PPPoE_restores_prior_peer(t *testing.T) {
	// Given
	root := t.TempDir()
	path := filepath.Join(root, "etc/ly-route/pppoe/ly-route-wan.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("prior-peer"), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{runErrs: map[string]error{"systemctl restart ly-route-pppoe@ly-route-wan.service": errors.New("PPPoE authentication failed")}}
	controller := FilesystemController{RootDir: root, Runner: runner}

	// When
	err := controller.ReloadOrRestart(context.Background(), PPPd, []RenderedArtifact{NewArtifact(PPPd, "/etc/ly-route/pppoe/ly-route-wan.json", `{"id":"wan","status_file":"/run/ly-route/pppoe/wan.json"}`, "restart")})

	// Then
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("PPPoE apply error = %v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got, want := string(content), "prior-peer"; got != want {
		t.Fatalf("PPPoE peer after failure = %q, want %q", got, want)
	}
}

func artifactServiceNames(artifacts []RenderedArtifact) []string {
	names := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		names = append(names, string(artifact.Service))
	}
	return names
}

func TestSystemctlRunner_Kea_readback_uses_test_and_config_flags(t *testing.T) {
	// When
	commands, err := serviceReadbackCommands(Kea)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || strings.Join(commands[0], " ") != "kea-dhcp4 -t /etc/kea/kea-dhcp4.conf" {
		t.Fatalf("Kea readback command = %#v", commands)
	}
}

func TestIPv6RA_uses_radvd_service_unit(t *testing.T) {
	// When
	unit := applyUnit(IPv6RA)

	// Then
	if unit != "radvd.service" {
		t.Fatalf("IPv6 RA unit = %q, want radvd.service", unit)
	}
}
