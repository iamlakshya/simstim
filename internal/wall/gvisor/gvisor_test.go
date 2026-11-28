package gvisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/wall"
)

func TestRunArgsInvariants(t *testing.T) {
	args := RunArgs("/tmp/s/runsc", "/tmp/s/bundle", "s-abc123", 3)
	if err := ValidateRunArgs(args); err != nil {
		t.Fatalf("ValidateRunArgs(%v): %v", args, err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--root /tmp/s/runsc",
		"--network=none",
		"--directfs=false",
		"--ignore-cgroups",
		"--host-uds=none",
		"run --bundle /tmp/s/bundle --pass-fd 3:3 s-abc123",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("RunArgs missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "rootless") {
		t.Errorf("RunArgs must never contain --rootless: %q", joined)
	}

	// Smoke-test args without --pass-fd still satisfy all invariants.
	smoke := RunArgs("/tmp/s/runsc", "/tmp/s/bundle", "smoke-1", -1)
	if err := ValidateRunArgs(smoke); err != nil {
		t.Fatalf("ValidateRunArgs(smoke): %v", err)
	}
	if slices.Contains(smoke, "--pass-fd") {
		t.Errorf("smoke args unexpectedly contain --pass-fd: %v", smoke)
	}
}

func TestValidateRunArgsRefusesForbiddenFlags(t *testing.T) {
	badCases := [][]string{
		{"--root", "/r", "--rootless", "--network=none", "--directfs=false", "--ignore-cgroups", "--host-uds=none", "run", "id"},
		{"--root", "/r", "--network=host", "--directfs=false", "--ignore-cgroups", "--host-uds=none", "run", "id"},
		{"--root", "/r", "--network=sandbox", "--directfs=false", "--ignore-cgroups", "--host-uds=none", "run", "id"},
		{"--root", "/r", "--network=none", "--directfs=true", "--ignore-cgroups", "--host-uds=none", "run", "id"},
		{"--root", "/r", "--network=none", "--directfs=false", "--ignore-cgroups", "--host-uds=open", "run", "id"},
		{"--root", "/r", "--network=none", "--directfs=false", "--ignore-cgroups", "--host-uds=all", "run", "id"},
		{"--root", "/r", "--network=none", "--directfs=false", "--host-uds=none", "run", "id"},
	}
	for _, args := range badCases {
		if err := ValidateRunArgs(args); err == nil {
			t.Errorf("ValidateRunArgs(%v) = nil, want error", args)
		}
	}
}

func TestBuildBundlePlanAndPrepareBundle(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "repo")
	state := filepath.Join(tmp, "state")
	helper := filepath.Join(tmp, "bin", brand.HostBinary)
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("ELF"), 0o700); err != nil {
		t.Fatal(err)
	}

	bundleDir := filepath.Join(tmp, "bundle")
	plan, err := PrepareBundle(bundleDir, Spec{
		SessionID:      "gv01test",
		SessionDir:     tmp,
		HelperExe:      helper,
		WorkspaceHost:  ws,
		WorkspaceGuest: "/work/repo",
		StateHost:      state,
		StateDirs:      []string{".claude/projects"},
		ExtraRO:        []Mount{{Source: "/opt/tool", Dest: "/opt/agents/tool-0", ReadOnly: true}},
		ExtraEnv:       []string{brand.Env("GUEST_PROXY") + "=1"},
		HostUID:        1000,
		HostGID:        1000,
	})
	if err != nil {
		t.Fatalf("PrepareBundle: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(bundleDir, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	// Spec 21.4: OCI bundle must have no hooks.
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode config.json: %v", err)
	}
	if _, hasHooks := doc["hooks"]; hasHooks {
		t.Fatalf("config.json must not include OCI hooks:\n%s", string(raw))
	}

	cfg := plan.Config
	if !cfg.Root.ReadOnly || cfg.Root.Path != "rootfs" {
		t.Errorf("root = %+v, want readonly rootfs", cfg.Root)
	}
	if !cfg.Process.NoNewPrivileges || cfg.Process.User.UID != 1000 || cfg.Process.User.GID != 1000 {
		t.Errorf("process = %+v, want uid/gid 1000 with NoNewPrivileges", cfg.Process)
	}
	if len(cfg.Process.Capabilities.Bounding) != 0 || len(cfg.Process.Capabilities.Effective) != 0 || len(cfg.Process.Capabilities.Permitted) != 0 {
		t.Errorf("capabilities must be empty: %+v", cfg.Process.Capabilities)
	}
	if len(cfg.Linux.UIDMappings) != 1 || cfg.Linux.UIDMappings[0] != (ociIDMapping{ContainerID: 1000, HostID: 1000, Size: 1}) {
		t.Errorf("uidMappings = %+v", cfg.Linux.UIDMappings)
	}
	if cfg.Linux.Seccomp == nil || len(cfg.Linux.Seccomp.Syscalls) < 4 {
		t.Errorf("seccomp missing expected defence-in-depth rules: %+v", cfg.Linux.Seccomp)
	}
	// Must not pass host Landlock env var to gVisor Sentry.
	for _, kv := range cfg.Process.Env {
		if strings.Contains(kv, "LANDLOCK") {
			t.Errorf("gVisor env must not set Landlock var: %q", kv)
		}
	}
	// Verify required mounts are present and sensitive host /etc paths are absent.
	mountByDest := map[string]ociMount{}
	for _, m := range cfg.Mounts {
		mountByDest[m.Destination] = m
	}
	for _, wantDest := range []string{"/proc", "/dev", "/dev/pts", "/etc/passwd", "/etc/resolv.conf", "/home/agent", "/home/agent/.claude/projects", "/work/repo", "/opt/agents/tool-0", plan.GuestHelper} {
		if _, ok := mountByDest[wantDest]; !ok {
			t.Errorf("missing expected OCI mount %q", wantDest)
		}
	}
	for _, forbidden := range []string{"/etc/shadow", "/etc/sudoers", "/etc/environment", "/etc/ssh"} {
		if _, ok := mountByDest[forbidden]; ok {
			t.Errorf("forbidden host /etc mount leaked into bundle: %q", forbidden)
		}
	}
	if !slices.Contains(mountByDest["/opt/agents/tool-0"].Options, "ro") {
		t.Errorf("ExtraRO mount not read-only: %+v", mountByDest["/opt/agents/tool-0"])
	}
	if !slices.Contains(mountByDest["/work/repo"].Options, "rw") {
		t.Errorf("workspace mount not writable: %+v", mountByDest["/work/repo"])
	}
	// Synthetic /etc/passwd exists inside bundle/etc/.
	passwd, err := os.ReadFile(filepath.Join(bundleDir, "etc", "passwd"))
	if err != nil || !strings.Contains(string(passwd), "agent:x:1000:1000:") {
		t.Errorf("synthetic passwd = %q, %v", string(passwd), err)
	}
}

func TestValidateSpecRejectsUnsafeInputs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := Spec{
		SessionID:      "s1",
		HelperExe:      "/usr/local/bin/helper",
		WorkspaceHost:  filepath.Join(home, "proj"),
		WorkspaceGuest: "/work/proj",
		StateHost:      filepath.Join(home, "state"),
	}
	cases := []struct {
		name string
		mod  func(*Spec)
	}{
		{"empty session id", func(s *Spec) { s.SessionID = "" }},
		{"relative helper", func(s *Spec) { s.HelperExe = "helper" }},
		{"root workspace", func(s *Spec) { s.WorkspaceHost = "/" }},
		{"host home workspace", func(s *Spec) { s.WorkspaceHost = home }},
		{"escape guest workspace", func(s *Spec) { s.WorkspaceGuest = "/etc/foo" }},
		{"traversal guest workspace", func(s *Spec) { s.WorkspaceGuest = "/work/../etc" }},
		{"refused bashrc state dir", func(s *Spec) { s.StateDirs = []string{".bashrc"} }},
		{"refused claude settings state dir", func(s *Spec) { s.StateDirs = []string{".claude/settings.json"} }},
		{"traversal state dir", func(s *Spec) { s.StateDirs = []string{"../host"} }},
		{"relative extra mount", func(s *Spec) { s.ExtraRO = []Mount{{Source: "rel", Dest: "/opt/a"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mod(&s)
			if _, err := BuildBundlePlan("/tmp/b", s); err == nil {
				t.Fatalf("BuildBundlePlan unexpectedly accepted %s", tc.name)
			}
		})
	}
}

func TestDriverPrepareRejectsImageAndRoundTrips(t *testing.T) {
	tmp := t.TempDir()
	helper := filepath.Join(tmp, "helper")
	if err := os.WriteFile(helper, []byte("ELF"), 0o700); err != nil {
		t.Fatal(err)
	}
	drv := Driver{}
	if drv.Name() != "gvisor" || drv.Level() != wall.GVisor {
		t.Fatalf("driver = (%q, %v)", drv.Name(), drv.Level())
	}
	_, err := drv.Prepare(context.Background(), wall.Spec{
		ID:         "s1",
		SessionDir: filepath.Join(tmp, "sess-img"),
		Helper:     helper,
		Image:      &wall.ImageRef{Ref: "debian:trixie"},
	})
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("Prepare with Image = %v, want error with Try: hint", err)
	}

	sessDir := filepath.Join(tmp, "sess-ok")
	sb, err := drv.Prepare(context.Background(), wall.Spec{
		ID:         "s2",
		SessionDir: sessDir,
		Helper:     helper,
		StateDirs:  []string{".claude/projects"},
	})
	if err != nil || sb == nil {
		t.Fatalf("Prepare valid = %v, %v", sb, err)
	}
	reopened, err := drv.Open(context.Background(), sessDir)
	if err != nil || reopened == nil {
		t.Fatalf("Open = %v, %v", reopened, err)
	}
}

func TestPinVerificationAndTamperRefusal(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		pin, err := PinForArch(arch)
		if err != nil {
			t.Fatalf("PinForArch(%s): %v", arch, err)
		}
		if len(pin.TarballSHA256) != 64 || !strings.HasPrefix(pin.TarballURL, "https://storage.googleapis.com/gvisor/releases/release/") {
			t.Errorf("bad pin for %s: %+v", arch, pin)
		}
		for _, bin := range requiredBinaries {
			if len(pin.Binaries[bin]) != 64 {
				t.Errorf("pin %s missing SHA-256 for %s", arch, bin)
			}
		}
	}
	if _, err := PinForArch("mips64"); err == nil {
		t.Error("expected error for unsupported arch")
	}

	dir := t.TempDir()
	if _, err := VerifyRuntimeDir(dir, "amd64"); err == nil {
		t.Error("VerifyRuntimeDir accepted empty directory")
	}
	// Write wrong bytes for runsc and verify SHA-256 mismatch is caught.
	if err := os.WriteFile(filepath.Join(dir, "runsc"), []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeDir(dir, "amd64"); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Errorf("VerifyRuntimeDir tamper err = %v, want SHA-256 mismatch", err)
	}

	badTar := filepath.Join(dir, "bad.tar.bz2")
	if err := os.WriteFile(badTar, []byte("not a valid archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallFromTarball(badTar, filepath.Join(dir, "out"), "amd64"); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Errorf("InstallFromTarball bad digest err = %v, want SHA-256 mismatch", err)
	}
}

func TestAppArmorProfileAndFixCmd(t *testing.T) {
	runscPath := "/home/user/.local/share/" + brand.Name + "/artefacts/runtime/" + RuntimeArtifactName + "/runsc"
	prof := AppArmorProfile(runscPath)
	for _, want := range []string{"abi <abi/4.0>,", "include <tunables/global>", "profile " + AppArmorProfileName() + " " + runscPath + " flags=(unconfined)", "userns,"} {
		if !strings.Contains(prof, want) {
			t.Errorf("AppArmorProfile missing %q:\n%s", want, prof)
		}
	}
	cmd := AppArmorFixCmd(runscPath)
	if strings.Count(cmd, "sudo") != 1 || !strings.Contains(cmd, "apparmor_parser -r") || !strings.Contains(cmd, AppArmorProfilePath()) {
		t.Errorf("AppArmorFixCmd = %q, want single sudo command targeting %s", cmd, AppArmorProfilePath())
	}

	// Shell metacharacters, format specifiers, quotes and spaces in runscPath
	// must be safely quoted for AppArmor and never expanded by sh or printf.
	tricky := `/tmp/dir with spaces/$HOME/"quoted"/` + "`id`" + `/100%s/'single'/\back/runsc`
	trickyProf := AppArmorProfile(tricky)
	if !profileMatchesRunsc(trickyProf, tricky) {
		t.Fatalf("profileMatchesRunsc failed for tricky path:\n%s", trickyProf)
	}
	if !strings.Contains(trickyProf, `"/tmp/dir with spaces/$HOME/\"quoted\"/`+"`id`"+`/100%s/'single'/\\back/runsc"`) {
		t.Errorf("AppArmorProfile did not quote/escape tricky path safely:\n%s", trickyProf)
	}
	trickyCmd := AppArmorFixCmd(tricky)
	if strings.Count(trickyCmd, "sudo") != 1 {
		t.Errorf("tricky AppArmorFixCmd = %q, want single sudo", trickyCmd)
	}
	if runtime.GOOS != "windows" {
		// Execute the inner shell writer (without sudo or apparmor_parser) and
		// verify the file receives AppArmorProfile(tricky) byte for byte.
		outPath := filepath.Join(t.TempDir(), "profile")
		dry := strings.TrimPrefix(trickyCmd, "sudo ")
		dry = strings.Replace(dry, ` && apparmor_parser -r "$2"`, "", 1)
		dry = strings.Replace(dry, shellSingleQuote(AppArmorProfilePath()), shellSingleQuote(outPath), 1)
		out, err := exec.CommandContext(context.Background(), "sh", "-c", dry).CombinedOutput()
		if err != nil {
			t.Fatalf("execute AppArmorFixCmd writer: %v\n%s\ncmd=%s", err, string(out), dry)
		}
		got, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("read written profile: %v", err)
		}
		if string(got) != trickyProf {
			t.Errorf("written profile mismatch:\ngot:\n%q\nwant:\n%q", string(got), trickyProf)
		}
	}
}

func TestProbeWithScenarios(t *testing.T) {
	baseDeps := func() ProbeDeps {
		return ProbeDeps{
			GOOS:          "linux",
			GOARCH:        "amd64",
			KernelRelease: func() string { return "6.12.118+deb13-cloud-amd64" },
			ReadFile:      func(string) ([]byte, error) { return nil, os.ErrNotExist },
			Stat:          func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
			FindRunsc:     func(string) (string, error) { return "/opt/gvisor/runsc", nil },
			RunCmd: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return []byte("runsc version release-20261123.1\nspec: 1.2.1\n"), nil
			},
			SmokeRun: func(context.Context, string) error { return nil },
		}
	}

	t.Run("all OK on Debian 13", func(t *testing.T) {
		p := ProbeWith(context.Background(), baseDeps())
		if !p.OK || p.Version != "release-20261123.1" || p.Facts["platform"] != "systrap" || p.Facts["smoke_ok"] != "true" {
			t.Fatalf("probe = %+v", p)
		}
	})

	t.Run("non-Linux refused", func(t *testing.T) {
		d := baseDeps()
		d.GOOS = "darwin"
		p := ProbeWith(context.Background(), d)
		if p.OK || !strings.Contains(p.Reason, "require Linux") || p.Fix == "" {
			t.Fatalf("probe = %+v", p)
		}
	})

	t.Run("kernel older than 5.6 refused", func(t *testing.T) {
		d := baseDeps()
		d.KernelRelease = func() string { return "5.4.0-generic" }
		p := ProbeWith(context.Background(), d)
		if p.OK || !strings.Contains(p.Reason, "5.6") {
			t.Fatalf("probe = %+v", p)
		}
	})

	t.Run("missing pinned runsc gives install fix", func(t *testing.T) {
		d := baseDeps()
		d.FindRunsc = func(string) (string, error) { return "", os.ErrNotExist }
		p := ProbeWith(context.Background(), d)
		if p.OK || !strings.Contains(p.Reason, PinnedRelease) || !strings.Contains(p.Fix, "wall install gvisor") {
			t.Fatalf("probe = %+v", p)
		}
	})

	t.Run("Ubuntu AppArmor restriction without profile gives single-sudo fix", func(t *testing.T) {
		d := baseDeps()
		d.ReadFile = func(path string) ([]byte, error) {
			if path == procAppArmorUserNS {
				return []byte("1\n"), nil
			}
			return nil, os.ErrNotExist
		}
		d.SmokeRun = func(context.Context, string) error {
			return errors.New("fork/exec gvisor-sentry-prewarmer: operation not permitted")
		}
		p := ProbeWith(context.Background(), d)
		if p.OK || !strings.Contains(p.Reason, "AppArmor") || strings.Count(p.Fix, "sudo") != 1 || !strings.Contains(p.Fix, AppArmorProfilePath()) {
			t.Fatalf("probe = %+v", p)
		}
		if p.Facts["runsc_apparmor"] != "blocked" {
			t.Errorf("facts = %+v", p.Facts)
		}
	})

	t.Run("Ubuntu AppArmor restriction with matching profile reloads it", func(t *testing.T) {
		d := baseDeps()
		d.ReadFile = func(path string) ([]byte, error) {
			switch path {
			case procAppArmorUserNS:
				return []byte("1\n"), nil
			case AppArmorProfilePath():
				return []byte(AppArmorProfile("/opt/gvisor/runsc")), nil
			default:
				return nil, os.ErrNotExist
			}
		}
		d.SmokeRun = func(context.Context, string) error {
			return errors.New("operation not permitted")
		}
		p := ProbeWith(context.Background(), d)
		want := "sudo apparmor_parser -r " + AppArmorProfilePath()
		if p.OK || p.Fix != want {
			t.Fatalf("probe.Fix = %q, want %q (probe=%+v)", p.Fix, want, p)
		}
		if p.Facts["runsc_apparmor_profile"] != "installed" {
			t.Errorf("facts = %+v", p.Facts)
		}
	})

	t.Run("Ubuntu AppArmor restriction with stale profile rewrites it", func(t *testing.T) {
		d := baseDeps()
		d.ReadFile = func(path string) ([]byte, error) {
			switch path {
			case procAppArmorUserNS:
				return []byte("1\n"), nil
			case AppArmorProfilePath():
				return []byte(AppArmorProfile("/opt/gvisor-old/runsc")), nil
			default:
				return nil, os.ErrNotExist
			}
		}
		d.SmokeRun = func(context.Context, string) error {
			return errors.New("operation not permitted")
		}
		p := ProbeWith(context.Background(), d)
		want := AppArmorFixCmd("/opt/gvisor/runsc")
		if p.OK || p.Fix != want {
			t.Fatalf("probe.Fix = %q, want %q (probe=%+v)", p.Fix, want, p)
		}
		if p.Facts["runsc_apparmor_profile"] != "stale" {
			t.Errorf("facts = %+v", p.Facts)
		}
	})

	t.Run("max_user_namespaces=0 gives sysctl fix", func(t *testing.T) {
		d := baseDeps()
		d.ReadFile = func(path string) ([]byte, error) {
			if path == procMaxUserNS {
				return []byte("0\n"), nil
			}
			return nil, os.ErrNotExist
		}
		d.SmokeRun = func(context.Context, string) error { return errors.New("ENOSPC") }
		p := ProbeWith(context.Background(), d)
		if p.OK || !strings.Contains(p.Reason, "max_user_namespaces") || !strings.Contains(p.Fix, "sysctl -p") {
			t.Fatalf("probe = %+v", p)
		}
	})
}

func TestFakeRunscSandboxLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("gVisor sandbox lifecycle test runs on Linux")
	}
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "work")
	state := filepath.Join(tmp, "state")
	helper := filepath.Join(tmp, "bin", brand.HostBinary)
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("ELF"), 0o700); err != nil {
		t.Fatal(err)
	}

	argsLog := filepath.Join(tmp, "runsc-args.log")
	pidFile := filepath.Join(tmp, "fake-runsc.pid")
	fakeRunsc := filepath.Join(tmp, "fake-runsc")
	script := `#!/usr/bin/env python3
import os, signal, socket, sys, time
with open(` + `"` + argsLog + `"` + `, "a") as f:
    f.write(" ".join(sys.argv[1:]) + "\n")
if "kill" in sys.argv:
    try:
        pid = int(open(` + `"` + pidFile + `"` + `).read().strip())
        os.kill(pid, signal.SIGTERM)
    except Exception:
        pass
    sys.exit(0)
if "delete" in sys.argv:
    sys.exit(0)
open(` + `"` + pidFile + `"` + `, "w").write(str(os.getpid()))
stopped = False
def on_term(*_):
    global stopped
    stopped = True
signal.signal(signal.SIGTERM, on_term)
s = socket.fromfd(3, socket.AF_UNIX, socket.SOCK_STREAM)
s.sendall(b"GUEST_FD3_OK\n")
data = s.recv(64)
if data == b"PING\n":
    s.sendall(b"PONG\n")
while not stopped:
    time.sleep(0.05)
s.close()
sys.exit(0)
`
	if err := os.WriteFile(fakeRunsc, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	sessDir := filepath.Join(tmp, "sess")
	sb := NewSandbox(Spec{
		SessionID:      "fake01",
		SessionDir:     sessDir,
		HelperExe:      helper,
		WorkspaceHost:  ws,
		WorkspaceGuest: "/work/app",
		StateHost:      state,
		StateDirs:      []string{".claude/projects"},
		RunscPath:      fakeRunsc,
	})
	conn, err := sb.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "GUEST_FD3_OK\n" {
		t.Fatalf("read fd3 greeting = %q, %v (stderr=%q)", string(buf[:n]), err, sb.Stderr())
	}
	if _, err := conn.Write([]byte("PING\n")); err != nil {
		t.Fatalf("write PING: %v", err)
	}
	n, err = conn.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read PONG: %v", err)
	}
	if string(buf[:n]) != "PONG\n" {
		t.Fatalf("reply = %q, want PONG", string(buf[:n]))
	}

	if err := sb.Stop(context.Background(), 3*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	ex := sb.Wait()
	if ex.Code != 0 {
		t.Fatalf("Wait exit = %+v (stderr=%q)", ex, sb.Stderr())
	}
	// Calling Kill or Stop again after exit must return immediately without
	// invoking runsc kill or signaling any reaped PID.
	if err := sb.Kill(); err != nil {
		t.Fatalf("Kill after exit: %v", err)
	}
	if err := sb.Stop(context.Background(), time.Second); err != nil {
		t.Fatalf("Stop after exit: %v", err)
	}
	if err := sb.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, gone := range []string{filepath.Join(sessDir, "runsc"), filepath.Join(sessDir, "bundle")} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Destroy left %s behind", gone)
		}
	}
	// Kept workspace and state folder survive Destroy.
	if _, err := os.Stat(ws); err != nil {
		t.Errorf("workspace disappeared after Destroy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, ".claude", "projects")); err != nil {
		t.Errorf("kept state dir disappeared after Destroy: %v", err)
	}

	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "--network=none --directfs=false --ignore-cgroups --host-uds=none run --bundle") ||
		!strings.Contains(string(logged), "--pass-fd 3:3 s-fake01") ||
		!strings.Contains(string(logged), "kill s-fake01 TERM") {
		t.Errorf("unexpected runsc invocations:\n%s", string(logged))
	}
	if strings.Contains(string(logged), "kill s-fake01 KILL") {
		t.Errorf("Kill after exit should not have invoked runsc kill KILL:\n%s", string(logged))
	}
}

func TestKillWhileDeletingSkipsSignalAndWaitsForDone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("gVisor sandbox lifecycle test runs on Linux")
	}
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "work")
	state := filepath.Join(tmp, "state")
	helper := filepath.Join(tmp, "bin", brand.HostBinary)
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("ELF"), 0o700); err != nil {
		t.Fatal(err)
	}
	argsLog := filepath.Join(tmp, "args.log")
	deletingFlag := filepath.Join(tmp, "deleting")
	fakeRunsc := filepath.Join(tmp, "fake-runsc")
	script := `#!/usr/bin/env python3
import os, sys, time
with open(` + `"` + argsLog + `"` + `, "a") as f:
    f.write(" ".join(sys.argv[1:]) + "\n")
if "delete" in sys.argv:
    open(` + `"` + deletingFlag + `"` + `, "w").write("1")
    time.sleep(0.3)
    sys.exit(0)
if "kill" in sys.argv:
    sys.exit(0)
# Exit immediately so cmd.Wait completes and reap enters runsc delete.
sys.exit(0)
`
	if err := os.WriteFile(fakeRunsc, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	sb := NewSandbox(Spec{
		SessionID:      "delrace",
		SessionDir:     filepath.Join(tmp, "sess"),
		HelperExe:      helper,
		WorkspaceHost:  ws,
		WorkspaceGuest: "/work/app",
		StateHost:      state,
		RunscPath:      fakeRunsc,
	})
	conn, err := sb.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conn.Close()

	// Wait until reap has finished cmd.Wait (exited closed) and entered runsc delete.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(deletingFlag); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(deletingFlag); err != nil {
		t.Fatalf("fake runsc delete never started")
	}

	if err := sb.Kill(); err != nil {
		t.Fatalf("Kill during delete: %v", err)
	}
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "kill s-delrace KILL") {
		t.Errorf("Kill during delete should not have invoked runsc kill KILL:\n%s", string(logged))
	}
}
