package orchestratorapi

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestTopologyFixture_source_SHA_is_current(t *testing.T) {
	// Given
	fixture, err := os.ReadFile("testdata/topology-v1.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	recorded, err := os.ReadFile("testdata/topology-v1.sha256")
	if err != nil {
		t.Fatalf("read fixture SHA: %v", err)
	}

	// When
	digest := sha256.Sum256(fixture)
	actual := hex.EncodeToString(digest[:])
	want := strings.Fields(string(recorded))

	// Then
	if len(want) != 2 || want[0] != actual || want[1] != "topology-v1.json" {
		t.Fatalf("fixture source SHA = %q, actual %q", strings.TrimSpace(string(recorded)), actual)
	}
}
