package main

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestVMMMirrorWriteAssetsCheck(t *testing.T) {
	useList(t, resolvedAt("0.2.0"))
	lockPath, _, guestDir := writeTestLockAndGuest(t)
	lock, err := supply.LoadLockFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	inputs := filepath.Join(build, "inputs")
	if err := os.Mkdir(inputs, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(guestDir, "assets", "linux-6.18.63.tar.xz"))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"amd64", "arm64", "source"} {
		name, err := supply.VMMInputName(lock.VMMs[0], target)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inputs, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{"mirror-vmm", "-release", "0.6.0", "-lock", lockPath, "-dir", build}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	dir := helpersDir(t)
	if err := run([]string{"write", "-release", "0.6.0", "-lock", lockPath, "-vmm", build, "-require-vmm", dir}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, supply.FileName)
	m, err := load(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 6 || !strings.HasPrefix(m.Runtime, "runtime-") {
		t.Fatalf("manifest = %+v", m)
	}
	var out bytes.Buffer
	if err := run([]string{"assets", manifest}, &out); err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(out.String())) != 7 || !strings.Contains(out.String(), "cloud-hypervisor-v54.1.tar.xz") {
		t.Fatalf("assets = %s", out.String())
	}
	downloads := upload(t, dir)
	for _, it := range m.Items {
		if it.Kind != supply.KindVMM {
			continue
		}
		for _, u := range []string{it.URL, it.SourceURL} {
			name := path.Base(u)
			b, err := os.ReadFile(filepath.Join(build, "assets", name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(downloads, name), b, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	args := []string{"check", "-url", release, "-dir", downloads, "-lock", lockPath, "-vmm", build, "-require-vmm", manifest}
	if err := run(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(downloads, "cloud-hypervisor-v54.1.tar.xz")); err != nil {
		t.Fatal(err)
	}
	if err := run(args, &bytes.Buffer{}); err == nil {
		t.Fatal("missing uploaded source passed")
	}
	if err := os.Remove(filepath.Join(inputs, "cloud-hypervisor-static-aarch64")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"write", "-release", "0.6.0", "-lock", lockPath, "-vmm", build, "-require-vmm", dir}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing architecture input passed")
	}
}

func TestRequireVMMRefusesEmptyBuild(t *testing.T) {
	if err := run([]string{"write", "-release", "0.6.0", "-require-vmm", helpersDir(t)}, &bytes.Buffer{}); err == nil {
		t.Fatal("required VMMs passed without build folder")
	}
}
