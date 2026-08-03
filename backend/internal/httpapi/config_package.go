package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ly-route/backend/internal/product"
)

const (
	ConfigPackageSchemaVersion = 1
	configContentType          = "local_desired_config"
)

var (
	ErrConfigProductMismatch = errors.New("config package product mismatch")
	ErrInvalidConfigPackage  = errors.New("invalid config package")
)

type ConfigPackagePayload struct {
	SchemaVersion int                          `json:"schema_version"`
	ContentType   string                       `json:"content_type"`
	Product       product.ID                   `json:"product"`
	DeviceMode    string                       `json:"device_mode"`
	Resources     map[string][]json.RawMessage `json:"resources"`
	Excluded      []string                     `json:"excluded_domains"`
}

type ConfigPackageManifest struct {
	SchemaVersion int        `json:"schema_version"`
	ContentType   string     `json:"content_type"`
	Product       product.ID `json:"product"`
	Included      []string   `json:"included_domains"`
	Excluded      []string   `json:"excluded_domains"`
	SecretPolicy  string     `json:"secret_policy"`
	PackageHash   string     `json:"package_hash"`
}

type ConfigImportRequest struct {
	Confirm                 bool                  `json:"confirm"`
	DryRun                  bool                  `json:"dry_run"`
	ConfirmationToken       string                `json:"confirmation_token,omitempty"`
	ConfirmationActor       string                `json:"confirmation_actor,omitempty"`
	ConfirmationExpiresAt   string                `json:"confirmation_expires_at,omitempty"`
	ConfirmationPackageHash string                `json:"package_hash,omitempty"`
	ConfirmationDiffHash    string                `json:"diff_hash,omitempty"`
	PackageManifest         ConfigPackageManifest `json:"package_manifest"`
	Payload                 ConfigPackagePayload  `json:"payload"`
}

type ConfigProductMismatchError struct {
	Package product.ID
	Server  product.ID
}

func (err *ConfigProductMismatchError) Error() string {
	return fmt.Sprintf("%s: package is %s, server is %s", ErrConfigProductMismatch, err.Package, err.Server)
}

func (err *ConfigProductMismatchError) Is(target error) bool {
	return target == ErrConfigProductMismatch
}

func (payload ConfigPackagePayload) ValidateFor(profile product.Profile) error {
	if payload.SchemaVersion != ConfigPackageSchemaVersion || payload.ContentType != configContentType || payload.Product.String() == "" {
		return fmt.Errorf("%w: schema, content type, and product are required", ErrInvalidConfigPackage)
	}
	if payload.Product != profile.ID() {
		return &ConfigProductMismatchError{Package: payload.Product, Server: profile.ID()}
	}
	if strings.TrimSpace(payload.DeviceMode) != profile.ID().String() {
		return fmt.Errorf("%w: device_mode must be %s", ErrInvalidConfigPackage, profile.ID())
	}
	for resourceType := range payload.Resources {
		if !resourceAllowed(profile, resourceType) {
			return fmt.Errorf("%w: unsupported resource type %q for %s", ErrInvalidConfigPackage, resourceType, profile.ID())
		}
	}
	return nil
}

func resourceAllowed(profile product.Profile, resourceType string) bool {
	if resourceType != "proxy_egress" {
		if _, ok := desiredResourceDefs[resourceType]; !ok {
			return false
		}
	}
	return profile.AllowsConfigResource(resourceType)
}

func hashConfigPayload(payload ConfigPackagePayload) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func manifestForPayload(payload ConfigPackagePayload) ConfigPackageManifest {
	return ConfigPackageManifest{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: payload.Product, Included: []string{"desired_config"}, Excluded: payload.Excluded, SecretPolicy: "excluded", PackageHash: hashConfigPayload(payload)}
}

func (server *Server) preflightConfigPackage(payload ConfigPackagePayload, manifest ConfigPackageManifest) error {
	if err := payload.ValidateFor(server.profile); err != nil {
		return err
	}
	if manifest.SchemaVersion != ConfigPackageSchemaVersion || manifest.ContentType != configContentType || manifest.Product.String() == "" {
		return fmt.Errorf("%w: manifest schema, content type, and product are required", ErrInvalidConfigPackage)
	}
	if manifest.Product != server.profile.ID() {
		return &ConfigProductMismatchError{Package: manifest.Product, Server: server.profile.ID()}
	}
	if manifest.Product != payload.Product {
		return fmt.Errorf("%w: manifest and payload products differ", ErrInvalidConfigPackage)
	}
	if manifest.PackageHash != hashConfigPayload(payload) {
		return fmt.Errorf("%w: package_hash does not match payload", ErrInvalidConfigPackage)
	}
	if manifest.SecretPolicy != "excluded" {
		return fmt.Errorf("%w: secret_policy must be excluded", ErrInvalidConfigPackage)
	}
	return nil
}

func configPayloadContainsSecret(payload ConfigPackagePayload) bool {
	for _, items := range payload.Resources {
		for _, item := range items {
			var value any
			if json.Unmarshal(item, &value) == nil && containsSecret(value) {
				return true
			}
		}
	}
	return false
}

func encodeResourceItems[T any](items []T) ([]json.RawMessage, error) {
	encoded := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("encode config resource: %w", err)
		}
		encoded = append(encoded, raw)
	}
	return encoded, nil
}
