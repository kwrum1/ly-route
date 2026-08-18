package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeaRuntimeIdentityTracksInterfaceRecreation(t *testing.T) {
	root := t.TempDir()
	controller := FilesystemController{RootDir: root}
	interfacePath := filepath.Join(root, "sys", "class", "net", "lylan-ens34")
	if err := os.MkdirAll(interfacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interfacePath, "ifindex"), []byte("17\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts := []RenderedArtifact{NewArtifact(Kea, "/etc/kea/kea-dhcp4.conf", `{"Dhcp4":{"interfaces-config":{"interfaces":["lylan-ens34"]}}}`, "restart")}
	if err := controller.saveApplyRecord(Kea, artifacts, "runtime-a"); err != nil {
		t.Fatal(err)
	}
	if !controller.serviceRuntimeIdentityMatches(Kea, artifacts) {
		t.Fatal("unchanged Kea interface identity did not match")
	}
	if err := os.WriteFile(filepath.Join(interfacePath, "ifindex"), []byte("46\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if controller.serviceRuntimeIdentityMatches(Kea, artifacts) {
		t.Fatal("recreated Kea interface retained a stale runtime identity")
	}
}
