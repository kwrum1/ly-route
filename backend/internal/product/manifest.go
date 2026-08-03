package product

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

var ErrInvalidManifest = errors.New("invalid product manifest")

const ManifestSchemaVersion = 1

type Manifest struct {
	profile Profile
}

type manifestWire struct {
	SchemaVersion int          `json:"schema_version"`
	Product       string       `json:"product"`
	Services      []Service    `json:"services"`
	Resources     []Capability `json:"resources"`
}

func ParseManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire manifestWire
	if err := decoder.Decode(&wire); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode: %w", ErrInvalidManifest, err)
	}
	if err := requireManifestEOF(decoder); err != nil {
		return Manifest{}, err
	}
	if wire.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, fmt.Errorf("%w: schema_version must be %d", ErrInvalidManifest, ManifestSchemaVersion)
	}
	profile, err := ParseProfile(wire.Product)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: product: %w", ErrInvalidManifest, err)
	}
	if !slices.Equal(wire.Services, profile.Services()) {
		return Manifest{}, fmt.Errorf("%w: services do not match %s profile", ErrInvalidManifest, profile.ID())
	}
	if !slices.Equal(wire.Resources, profile.Capabilities()) {
		return Manifest{}, fmt.Errorf("%w: resources do not match %s profile", ErrInvalidManifest, profile.ID())
	}
	return Manifest{profile: profile}, nil
}

func requireManifestEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: trailing data: %w", ErrInvalidManifest, err)
	}
	return fmt.Errorf("%w: multiple JSON values", ErrInvalidManifest)
}

func (manifest Manifest) Product() ID {
	return manifest.profile.ID()
}

func (manifest Manifest) Profile() Profile {
	return manifest.profile
}

func (manifest Manifest) Services() []Service {
	return manifest.profile.Services()
}

func (manifest Manifest) Resources() []Capability {
	return manifest.profile.Capabilities()
}
