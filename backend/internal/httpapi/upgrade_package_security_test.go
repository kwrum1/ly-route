package httpapi

import (
	"archive/tar"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ly-route/backend/internal/product"
)

func TestProductUpgradeRejectsUnsafeArchiveEntries(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd is required to build test upgrade package")
	}
	tests := []struct {
		name   string
		header tar.Header
	}{
		{name: "parent traversal", header: tar.Header{Name: "../outside", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}},
		{name: "symbolic link", header: tar.Header{Name: "inside-link", Linkname: "/etc/passwd", Mode: 0o777, Typeflag: tar.TypeSymlink}},
		{name: "hard link", header: tar.Header{Name: "inside-hardlink", Linkname: "/etc/passwd", Mode: 0o777, Typeflag: tar.TypeLink}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			packagePath := unsafeUpgradePackage(t, test.header)

			// When
			err := validateUpgradePackage(packagePath, product.Gateway().ID())

			// Then
			if err == nil || !strings.Contains(err.Error(), "unsafe archive entry") {
				t.Fatalf("validate unsafe package error = %v, want unsafe archive entry", err)
			}
		})
	}
}

func TestProductFirmwareTargetRejectsShellMetacharacters(t *testing.T) {
	for _, path := range []string{
		"/opt/ly-route-upgrades/$(touch-pwned)",
		"/opt/ly-route-upgrades/`touch-pwned`",
		"/opt/ly-route-upgrades/name'quoted",
		"/opt/ly-route-upgrades/name\"quoted",
	} {
		if safeFirmwareTargetDir(path) {
			t.Errorf("safeFirmwareTargetDir(%q) = true, want false", path)
		}
	}
}

func TestProductUpgradeInvocationDoesNotInterpolatePackagePathIntoShell(t *testing.T) {
	// Given
	packagePath := `/var/lib/ly-route/firmware-update/$(touch injected).tar.zst`

	// When
	invocation := firmwareUpgradeInstallInvocation(packagePath, strings.Repeat("a", 64), "/usr/lib/ly-route", true)

	// Then
	if invocation.Name != "bash" || len(invocation.Args) < 7 || invocation.Args[0] != "-c" {
		t.Fatalf("installer invocation = %#v, want bash -c with positional arguments", invocation)
	}
	if strings.Contains(invocation.Args[1], packagePath) {
		t.Fatalf("installer script interpolates untrusted package path: %s", invocation.Args[1])
	}
	if invocation.Args[3] != packagePath {
		t.Fatalf("package argument = %q, want exact positional value %q", invocation.Args[3], packagePath)
	}
}

func unsafeUpgradePackage(t *testing.T, header tar.Header) string {
	t.Helper()
	tarPath := filepath.Join(t.TempDir(), "unsafe.tar")
	tarFile, err := os.Create(tarPath)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	writer := tar.NewWriter(tarFile)
	if err := writer.WriteHeader(&header); err != nil {
		tarFile.Close()
		t.Fatalf("write tar header: %v", err)
	}
	if header.Size > 0 {
		if _, err := writer.Write([]byte("x")); err != nil {
			tarFile.Close()
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := errors.Join(writer.Close(), tarFile.Close()); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	packagePath := tarPath + ".zst"
	if output, err := exec.Command("zstd", "-q", "-f", tarPath, "-o", packagePath).CombinedOutput(); err != nil {
		t.Fatalf("compress tar: %v: %s", err, output)
	}
	return packagePath
}
