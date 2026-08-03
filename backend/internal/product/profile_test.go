package product

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestProfileAcceptsOnlyKnownProductIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "gateway", raw: "gateway", want: "gateway"},
		{name: "orchestrator", raw: "orchestrator", want: "orchestrator"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// When
			profile, err := ParseProfile(test.raw)

			// Then
			if err != nil {
				t.Fatalf("ParseProfile(%q): %v", test.raw, err)
			}
			if profile.ID().String() != test.want {
				t.Fatalf("profile ID = %q, want %q", profile.ID(), test.want)
			}
		})
	}
}

func TestProfileRejectsUnknownAndEmptyProductIDs(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", " ", "Gateway", "bridge", "gateway\n"} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			// When
			_, err := ParseProfile(raw)

			// Then
			if !errors.Is(err, ErrInvalidProductID) {
				t.Fatalf("ParseProfile(%q) error = %v, want ErrInvalidProductID", raw, err)
			}
		})
	}
}

func TestProfileGatewayAllowlistIsComplete(t *testing.T) {
	t.Parallel()

	// Given
	want := []Capability{
		CapabilityProduct,
		CapabilityHealth,
		CapabilityAuth,
		CapabilityManagement,
		CapabilityInterfaces,
		CapabilityGatewayMode,
		CapabilityObjectGroups,
		CapabilityGatewayWAN,
		CapabilityGatewayPPPoE,
		CapabilityGatewayRouting,
		CapabilityGatewayNAT,
		CapabilityProxy,
		CapabilityDNS,
		CapabilityDHCP,
		CapabilitySecurity,
		CapabilityTrafficControl,
		CapabilityRuntime,
		CapabilityConfig,
		CapabilityFirmware,
		CapabilityDashboard,
		CapabilityTelemetry,
		CapabilityTopDomains,
	}

	// When
	profile := Gateway()

	// Then
	if !slices.Equal(profile.Capabilities(), want) {
		t.Fatalf("Gateway capabilities = %#v, want %#v", profile.Capabilities(), want)
	}
}

func TestProfileOrchestratorAllowlistExcludesGatewayResources(t *testing.T) {
	t.Parallel()

	// Given
	profile := Orchestrator()
	forbidden := []Capability{
		CapabilityGatewayMode,
		CapabilityGatewayWAN,
		CapabilityGatewayPPPoE,
		CapabilityGatewayRouting,
		CapabilityGatewayNAT,
		CapabilityProxy,
		CapabilityDNS,
		CapabilityDHCP,
		CapabilityFirmware,
		CapabilityTopDomains,
	}
	if !profile.Allows(CapabilityObjectGroups) {
		t.Fatal("Orchestrator must allow IP object groups")
	}

	// When / Then
	for _, capability := range forbidden {
		if profile.Allows(capability) {
			t.Fatalf("Orchestrator unexpectedly allows %q", capability)
		}
	}
}

func TestProfileCapabilityCopiesCannotMutateProfile(t *testing.T) {
	t.Parallel()

	// Given
	profile := Gateway()
	capabilities := profile.Capabilities()

	// When
	capabilities[0] = CapabilityDNS

	// Then
	if profile.Capabilities()[0] != CapabilityProduct {
		t.Fatalf("profile capability changed through returned slice: %#v", profile.Capabilities())
	}
}

func TestImmutableSelectionRejectsProfileChangeAfterInitialization(t *testing.T) {
	t.Parallel()

	// Given
	selection := NewSelection()
	if err := selection.Initialize(Gateway()); err != nil {
		t.Fatalf("initialize Gateway: %v", err)
	}

	// When
	err := selection.Initialize(Orchestrator())

	// Then
	if !errors.Is(err, ErrSelectionImmutable) {
		t.Fatalf("second initialize error = %v, want ErrSelectionImmutable", err)
	}
	active, activeErr := selection.Profile()
	if activeErr != nil {
		t.Fatalf("read active profile: %v", activeErr)
	}
	if active.ID().String() != "gateway" {
		t.Fatalf("active profile = %q, want gateway", active.ID())
	}
}

func TestProfileManifestsMatchCanonicalAllowlists(t *testing.T) {
	t.Parallel()

	for _, profile := range []Profile{Gateway(), Orchestrator()} {
		profile := profile
		t.Run(profile.ID().String(), func(t *testing.T) {
			t.Parallel()

			// Given
			path := filepath.Join("..", "..", "..", "packaging", "product-profiles", profile.ID().String()+".json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			// When
			manifest, err := ParseManifest(data)

			// Then
			if err != nil {
				t.Fatalf("ParseManifest(%s): %v", path, err)
			}
			if manifest.Product() != profile.ID() {
				t.Fatalf("manifest product = %q, want %q", manifest.Product(), profile.ID())
			}
			if !slices.Equal(manifest.Services(), profile.Services()) {
				t.Fatalf("manifest services = %#v, want %#v", manifest.Services(), profile.Services())
			}
			if !slices.Equal(manifest.Resources(), profile.Capabilities()) {
				t.Fatalf("manifest resources = %#v, want %#v", manifest.Resources(), profile.Capabilities())
			}
		})
	}
}

func TestProfileManifestRejectsMalformedOrDriftedInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "malformed JSON", data: `{"product":`},
		{name: "unknown field", data: `{"schema_version":1,"product":"gateway","services":[],"resources":[],"secret":"value"}`},
		{name: "unknown product", data: `{"schema_version":1,"product":"bridge","services":[],"resources":[]}`},
		{name: "missing allowlists", data: `{"schema_version":1,"product":"gateway","services":[],"resources":[]}`},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// When
			_, err := ParseManifest([]byte(test.data))

			// Then
			if err == nil {
				t.Fatal("ParseManifest succeeded, want rejection")
			}
		})
	}
}
