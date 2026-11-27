package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
)

// release is the project's own release of 0.6.0.
var release = supply.ReleaseFolder("0.6.0")

// helpersDir is a helpers folder with the files a release build has.
func helpersDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"host-linux-amd64": "HOST-AMD64", "host-linux-arm64": "HOST-ARM64",
		"vt-linux-amd64": "VT-AMD64", "vt-linux-arm64": "VT-ARM64",
		"userns-linux-amd64": "USERNS-AMD64", "userns-linux-arm64": "USERNS-ARM64",
		"README.md": "not a helper",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// upload copies the helpers into a folder under their names on the
// release, as the release job downloads them.
func upload(t *testing.T, dir string) string {
	t.Helper()
	dl := t.TempDir()
	for _, c := range supply.CarriedFiles {
		for _, arch := range supply.Arches {
			b, err := os.ReadFile(filepath.Join(dir, c.File(arch)))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dl, c.AssetName(arch)), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dl
}

// useList makes write put list in the manifests that the test writes.
func useList(t *testing.T, list []supply.Advisory) {
	t.Helper()
	old := knownBadList
	knownBadList = func() []supply.Advisory { return list }
	t.Cleanup(func() { knownBadList = old })
}

// resolvedAt is the shipped list with each unresolved entry resolved at a
// version, as the release that carries the fixed Ghostty pin does.
func resolvedAt(before string) []supply.Advisory {
	list := supply.KnownBad()
	for i := range list {
		if list[i].Unresolved {
			list[i].Unresolved, list[i].Before = false, before
		}
	}
	return list
}

func TestWriteAssetsCheck(t *testing.T) {
	useList(t, resolvedAt("0.2.0"))
	dir := helpersDir(t)
	var out bytes.Buffer
	if err := run([]string{"write", "-release", "v0.6.0", dir}, &out); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, supply.FileName)
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	m, err := supply.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Release != "0.6.0" || len(m.Items) != 6 {
		t.Fatalf("manifest = %+v", m)
	}
	if !reflect.DeepEqual(m.Advisories, resolvedAt("0.2.0")) || len(m.Advisories) == 0 {
		t.Errorf("advisories = %+v, want the known-bad list", m.Advisories)
	}
	it, ok := m.Find(supply.KindVT, brand.VTBinary, "arm64")
	if !ok || it.URL != release+"/"+brand.Name+"-vt-linux-arm64" || it.Size != int64(len("VT-ARM64")) || it.Version != "0.6.0" {
		t.Errorf("vt = %+v, %v", it, ok)
	}
	userns, ok := m.Find(supply.KindUserns, brand.UsernsBinary, "amd64")
	if !ok || userns.URL != release+"/"+brand.Name+"-userns-linux-amd64" || userns.Size != int64(len("USERNS-AMD64")) || userns.Version != "0.6.0" {
		t.Errorf("userns = %+v, %v", userns, ok)
	}

	out.Reset()
	if err := run([]string{"assets", manifest}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(out.String()); len(got) != 6 || got[0] != brand.Name+"-host-linux-amd64" {
		t.Errorf("assets = %q", got)
	}

	dl := upload(t, dir)
	out.Reset()
	if err := run([]string{"check", "-url", release + "/", "-dir", dl, manifest}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "sha256 ok") != 6 {
		t.Errorf("check printed %q", out.String())
	}
}

func TestCheckRefuses(t *testing.T) {
	useList(t, resolvedAt("0.2.0"))
	dir := helpersDir(t)
	if err := run([]string{"write", "-release", "0.6.0", dir}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, supply.FileName)

	// A file on the release that differs from the one the client embeds.
	dl := upload(t, dir)
	asset := filepath.Join(dl, supply.CarriedFiles[0].AssetName("arm64"))
	if err := os.WriteFile(asset, []byte("HOST-ARM6X"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"check", "-url", release, "-dir", dl, manifest}, &bytes.Buffer{})
	var f *failure
	if !errors.As(err, &f) || !strings.Contains(f.msg, "does not match its SHA-256") || f.try == "" {
		t.Errorf("a changed asset: %v", err)
	}
	// A file of another size.
	if err := os.WriteFile(asset, []byte("HOST"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check", "-url", release, "-dir", dl, manifest}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "size") {
		t.Errorf("a shorter asset: %v", err)
	}
	// A missing file.
	if err := os.Remove(asset); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check", "-url", release, "-dir", dl, manifest}, &bytes.Buffer{}); err == nil {
		t.Error("a missing asset passed")
	}
	// The release job of another repository, such as a fork's, or of another tag.
	for _, other := range []string{"https://github.com/fork/tool/releases/download/v0.6.0", supply.ReleaseFolder("0.6.1")} {
		err = run([]string{"check", "-url", other, "-dir", upload(t, dir), manifest}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "not at this release") {
			t.Errorf("%s: %v", other, err)
		}
	}
}

// The release job fails when an item of its own release manifest matches
// the manifest's known-bad list (isolation spec 20.9). It fails before it
// reads any file, so the manifest never goes up.
func TestCheckRefusesAKnownBadRelease(t *testing.T) {
	useList(t, resolvedAt("0.2.0"))
	dir := helpersDir(t)
	if err := run([]string{"write", "-release", "0.6.0", dir}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, supply.FileName)
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	m, err := supply.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range supply.Arches {
		vmm := supply.NewItem(supply.KindVMM, "cloud-hypervisor", "54.0", arch, []byte("CHV-"+arch), release+"/cloud-hypervisor-static-"+arch)
		vmm.Licence, vmm.EOL, vmm.HostKernel, vmm.Released = "Apache-2.0 AND BSD-3-Clause", "2027-01-06", ">=6.8", "2026-10-14"
		m.Items = append(m.Items, vmm)
	}
	if b, err = supply.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, b, 0o600); err != nil {
		t.Fatal(err)
	}
	err = run([]string{"check", "-url", release, "-dir", t.TempDir(), manifest}, &bytes.Buffer{})
	var f *failure
	if !errors.As(err, &f) || f.msg != "release: cloud-hypervisor 54.0 matches GHSA-3r3x-6hgg-2m38." || f.try != "bump the pin in guest/supply.lock" {
		t.Errorf("a VMM of 54.0: %#v", err)
	}

	// A release below the first helper whose Ghostty pin holds every fix
	// carries a shadow terminal that its own list names.
	if f := checkRelease(t, "0.1.9"); f == nil || f.msg != "release: "+brand.VTBinary+" 0.1.9 matches GHSA-w9q4-5jvv-mcv4.\n"+
		"release: "+brand.VTBinary+" 0.1.9 matches GHSA-jqr7-69mm-v35h." || f.try != "move the Ghostty pin past the fix (vt/README.md)" {
		t.Errorf("a shadow terminal of 0.1.9: %#v", f)
	}
	// Resolved at 0.2.0, the entries clear 0.2.0 and every later release.
	for _, v := range []string{"0.2.0", "0.3.0", "1.0.0"} {
		if f := checkRelease(t, v); f != nil {
			t.Errorf("%s, with the entries resolved at 0.2.0: %v", v, f)
		}
	}
	// Resolved at 0.3.0, they still refuse 0.2.0.
	useList(t, resolvedAt("0.3.0"))
	if f := checkRelease(t, "0.2.0"); f == nil || !strings.Contains(f.msg, brand.VTBinary+" 0.2.0 matches GHSA-w9q4-5jvv-mcv4.") {
		t.Errorf("0.2.0, with the entries resolved at 0.3.0: %#v", f)
	}
}

// While the shadow terminal's entries are unresolved, as they ship, the
// release job refuses every release, and says how to resolve them.
func TestCheckRefusesWhileUnresolved(t *testing.T) {
	for _, v := range []string{"0.1.3", "0.2.0", "0.3.0", "1.0.0"} {
		f := checkRelease(t, v)
		if f == nil || !strings.Contains(f.msg, "release: "+brand.VTBinary+" "+v+" matches GHSA-w9q4-5jvv-mcv4, whose first fixed release is unresolved.") ||
			!strings.Contains(f.try, "set the entry's before to "+v+" in internal/supply/advisories.go") || !strings.Contains(f.try, "vt/README.md") {
			t.Errorf("%s: %#v", v, f)
		}
	}
}

// checkRelease writes the manifest of a release of v with the helpers of
// a release build, and runs check on it. It returns check's failure, or
// nil when the release passes.
func checkRelease(t *testing.T, v string) *failure {
	t.Helper()
	dir := helpersDir(t)
	if err := run([]string{"write", "-release", v, dir}, &bytes.Buffer{}); err != nil {
		t.Fatalf("write %s: %v", v, err)
	}
	err := run([]string{"check", "-url", supply.ReleaseFolder(v), "-dir", upload(t, dir), filepath.Join(dir, supply.FileName)}, &bytes.Buffer{})
	if err == nil {
		return nil
	}
	var f *failure
	if !errors.As(err, &f) {
		t.Fatalf("check %s: %v", v, err)
	}
	return f
}

// A list entry that breaks the format stops write, and the hint names the
// list, so `just helpers` says what to fix.
func TestWriteRefusesABadList(t *testing.T) {
	list := supply.KnownBad()
	list[0].CVSS, list[0].Severity = 7.5, "medium"
	useList(t, list)
	err := run([]string{"write", "-release", "0.6.0", helpersDir(t)}, &bytes.Buffer{})
	var f *failure
	if !errors.As(err, &f) || !strings.Contains(f.msg, "internal/supply/advisories.go: ") || !strings.Contains(f.msg, "makes it high") ||
		f.try != "fix the entry in internal/supply/advisories.go, then run go test ./internal/supply" {
		t.Errorf("a bad entry: %#v", err)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"sign"},
		{"write", t.TempDir()},
		{"write", "-release", "0.6.0", t.TempDir()},
		{"write", "-release", "0.6.0", "-url", release, t.TempDir()},
		{"assets"},
		{"check", "-url", release},
		{"check", "-nope"},
	} {
		var f *failure
		if err := run(args, &bytes.Buffer{}); !errors.As(err, &f) || f.msg == "" || f.try == "" {
			t.Errorf("%q: err = %v, want a message and a hint", args, err)
		}
	}
}
