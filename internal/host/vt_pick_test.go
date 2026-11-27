package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests check how the vt tests pick their shadow terminal: never one
// built from another Ghostty commit than the pin, and with the note of spec
// 19.1 when they skip. Fake Zigs and fake shadow terminals stand in for the
// real ones, so the tests need no Zig.

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// versionScript is a fake shadow terminal whose --version names commit, or
// one that fails when commit is "".
func versionScript(commit string) string {
	if commit == "" {
		return "exit 1\n"
	}
	return "echo '0.1.0 ghostty " + commit + "'\n"
}

func TestPickVTBinary(t *testing.T) {
	pin, old, none := strings.Repeat("a", 40), strings.Repeat("b", 40), ""
	tools := t.TempDir()
	zig16 := writeScript(t, filepath.Join(tools, "zig16"), "echo 0.16.0\n")
	zigBroken := writeScript(t, filepath.Join(tools, "zig-broken"), "exit 1\n")
	// Zig 0.17 builds a shadow terminal of the pin, as `zig build` would.
	zig17 := writeScript(t, filepath.Join(tools, "zig17"), strings.ReplaceAll(`case "$1" in
version) echo 0.17.0 ;;
build) mkdir -p zig-out/bin && printf '#!/bin/sh\necho "0.1.0 ghostty PIN"\n' >zig-out/bin/shadow-vt && chmod +x zig-out/bin/shadow-vt ;;
esac
`, "PIN", pin))
	zig17Fails := writeScript(t, filepath.Join(tools, "zig17-fails"), `case "$1" in
version) echo 0.17.0 ;;
build) echo "no network" >&2; exit 1 ;;
esac
`)
	envPin := writeScript(t, filepath.Join(tools, "env-pin"), versionScript(pin))
	envOld := writeScript(t, filepath.Join(tools, "env-old"), versionScript(old))
	envBroken := writeScript(t, filepath.Join(tools, "env-broken"), versionScript(""))

	const (
		pickNone  = ""
		pickBuilt = "built"
		pickEnv   = "env"
	)
	for _, c := range []struct {
		name     string
		build    *string // the commit of an earlier build in zig-out, or nil for none
		env, zig string
		pick     string
		skip     []string // parts of the skip reason
		fail     string   // part of the failure
	}{
		{name: "no Zig and no build", pick: pickNone, skip: []string{"needs Zig 0.17.0, and this machine has no Zig.", "Try: just setup"}},
		{name: "no Zig and a build of the pin", build: &pin, pick: pickBuilt},
		{name: "no Zig and an old build", build: &old, pick: pickNone,
			skip: []string{"has no Zig.", "from Ghostty " + old + ", not the pinned " + pin}},
		{name: "Zig 0.16 and an old build", build: &old, zig: zig16, pick: pickNone,
			skip: []string{"this machine has 0.16.0.", "from Ghostty " + old + ", not the pinned " + pin, "Try: just setup"}},
		{name: "Zig 0.16 and a build of the pin", build: &pin, zig: zig16, pick: pickBuilt},
		{name: "Zig 0.16 and a build with no version", build: &none, zig: zig16, pick: pickNone,
			skip: []string{"has 0.16.0.", "from Ghostty an unknown commit, not the pinned " + pin}},
		{name: "a Zig that fails and an old build", build: &old, zig: zigBroken, pick: pickNone,
			skip: []string{"has no Zig.", "not the pinned " + pin}},
		{name: "Zig 0.17 builds the pin", zig: zig17, pick: pickBuilt},
		{name: "Zig 0.17 replaces an old build", build: &old, zig: zig17, pick: pickBuilt},
		{name: "Zig 0.17 fails to build", zig: zig17Fails, pick: pickNone, fail: "zig build in vt/ failed"},
		{name: "VT_BINARY of the pin", build: &old, env: envPin, zig: zig16, pick: pickEnv},
		{name: "VT_BINARY of another commit", env: envOld, zig: zig17, pick: pickNone,
			skip: []string{"VT_BINARY " + envOld + " is built from Ghostty " + old + ", not the pinned " + pin, "Try:"}},
		{name: "VT_BINARY with no version", env: envBroken, pick: pickNone, skip: []string{"Ghostty an unknown commit"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			zon := `.{ .dependencies = .{ .ghostty = .{ .url = "https://github.com/ghostty-org/ghostty/archive/` + pin + `.tar.gz" } } }`
			if err := os.WriteFile(filepath.Join(dir, "build.zig.zon"), []byte(zon), 0o644); err != nil {
				t.Fatal(err)
			}
			built := filepath.Join(dir, "zig-out", "bin", "shadow-vt")
			if c.build != nil {
				writeScript(t, built, versionScript(*c.build))
			}
			path, skip, fail := pickVTBinary(dir, c.env, c.zig)
			want := map[string]string{pickNone: "", pickBuilt: built, pickEnv: c.env}[c.pick]
			if path != want {
				t.Errorf("picked %q, want %q (skip %q, fail %q)", path, want, skip, fail)
			}
			if path != "" && skip != "" {
				t.Errorf("picked %q and skips: %q", path, skip)
			}
			for _, part := range c.skip {
				if !strings.Contains(skip, part) {
					t.Errorf("skip = %q, want %q in it", skip, part)
				}
			}
			if c.skip == nil && skip != "" {
				t.Errorf("skip = %q, want none", skip)
			}
			if (c.fail == "") != (fail == "") || !strings.Contains(fail, c.fail) {
				t.Errorf("fail = %q, want %q", fail, c.fail)
			}
		})
	}

	// With no pin to compare with, the tests fail and say so.
	if path, _, fail := pickVTBinary(t.TempDir(), envPin, zig16); path != "" || !strings.Contains(fail, "read the shadow terminal's pin") {
		t.Errorf("with no build.zig.zon: picked %q, fail %q", path, fail)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "build.zig.zon"), []byte(".{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if path, _, fail := pickVTBinary(dir, envPin, zig16); path != "" || !strings.Contains(fail, "pins no Ghostty commit") {
		t.Errorf("with no pin: picked %q, fail %q", path, fail)
	}
}
