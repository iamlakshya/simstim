//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func firecrackerFixture(t *testing.T) wall.Spec {
	t.Helper()
	spec := vmFixture(t)
	spec.VM.Runtime.Items[0].Name = "firecracker"
	spec.VM.Runtime.Items[0].Version = "1.18.0"
	spec.VM.Runtime.Items[0].HostKernel = "5.10,6.1,6.18"
	return spec
}

func TestFirecrackerUsesSameResourceEngineWithExactBackend(t *testing.T) {
	spec := firecrackerFixture(t)
	base := fixtureDriver(t, spec.SessionDir)
	driver, err := NewForDriver("firecracker", base.options)
	if err != nil || driver.Name() != "firecracker" || driver.Level() != wall.MicroVM {
		t.Fatal("the actual Firecracker backend was not constructed", err)
	}
	if _, err := NewForDriver("other", base.options); err == nil {
		t.Fatal("an unknown backend selected executable code")
	}
	if _, err := base.Prepare(t.Context(), spec); err == nil {
		t.Fatal("the CHV constructor accepted another selected VMM")
	}
	prepared, err := driver.Prepare(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.(*sandbox).ReleasePreparedV1(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(spec.SessionDir, "firecracker.json"))
	if err != nil || strings.Contains(string(data), "runtime") || strings.Contains(string(data), "root_hash") {
		t.Fatal("saved layout acquired execution pins", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(spec.SessionDir, "chv.json")); !os.IsNotExist(err) {
		t.Fatal("Firecracker used the other backend's layout", err)
	}
	if _, err := driver.Open(t.Context(), spec.SessionDir); err == nil {
		t.Fatal("saved layout replaced original start-plan authority")
	}
	opened, err := driver.Open(wall.WithExpectedSpec(t.Context(), spec), spec.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.(*sandbox).ReleasePreparedV1(); err != nil {
		t.Fatal(err)
	}
}

func TestFirecrackerGeneratedConfigurationIsOwnedReadOnlyData(t *testing.T) {
	spec := firecrackerFixture(t)
	config, err := prepareFirecrackerConfig(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer config.Close()
	if err := config.check(); err != nil {
		t.Fatal(err)
	}
	info, err := config.file.Stat()
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatal("generated boot data is writable or missing", err)
	}
	data, err := os.ReadFile(config.path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 5 || string(fields["network-interfaces"]) != "[]" {
		t.Fatal("the closed device configuration changed", string(data), err)
	}
	if strings.Contains(string(data), "--enable-pci") || strings.Contains(string(data), "snapshot") || strings.Contains(string(data), "balloon") {
		t.Fatal("the production configuration enabled an unwanted mechanism")
	}
	if err := os.Chmod(config.path, 0o600); err != nil {
		t.Fatal(err)
	}
	if config.check() == nil {
		t.Fatal("a changed role mode retained its boot-data witness")
	}
}

func TestFirecrackerConfigRejectsReplacementAndCancelledPublication(t *testing.T) {
	spec := firecrackerFixture(t)
	outside := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(outside, []byte("preserve other data"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spec.SessionDir, firecrackerConfigName)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if config, err := prepareFirecrackerConfig(t.Context(), spec); err == nil {
		config.Close()
		t.Fatal("generated data replaced or followed a planted symlink")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "preserve other data" {
		t.Fatal("a refused publication changed another file", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if config, err := prepareFirecrackerConfig(ctx, spec); err == nil {
		config.Close()
		t.Fatal("a cancelled publication succeeded")
	}
	entries, err := os.ReadDir(spec.SessionDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("a cancelled publication left a file", entries, err)
	}
}
