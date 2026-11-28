package lima

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

func makeTarGz(t *testing.T, entries []struct {
	name     string
	typeflag byte
	body     string
	linkname string
	mode     int64
}) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Size:     int64(len(e.body)),
			Linkname: e.linkname,
			Mode:     mode,
		}
		if e.typeflag == tar.TypeDir || e.typeflag == tar.TypeSymlink {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typeflag == tar.TypeReg && len(e.body) > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func TestEnsurePinnedDownloadAndVerify(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake limactl requires POSIX")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	raw, digest := makeTarGz(t, []struct {
		name     string
		typeflag byte
		body     string
		linkname string
		mode     int64
	}{
		{name: "bin/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "bin/limactl", typeflag: tar.TypeReg, body: "#!/bin/sh\necho 'limactl version " + PinnedVersion + "'\n", mode: 0o755},
		{name: "share/lima/ok.txt", typeflag: tar.TypeReg, body: "ok\n", mode: 0o644},
		{name: "bin/lima-link", typeflag: tar.TypeSymlink, linkname: "limactl", mode: 0o777},
	})

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/lima-test.tar.gz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	origURL, origClient := pinnedBaseURL, pinnedClient
	origOS, origArch := goos, goarch
	origArchives := pinnedArchives
	t.Cleanup(func() {
		pinnedBaseURL, pinnedClient = origURL, origClient
		goos, goarch = origOS, origArch
		pinnedArchives = origArchives
	})
	goos, goarch = "darwin", "arm64"
	pinnedBaseURL = srv.URL
	pinnedClient = srv.Client()
	pinnedArchives = map[string]pinnedArchive{
		"darwin/arm64": {file: "lima-test.tar.gz", sha256: digest},
	}

	var out bytes.Buffer
	bin, err := EnsurePinned(context.Background(), &out)
	if err != nil {
		t.Fatalf("EnsurePinned: %v", err)
	}
	if bin != PinnedBinary() || limaCommand() != bin {
		t.Fatalf("bin=%q PinnedBinary=%q limaCommand=%q", bin, PinnedBinary(), limaCommand())
	}
	if hits.Load() != 1 {
		t.Fatalf("download hits = %d, want 1", hits.Load())
	}
	if !strings.Contains(out.String(), "SHA-256 verified") {
		t.Errorf("progress output lacks verification note: %q", out.String())
	}

	// Second call reuses the installed binary without hitting HTTP again.
	bin2, err := EnsurePinned(context.Background(), &out)
	if err != nil || bin2 != bin || hits.Load() != 1 {
		t.Fatalf("second EnsurePinned = %q, %v (hits=%d)", bin2, err, hits.Load())
	}
}

func TestEnsurePinnedRejectsDigestMismatch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	raw, _ := makeTarGz(t, []struct {
		name     string
		typeflag byte
		body     string
		linkname string
		mode     int64
	}{
		{name: "bin/limactl", typeflag: tar.TypeReg, body: "tampered", mode: 0o755},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	origURL, origClient := pinnedBaseURL, pinnedClient
	origOS, origArch := goos, goarch
	origArchives := pinnedArchives
	t.Cleanup(func() {
		pinnedBaseURL, pinnedClient = origURL, origClient
		goos, goarch = origOS, origArch
		pinnedArchives = origArchives
	})
	goos, goarch = "darwin", "arm64"
	pinnedBaseURL = srv.URL
	pinnedClient = srv.Client()
	pinnedArchives = map[string]pinnedArchive{
		"darwin/arm64": {file: "lima-test.tar.gz", sha256: strings.Repeat("0", 64)},
	}

	_, err := EnsurePinned(context.Background(), nil)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "lima_digest" {
		t.Fatalf("EnsurePinned bad digest = %+v, want lima_digest", err)
	}
	if _, statErr := os.Stat(PinnedDir()); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("PinnedDir was created on digest mismatch: %v", statErr)
	}
}

func TestExtractTarGzRejectsTraversalAndSymlinkEscape(t *testing.T) {
	cases := []struct {
		name    string
		entries []struct {
			name     string
			typeflag byte
			body     string
			linkname string
			mode     int64
		}
	}{
		{
			name: "dotdot",
			entries: []struct {
				name     string
				typeflag byte
				body     string
				linkname string
				mode     int64
			}{{name: "../evil", typeflag: tar.TypeReg, body: "x", mode: 0o644}},
		},
		{
			name: "absolute",
			entries: []struct {
				name     string
				typeflag byte
				body     string
				linkname string
				mode     int64
			}{{name: "/etc/evil", typeflag: tar.TypeReg, body: "x", mode: 0o644}},
		},
		{
			name: "symlink-escape",
			entries: []struct {
				name     string
				typeflag byte
				body     string
				linkname string
				mode     int64
			}{{name: "bin/escape", typeflag: tar.TypeSymlink, linkname: "../../outside", mode: 0o777}},
		},
		{
			name: "symlink-abs",
			entries: []struct {
				name     string
				typeflag byte
				body     string
				linkname string
				mode     int64
			}{{name: "bin/abs", typeflag: tar.TypeSymlink, linkname: "/etc/passwd", mode: 0o777}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := makeTarGz(t, tc.entries)
			tgz := filepath.Join(t.TempDir(), "bad.tar.gz")
			if err := os.WriteFile(tgz, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := extractTarGz(tgz, t.TempDir()); err == nil {
				t.Fatalf("extractTarGz(%s) succeeded, want error", tc.name)
			}
		})
	}
}

func TestCheckMacHost(t *testing.T) {
	origOS, origArch, origVer := goos, goarch, macOSVersionFunc
	t.Cleanup(func() { goos, goarch, macOSVersionFunc = origOS, origArch, origVer })

	goos, goarch = "linux", "amd64"
	var ue *client.UserError
	if err := CheckMacHost(context.Background()); !errors.As(err, &ue) || ue.Code != "not_mac" {
		t.Errorf("linux = %v, want not_mac", err)
	}

	goos, goarch = "darwin", "amd64"
	if err := CheckMacHost(context.Background()); !errors.As(err, &ue) || ue.Code != "mac_unsupported" {
		t.Errorf("intel mac = %v, want mac_unsupported", err)
	}

	goos, goarch = "darwin", "arm64"
	macOSVersionFunc = func(context.Context) (string, error) { return "14.7.2", nil }
	if err := CheckMacHost(context.Background()); !errors.As(err, &ue) || ue.Code != "mac_unsupported" {
		t.Errorf("macOS 14 = %v, want mac_unsupported", err)
	}

	for _, ver := range []string{"15.0", "26.2"} {
		macOSVersionFunc = func(context.Context) (string, error) { return ver, nil }
		if err := CheckMacHost(context.Background()); err != nil {
			t.Errorf("macOS %s = %v, want nil", ver, err)
		}
	}
}

func TestMacResources(t *testing.T) {
	origHW := macHardwareFunc
	t.Cleanup(func() { macHardwareFunc = origHW })

	cases := []struct {
		totalCPUs int
		totalGiB  int64
		wantCPUs  int
		wantGiB   int
	}{
		{11, 18, 5, 8},
		{8, 16, 3, 7},
		{8, 8, 3, 3},
		{14, 36, 6, 17},
	}
	for _, tc := range cases {
		macHardwareFunc = func(context.Context) (int, int64, error) {
			return tc.totalCPUs, tc.totalGiB << 30, nil
		}
		cpus, mem := MacResources(context.Background())
		if cpus != tc.wantCPUs || mem != tc.wantGiB {
			t.Errorf("MacResources(%d, %dGiB) = (%d, %d), want (%d, %d)",
				tc.totalCPUs, tc.totalGiB, cpus, mem, tc.wantCPUs, tc.wantGiB)
		}
	}
	macHardwareFunc = func(context.Context) (int, int64, error) { return 0, 0, errors.New("boom") }
	if cpus, mem := MacResources(context.Background()); cpus != DefaultCPUs || mem != DefaultMemoryGiB {
		t.Errorf("MacResources error fallback = (%d, %d), want defaults", cpus, mem)
	}
}

// autostartFakeLimactl handles list/create/start/stop/delete/autostart for macOS autostart tests.
const autostartFakeLimactl = `#!/bin/sh
echo "$*" >> "$FAKE_DIR/calls"
cmd=$1; shift
case $cmd in
--version) echo "limactl version 2.3.0" ;;
list)
  for f in "$FAKE_DIR"/inst-*; do
    [ -f "$f" ] || continue
    i=${f##*/inst-}
    st=$(cat "$f")
    printf '{"name":"%s","status":"%s","dir":"%s/dir-%s","vmType":"vz","sshConfigFile":"%s/ssh-%s","config":{"networkOpts":{"hostLoopback":false},"nestedVirtualization":false}}\n' "$i" "$st" "$FAKE_DIR" "$i" "$FAKE_DIR" "$i"
  done ;;
create)
  inst=""
  for a; do case $a in --name=*) inst=${a#--name=} ;; esac; done
  mkdir -p "$FAKE_DIR/dir-$inst"
  echo "Stopped" > "$FAKE_DIR/inst-$inst" ;;
start)
  for a; do inst=$a; done
  echo "Running" > "$FAKE_DIR/inst-$inst" ;;
stop)
  for a; do inst=$a; done
  echo "Stopped" > "$FAKE_DIR/inst-$inst" ;;
delete)
  for a; do inst=$a; done
  rm -f "$FAKE_DIR/inst-$inst" ;;
autostart)
  sub=$1; shift
  cond="login"
  for a; do
    case $a in
      --condition=*) cond=${a#--condition=} ;;
      *) inst=$a ;;
    esac
  done
  case $sub in
    enable)
      if [ "$cond" = boot ]; then
        touch "$DAEMON_DIR/io.lima-vm.daemon.$inst.plist"
        echo "Running" > "$FAKE_DIR/inst-$inst"
      else
        touch "$AGENT_DIR/io.lima-vm.autostart.$inst.plist"
      fi ;;
    disable)
      rm -f "$DAEMON_DIR/io.lima-vm.daemon.$inst.plist" "$AGENT_DIR/io.lima-vm.autostart.$inst.plist" ;;
  esac ;;
esac
`

func TestAutostartLifecycleAndDoctorCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake limactl requires POSIX")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	daemonDir := filepath.Join(root, "LaunchDaemons")
	agentDir := filepath.Join(root, "LaunchAgents")
	for _, d := range []string{binDir, daemonDir, agentDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(binDir, "limactl"), []byte(autostartFakeLimactl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", root)
	t.Setenv("DAEMON_DIR", daemonDir)
	t.Setenv("AGENT_DIR", agentDir)

	origOS := goos
	origDaemon, origAgent := daemonPlistDir, agentPlistDir
	origSudo, origPoll := sudoAvailable, bootWaitPoll
	t.Cleanup(func() {
		goos = origOS
		daemonPlistDir, agentPlistDir = origDaemon, origAgent
		sudoAvailable, bootWaitPoll = origSudo, origPoll
	})
	goos = "darwin"
	daemonPlistDir = func() string { return daemonDir }
	agentPlistDir = func() string { return agentDir }
	bootWaitPoll = 10 * time.Millisecond

	ctx := context.Background()
	p := Provider{}

	// 1. Explicit boot without sudo -> fails with autostart_sudo and deletes the VM.
	sudoAvailable = func(context.Context) bool { return false }
	if _, err := p.Create(ctx, provider.Request{
		Name: "nosudo", Autostart: AutostartBoot, AutostartExplicit: true,
	}); err == nil {
		t.Fatal("Create with explicit boot and no sudo succeeded")
	} else {
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != "autostart_sudo" {
			t.Fatalf("err = %+v, want autostart_sudo", err)
		}
	}
	if in, _ := find(ctx, provider.MachineName("nosudo")); in != nil {
		t.Error("failed autostart left orphan VM behind")
	}

	// 2. Implicit boot without sudo -> falls back to login LaunchAgent, prints boot command.
	var out bytes.Buffer
	hLogin, err := p.Create(ctx, provider.Request{
		Name: "maclogin", Autostart: AutostartBoot, AutostartExplicit: false, Out: &out,
	})
	if err != nil {
		t.Fatalf("Create fallback to login: %v", err)
	}
	if hLogin.Machine.Autostart != AutostartLogin {
		t.Fatalf("Autostart = %q, want login", hLogin.Machine.Autostart)
	}
	if !strings.Contains(out.String(), "--condition=boot") {
		t.Errorf("fallback output did not print boot command:\n%s", out.String())
	}
	if c, ok := AutostartCheck(ctx, hLogin); !ok || !c.OK || !strings.Contains(c.Detail, "starts at login") {
		t.Errorf("AutostartCheck(login) = %+v, %v", c, ok)
	}
	// Destroy unregisters LaunchAgent before deleting VM.
	lc, err := p.Open(hLogin)
	if err != nil {
		t.Fatal(err)
	}
	if err := lc.Destroy(ctx); err != nil {
		t.Fatalf("Destroy(login): %v", err)
	}
	if AutostartState(hLogin.Machine.ID) != AutostartOff {
		t.Error("Destroy left LaunchAgent plist behind")
	}

	// 3. Boot with sudo available -> stops VM, enables boot LaunchDaemon, waits for Running.
	sudoAvailable = func(context.Context) bool { return true }
	hBoot, err := p.Create(ctx, provider.Request{
		Name: "macboot", Autostart: AutostartBoot, AutostartExplicit: true,
	})
	if err != nil {
		t.Fatalf("Create boot: %v", err)
	}
	if hBoot.Machine.Autostart != AutostartBoot || AutostartState(hBoot.Machine.ID) != AutostartBoot {
		t.Fatalf("boot state = %q / %q", hBoot.Machine.Autostart, AutostartState(hBoot.Machine.ID))
	}
	if c, ok := AutostartCheck(ctx, hBoot); !ok || !c.OK || !strings.Contains(c.Detail, "starts at boot") {
		t.Errorf("AutostartCheck(boot) = %+v, %v", c, ok)
	}

	// 4. Plain Lima VM with no autostart -> AutostartCheck returns false (keeps doctor clean).
	plain := client.Host{Name: "plain", Machine: &client.Machine{Provider: "lima", ID: provider.MachineName("plain")}}
	if _, ok := AutostartCheck(ctx, plain); ok {
		t.Error("AutostartCheck returned true for plain VM with no autostart")
	}

	// 5. Missing plist on a host expecting boot -> warns with enable command.
	if err := os.Remove(daemonPlistPath(hBoot.Machine.ID)); err != nil {
		t.Fatal(err)
	}
	if c, ok := AutostartCheck(ctx, hBoot); !ok || c.OK || !strings.Contains(c.Fix, "autostart enable --condition=boot") {
		t.Errorf("AutostartCheck(missing) = %+v, %v", c, ok)
	}
}
