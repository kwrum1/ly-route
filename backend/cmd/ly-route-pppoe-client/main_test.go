package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteStatusPreservesOtherPeerStatus(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "wan-primary.json")
	second := filepath.Join(directory, "wan-secondary.json")
	if err := os.WriteFile(second, []byte(`{"state":"connected","interface":"pppoe-secondary"}`), 0600); err != nil {
		t.Fatalf("write secondary status: %v", err)
	}

	writeStatus(first, map[string]any{"state": "connected", "interface": "pppoe-primary"})

	primary, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read primary status: %v", err)
	}
	if !strings.Contains(string(primary), `"pppoe-primary"`) {
		t.Fatalf("primary status = %q", primary)
	}
	secondary, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read secondary status: %v", err)
	}
	if !strings.Contains(string(secondary), `"pppoe-secondary"`) {
		t.Fatalf("secondary status was changed: %q", secondary)
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".wan-primary.json.tmp-*"))
	if err != nil {
		t.Fatalf("find temporary status files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary status files remain: %v", matches)
	}
}
