package supply

import (
	"strings"
	"testing"
)

func TestReleaseAssetNamesArePortable(t *testing.T) {
	for _, name := range []string{
		"asset?", "asset#", "sub%2ffile", "sub%2Ffile", "sub/file",
		`..\..\secret`, "..%5c..%5csecret", "asset%00", "asset%20name",
		".hidden", "asset:name", "asset name",
	} {
		t.Run(name, func(t *testing.T) {
			for _, source := range []bool{false, true} {
				m := sample()
				if source {
					m.Items[3].SourceURL = base + name
				} else {
					m.Items[0].URL = base + name
				}
				if err := m.Validate(); err == nil {
					t.Errorf("source=%v accepted unsafe release asset %q", source, name)
				}
			}
		})
	}
}

func TestReleaseIsOneURLComponent(t *testing.T) {
	for _, release := range []string{"0.6.0/sub", `0.6.0\sub`, ".", "..", "0.6.0?", "0.6.0#", "0.6.0%2fsub", "0.6.0 sub", "0.6.0\n"} {
		if err := (&Manifest{Release: release}).Validate(); err == nil {
			t.Errorf("accepted release %q", release)
		}
	}
	for _, release := range []string{"0.6.0", "v0.6.0", "0.6.0-rc.1", "0.6.0+build.1", "0.6.0-12-gabcdef-dirty"} {
		if err := (&Manifest{Release: release}).Validate(); err != nil {
			t.Errorf("release %q: %v", release, err)
		}
	}
}

func TestFileURLRejectsHiddenComponents(t *testing.T) {
	for _, suffix := range []string{"asset?", "asset#", "sub%2ffile", "sub%2Ffile", `..\..\secret`, "..%5c..%5csecret", "asset%00", "asset%20name"} {
		if err := checkURL("https://example.org/releases/" + suffix); err == nil {
			t.Errorf("accepted %q", suffix)
		}
	}
	if err := checkURL("https://example.org/releases/asset.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if err := checkURL("https://example.org/releases/asset?raw=1"); err == nil || !strings.Contains(err.Error(), "one file") {
		t.Fatalf("query error = %v", err)
	}
}
