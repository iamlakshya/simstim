package lima

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

func TestParseLimaVersion(t *testing.T) {
	for _, tc := range []struct {
		in, text string
		old      bool
	}{
		{"limactl version 2.3.0\n", "2.3.0", false},
		{"limactl version 2.2.1", "2.2.1", true},
		{"limactl version 2.10.0", "2.10.0", false},
		// A pre-release comes before its release.
		{"limactl version 2.3.0-rc.1", "2.3.0-rc.1", true},
		{"limactl version 2.3.1", "2.3.1", false},
		{"limactl version 3.0.0", "3.0.0", false},
		{"limactl version 1.9.9", "1.9.9", true},
		// A build from a commit after the tag, as Lima's Makefile names it.
		{"limactl version 2.3.0-5-g1a2b3c4", "2.3.0-5-g1a2b3c4", false},
		{"limactl version v2.3.0-5-g1a2b3c4.m+brew\nextra line\n", "2.3.0-5-g1a2b3c4.m+brew", false},
		{"limactl version 2.2.1-5-g1a2b3c4", "2.2.1-5-g1a2b3c4", true},
		// Build metadata: a changed tree at the tag, and a packager's mark.
		{"limactl version 2.3.0.m", "2.3.0.m", false},
		{"limactl version 2.3.0+brew", "2.3.0+brew", false},
		{"limactl version 2.3.0-5-g1a2b3c4.m", "2.3.0-5-g1a2b3c4.m", false},
		{"limactl version 2.2.1.m", "2.2.1.m", true},
		{"limactl version 2.3.0-rc.1.m", "2.3.0-rc.1.m", true},
	} {
		v, err := parseLimaVersion(tc.in)
		if err != nil || v.text != tc.text || v.before(minVersion) != tc.old {
			t.Errorf("parseLimaVersion(%q) = %+v, %v; before %s = %v, want %v", tc.in, v, err, MinVersion, v.before(minVersion), tc.old)
		}
	}
	for _, in := range []string{"limactl: command failed", "", "limactl version abc", "limactl version 2.3", "version 2.3.0", "limactl version 2.3.0 extra", "limactl version 2.3.0.x"} {
		if v, err := parseLimaVersion(in); err == nil {
			t.Errorf("parseLimaVersion(%q) = %+v, want an error", in, v)
		}
	}
}

func TestCreateRefusesAnOldLima(t *testing.T) {
	for _, tc := range []struct {
		system, msg string
	}{
		{"darwin", "Lima 2.2.1 is too old for host create: 2.3.0 or later can keep the VM away from your Mac's loopback services"},
		{"linux", "Lima 2.2.1 is too old for host create: it needs 2.3.0 or later"},
	} {
		t.Run(tc.system, func(t *testing.T) {
			dir := fakeLima(t, tc.system)
			if err := os.WriteFile(filepath.Join(dir, "version"), []byte("limactl version 2.2.1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Provider{}.Create(context.Background(), provider.Request{Name: "dev"})
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Msg != tc.msg || ue.Hint != "Update Lima (https://lima-vm.io/docs/installation/), then run this again." {
				t.Fatalf("create = %#v", err)
			}
			if cs := calls(t, dir); len(cs) != 1 || cs[0][0] != "--version" {
				t.Errorf("limactl calls after the refusal: %q", cs)
			}
		})
	}
}

// The message quotes what limactl printed: its stderr, else its output,
// else how it ended.
func TestCreateWithoutAVersion(t *testing.T) {
	for _, tc := range []struct {
		file, version, quoted string
	}{
		{"version-fails", "", "limactl: command failed"},
		{"version-fails-quiet", "", "exit status 3"},
		{"version", "lima, a build of my own\n", "lima, a build of my own"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := fakeLima(t, "darwin")
			if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.version), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Provider{}.Create(context.Background(), provider.Request{Name: "dev"})
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Hint != "limactl --version" || ue.Msg != `could not read the version of limactl from "`+tc.quoted+`"` {
				t.Fatalf("create = %#v", err)
			}
			if verbs(calls(t, dir)) != nil {
				t.Error("a VM was made without a known version of Lima")
			}
		})
	}
}

func TestCreateWithoutLimactl(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Provider{}.Create(context.Background(), provider.Request{Name: "dev"})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Msg != "limactl is not installed, or not on your PATH" {
		t.Fatalf("create = %#v", err)
	}
}
