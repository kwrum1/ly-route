package httpapi

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"ly-route/backend/internal/product"
)

var ErrUpgradeProductMismatch = errors.New("upgrade package product mismatch")

type UpgradeProductMismatchError struct {
	Package product.ID
	Server  product.ID
}

func (err *UpgradeProductMismatchError) Error() string {
	return fmt.Sprintf("%s: package is %s, server is %s", ErrUpgradeProductMismatch, err.Package, err.Server)
}

func (err *UpgradeProductMismatchError) Is(target error) bool {
	return target == ErrUpgradeProductMismatch
}

type upgradePackageManifest struct {
	PackageType string            `json:"package_type"`
	Product     product.ID        `json:"product"`
	Suite       string            `json:"suite,omitempty"`
	Arch        string            `json:"arch,omitempty"`
	Commit      string            `json:"commit,omitempty"`
	CreatedAt   string            `json:"created_at,omitempty"`
	InstallRoot string            `json:"install_root,omitempty"`
	Services    []string          `json:"services,omitempty"`
	Checksums   map[string]string `json:"checksums"`
}

func validateUpgradePackage(packagePath string, expectedProduct product.ID) error {
	tmp, err := os.MkdirTemp(filepath.Dir(packagePath), "validate-*")
	if err != nil {
		return fmt.Errorf("create validation directory: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := extractUpgradePackage(packagePath, tmp); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(tmp, "manifest.json"))
	if err != nil {
		return fmt.Errorf("manifest.json is required: %w", err)
	}
	manifest, err := parseUpgradeManifest(body)
	if err != nil {
		return err
	}
	if manifest.Product != expectedProduct {
		return &UpgradeProductMismatchError{Package: manifest.Product, Server: expectedProduct}
	}
	return verifyUpgradeChecksums(tmp, manifest)
}

func extractUpgradePackage(packagePath, root string) (err error) {
	cmd := exec.Command("zstd", "-dc", packagePath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open upgrade decompressor: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start upgrade decompressor: %w", err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			err = errors.Join(err, cmd.Wait())
		}
	}()

	reader := tar.NewReader(stdout)
	const maxEntries = 10_000
	const maxExtractedBytes int64 = 2 << 30
	var entries int
	var extractedBytes int64
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read upgrade archive: %w", nextErr)
		}
		entries++
		if entries > maxEntries || header.Size < 0 || header.Size > maxExtractedBytes-extractedBytes {
			return fmt.Errorf("upgrade archive exceeds extraction limits")
		}
		extractedBytes += header.Size
		name, pathErr := safeArchiveEntryPath(header.Name)
		if pathErr != nil {
			return pathErr
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create upgrade directory: %w", err)
			}
		case tar.TypeReg, 0:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create upgrade parent: %w", err)
			}
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return fmt.Errorf("create upgrade file: %w", err)
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return fmt.Errorf("write upgrade file: %w", err)
			}
		default:
			return fmt.Errorf("unsafe archive entry %q: unsupported type %d", header.Name, header.Typeflag)
		}
	}
	if err := cmd.Wait(); err != nil {
		waited = true
		return fmt.Errorf("decompress upgrade package: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	waited = true
	return nil
}

func safeArchiveEntryPath(raw string) (string, error) {
	cleaned := path.Clean(raw)
	if raw == "" || path.IsAbs(raw) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe archive entry %q: path escapes package", raw)
	}
	if cleaned == "." {
		return cleaned, nil
	}
	return cleaned, nil
}

func parseUpgradeManifest(body []byte) (upgradePackageManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var manifest upgradePackageManifest
	if err := decoder.Decode(&manifest); err != nil {
		return upgradePackageManifest{}, fmt.Errorf("manifest.json must be valid strict JSON: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return upgradePackageManifest{}, fmt.Errorf("manifest.json must contain one JSON value")
	}
	if manifest.PackageType != "ly-route-upgrade" {
		return upgradePackageManifest{}, fmt.Errorf("manifest package_type must be ly-route-upgrade")
	}
	if manifest.Product.String() == "" {
		return upgradePackageManifest{}, fmt.Errorf("manifest product is required")
	}
	if len(manifest.Checksums) == 0 {
		return upgradePackageManifest{}, fmt.Errorf("manifest checksums are required")
	}
	return manifest, nil
}

func verifyUpgradeChecksums(root string, manifest upgradePackageManifest) error {
	required := []string{"usr/lib/ly-route/ly-route-control", "opt/ly-route/admin/app.js", "etc/nginx/conf.d/ly-route-admin.conf"}
	for _, path := range required {
		if _, ok := manifest.Checksums[path]; !ok {
			return fmt.Errorf("manifest checksum missing for %s", path)
		}
	}
	for path, expected := range manifest.Checksums {
		if strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.TrimSpace(expected) == "" {
			return fmt.Errorf("unsafe manifest checksum entry")
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return fmt.Errorf("manifest file missing: %s: %w", path, err)
		}
		actual := sha256.Sum256(body)
		if hex.EncodeToString(actual[:]) != strings.ToLower(expected) {
			return fmt.Errorf("manifest checksum mismatch: %s", path)
		}
	}
	return nil
}
