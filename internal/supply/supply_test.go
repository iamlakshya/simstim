package supply

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// base is the project's own release of 0.6.0, with a final slash.
var base = ReleaseFolder("0.6.0") + "/"

var (
	hexA = strings.Repeat("a", 64)
	hexB = strings.Repeat("b", 64)
)

// sample is a manifest with an item of each shape that this task defines.
func sample() *Manifest {
	return &Manifest{
		Release: "0.6.0",
		Items: []Item{
			NewItem(KindHelper, "tool-host", "0.6.0", "amd64", []byte("HOST"), base+"tool-host-linux-amd64"),
			NewItem(KindVT, "tool-vt", "0.6.0", "arm64", []byte("VT"), base+"tool-vt-linux-arm64"),
			{Kind: KindVMM, Name: "cloud-hypervisor", Version: "54.1", Arch: "amd64", SHA256: hexA, Size: 7, URL: base + "cloud-hypervisor-static",
				Licence: "Apache-2.0 AND BSD-3-Clause", EOL: "2027-01-06", HostKernel: ">=6.8", Released: "2026-11-11"},
			{Kind: KindKernel, Name: "guest", Version: "6.18", Arch: "arm64", SHA256: hexB, Size: 9, URL: base + "Image-arm64",
				Licence: "GPL-2.0-only", SourceURL: base + "linux-6.18.tar.xz", Released: "2026-09-28"},
			{Kind: KindTools, Name: "tools", Version: "0.6.0", Arch: "amd64", SHA256: hexA, Size: 8192, URL: base + "tools-amd64.erofs",
				RootHash: hexB, Salt: strings.Repeat("0", 64), DataBlocks: 1},
			{Kind: KindImage, Name: "agent-base", Arch: "amd64", Digest: "sha256:" + hexA, RootHash: hexB, DataBlocks: 4},
			{Kind: KindLima, Name: "lima", Version: "2.3.0", Arch: "arm64", SHA256: hexA, Size: 5, URL: base + "lima-2.3.0-Darwin-arm64.tar.gz"},
			{Kind: KindVMImage, Name: "debian", Version: "13", Arch: "arm64", SHA256: hexB, Size: 5, URL: base + "debian-13-arm64.qcow2"},
		},
		Advisories: []Advisory{{ID: "GHSA-f47p-p25q-83rh", Kind: "vmm", Name: "cloud-hypervisor", Before: "51.2", Severity: "high"}},
	}
}

// raw is m as JSON, with no check: what Load reads from a file.
func raw(t *testing.T, m *Manifest) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b, err := Marshal(sample())
	if err != nil {
		t.Fatal(err)
	}
	m, err := Load(b)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, b)
	}
	it, ok := m.Find(KindHelper, "tool-host", "amd64")
	if !ok || it.Size != 4 || it.URL != base+"tool-host-linux-amd64" || it.Version != "0.6.0" {
		t.Fatalf("helper = %+v, %v", it, ok)
	}
	if _, ok := m.Find(KindHelper, "tool-host", "arm64"); ok {
		t.Error("found a helper that the manifest does not name")
	}
	if len(m.Advisories) != 1 || m.Advisories[0].Before != "51.2" {
		t.Errorf("advisories = %+v", m.Advisories)
	}
}

// The project's release URL comes from internal/brand, which must name the
// repository of the module path.
func TestTheReleaseFolderIsTheModulesRepository(t *testing.T) {
	b, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if want := "module github.com/" + brand.Repo + "\n"; !strings.HasPrefix(string(b), want) {
		t.Fatalf("go.mod does not start with %q", want)
	}
	if got, want := ReleaseFolder("v0.6.0"), "https://github.com/"+brand.Repo+"/releases/download/v0.6.0"; got != want {
		t.Errorf("ReleaseFolder = %q, want %q", got, want)
	}
}

func TestMarshalWritesAnEmptyAdvisoryList(t *testing.T) {
	m := sample()
	m.Advisories = nil
	b, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"advisories": []`) || !strings.HasSuffix(string(b), "}\n") {
		t.Errorf("manifest =\n%s", b)
	}
	if strings.Contains(string(b), "runtime") {
		t.Error("a manifest with no runtime label wrote one")
	}
}

func TestLoadRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(m *Manifest)
	}{
		{"no release", "names no release", func(m *Manifest) { m.Release = "" }},
		{"unknown kind", `unknown kind "plugin"`, func(m *Manifest) { m.Items[0].Kind = "plugin" }},
		{"no arch", `unknown architecture ""`, func(m *Manifest) { m.Items[0].Arch = "" }},
		{"unknown arch", `unknown architecture "riscv64"`, func(m *Manifest) { m.Items[0].Arch = "riscv64" }},
		{"no name", "no name", func(m *Manifest) { m.Items[0].Name = "" }},
		{"no version", "no version", func(m *Manifest) { m.Items[0].Version = "" }},
		{"short digest", "not 64 lower-case hex", func(m *Manifest) { m.Items[0].SHA256 = "abc" }},
		{"upper-case digest", "not 64 lower-case hex", func(m *Manifest) { m.Items[0].SHA256 = strings.ToUpper(m.Items[0].SHA256) }},
		{"no size", "no size", func(m *Manifest) { m.Items[0].Size = 0 }},
		{"no url", "url: missing", func(m *Manifest) { m.Items[0].URL = "" }},
		{"http url", "not an https URL", func(m *Manifest) { m.Items[0].URL = strings.Replace(m.Items[0].URL, "https:", "http:", 1) }},
		{"url with a query", "does not name one file", func(m *Manifest) { m.Items[0].URL += "?raw=1" }},
		{"url of a folder", "does not name one file", func(m *Manifest) { m.Items[0].URL = base }},
		{"another release", "outside the project's release", func(m *Manifest) {
			m.Items[1].URL = ReleaseFolder("0.5.0") + "/tool-vt-linux-arm64"
		}},
		{"a folder in the release", "outside the project's release", func(m *Manifest) { m.Items[1].URL = base + "old/tool-vt-linux-arm64" }},
		{"a third party for every item", "outside the project's release", func(m *Manifest) {
			for i := range m.Items {
				if m.Items[i].URL != "" {
					m.Items[i].URL = "https://example.org/acme/tool/releases/download/v0.6.0/x" + m.Items[i].Kind
				}
			}
			m.Items[3].SourceURL = "https://example.org/acme/tool/releases/download/v0.6.0/src"
		}},
		{"another owner of the same name", "outside the project's release", func(m *Manifest) {
			m.Items[0].URL = strings.Replace(m.Items[0].URL, brand.Repo, "someone/"+brand.Name, 1)
		}},
		{"GPL without source", "needs a source_url", func(m *Manifest) { m.Items[3].SourceURL = "" }},
		{"source elsewhere", "outside the project's release", func(m *Manifest) { m.Items[3].SourceURL = "https://cdn.example.org/linux-6.18.tar.xz" }},
		{"vmm without eol", "needs its eol", func(m *Manifest) { m.Items[2].EOL = "" }},
		{"vmm with a date that does not exist", "needs its eol", func(m *Manifest) { m.Items[2].EOL = "2027-13-45" }},
		{"vmm without host kernel", "needs its host_kernel", func(m *Manifest) { m.Items[2].HostKernel = "" }},
		{"kernel without licence or source", "a kernel needs a source_url", func(m *Manifest) {
			m.Items[3].Licence = ""
			m.Items[3].SourceURL = ""
		}},
		{"tools without root hash", "root_hash", func(m *Manifest) { m.Items[4].RootHash = "" }},
		{"tools without salt", "salt", func(m *Manifest) { m.Items[4].Salt = "00" }},
		{"tools without data blocks", "no data_blocks", func(m *Manifest) { m.Items[4].DataBlocks = 0 }},
		{"image without digest", "digest", func(m *Manifest) { m.Items[5].Digest = hexA }},
		{"image without root hash", "root_hash", func(m *Manifest) { m.Items[5].RootHash = "" }},
		{"image without data blocks", "no data_blocks", func(m *Manifest) { m.Items[5].DataBlocks = 0 }},
		{"image with a url", "in place of", func(m *Manifest) { m.Items[5].URL = base + "agent-base" }},
		{"an item twice", "twice", func(m *Manifest) { m.Items = append(m.Items, m.Items[0]) }},
		{"advisory of an unknown kind", `unknown kind "helper"`, func(m *Manifest) { m.Advisories[0].Kind = "helper" }},
		{"advisory without before", "no before", func(m *Manifest) { m.Advisories[0].Before = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sample()
			tc.change(m)
			if _, err := Load(raw(t, m)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load: err = %v, want %q", err, tc.want)
			}
			if _, err := Marshal(m); err == nil {
				t.Error("Marshal wrote it")
			}
		})
	}
}

func TestLoadRefusesWhatItDoesNotKnow(t *testing.T) {
	b, err := Marshal(sample())
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ data, want string }{
		"an expiry":              {strings.Replace(string(b), `"release": "0.6.0"`, `"release": "0.6.0", "expires": "2027-01-01"`, 1), "expires"},
		"a field inside an item": {strings.Replace(string(b), `"kind": "helper"`, `"kind": "helper", "unrecognised": "x"`, 1), "unrecognised"},
		"two manifests":          {string(b) + string(b), "more than one"},
		"a trailing ]":           {string(b) + "]", "more than one"},
		"a trailing }":           {string(b) + "}", "more than one"},
		"trailing junk":          {string(b) + "]garbage", "more than one"},
		"broken JSON":            {"{", "read the release manifest"},
	} {
		if _, err := Load([]byte(tc.data)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if _, err := Load(append(b, " \n\t"...)); err != nil {
		t.Errorf("trailing white space: %v", err)
	}
}

func TestArchitectures(t *testing.T) {
	for machine, want := range map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"} {
		if got, ok := ArchOf(machine); !ok || got != want {
			t.Errorf("ArchOf(%q) = %q, %v", machine, got, ok)
		}
	}
	if got, ok := ArchOf("riscv64"); ok || got != "riscv64" {
		t.Errorf("ArchOf(riscv64) = %q, %v", got, ok)
	}
	for _, a := range Arches {
		for _, m := range Machines(a) {
			if got, _ := ArchOf(m); got != a {
				t.Errorf("Machines(%s) holds %s, which is %s", a, m, got)
			}
		}
	}
	if got := CarriedFiles[0].AssetName("arm64"); got != brand.Name+"-host-linux-arm64" {
		t.Errorf("AssetName = %q", got)
	}
}

func TestCheck(t *testing.T) {
	data := []byte("HOST")
	it := NewItem(KindHelper, "tool-host", "0.6.0", "amd64", data, base+"tool-host-linux-amd64")
	if err := Check(it, "tool-host", data); err != nil {
		t.Fatal(err)
	}
	var me *MismatchError
	if err := Check(it, "tool-host", []byte("HOSX")); !errors.As(err, &me) || me.What != "SHA-256" {
		t.Errorf("a changed byte: %v", err)
	}
	if err := Check(it, "tool-host", []byte("HOST!")); !errors.As(err, &me) || me.What != "size" || !strings.Contains(err.Error(), "5 bytes, not 4 bytes") {
		t.Errorf("a longer file: %v", err)
	}
	img := sample().Items[5]
	if err := Check(img, "agent-base", data); err == nil {
		t.Error("an image was checked by a SHA-256 it does not have")
	}
}

func TestCheckFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tool-host-linux-amd64")
	if err := os.WriteFile(p, []byte("HOST"), 0o600); err != nil {
		t.Fatal(err)
	}
	it := NewItem(KindHelper, "tool-host", "0.6.0", "amd64", []byte("HOST"), base+"tool-host-linux-amd64")
	if err := CheckFile(it, p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("HOSTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	var me *MismatchError
	if err := CheckFile(it, p); !errors.As(err, &me) || me.Name != "tool-host-linux-amd64" || me.What != "size" {
		t.Errorf("a longer file: %v", err)
	}
	if err := CheckFile(it, filepath.Join(dir, "missing")); err == nil || errors.As(err, &me) {
		t.Errorf("a missing file: %v", err)
	}
}
