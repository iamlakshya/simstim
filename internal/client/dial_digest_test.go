package client_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// shellTransport runs remote POSIX commands via /bin/sh -c with a custom HOME
// and optional PATH, matching how SSH executes remote commands on a host.
type shellTransport struct {
	home string
	path string
}

func (s *shellTransport) env() []string {
	env := append([]string(nil), os.Environ()...)
	set := func(k, v string) {
		prefix := k + "="
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = prefix + v
				return
			}
		}
		env = append(env, prefix+v)
	}
	set("HOME", s.home)
	if s.path != "" {
		set("PATH", s.path)
	}
	return env
}

func (s *shellTransport) Start(ctx context.Context, remote string) (client.Proc, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
	cmd.Env = s.env()
	return client.CommandProc(cmd, nil)
}

func (s *shellTransport) Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
	cmd.Env = s.env()
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (s *shellTransport) ReachHint() string { return "check the shell host" }

type shellLifecycle struct{ tr client.Transport }

func (shellLifecycle) Status(context.Context) (string, error) { return client.MachineRunning, nil }
func (shellLifecycle) Start(context.Context) error            { return nil }
func (shellLifecycle) Stop(context.Context) error             { return nil }
func (shellLifecycle) Destroy(context.Context) error          { return nil }
func (l shellLifecycle) Transport(context.Context, bool) (client.Transport, error) {
	return l.tr, nil
}

// writeFakeHelper installs an executable shell script at the host helper path
// under home. When executed, it creates sentinel and emits a valid host THello
// frame so client.Dial completes its handshake.
func writeFakeHelper(t *testing.T, home, ver string) (helperPath, sentinelPath string, digest string) {
	t.Helper()
	var helloBuf bytes.Buffer
	w := proto.NewWriter(&helloBuf)
	if err := w.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  version.Version,
		Role:     "host",
	}); err != nil {
		t.Fatal(err)
	}
	helloPath := filepath.Join(home, "hello.frame")
	if err := os.WriteFile(helloPath, helloBuf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	sentinelPath = filepath.Join(home, "helper-ran")
	dir := filepath.Join(home, ".local", "share", brand.Name, "bin", ver)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	helperPath = filepath.Join(dir, brand.HostBinary)
	script := fmt.Sprintf("#!/bin/sh\n: > %q\ncat %q\ncat >/dev/null\n", sentinelPath, helloPath)
	if err := os.WriteFile(helperPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	return helperPath, sentinelPath, hex.EncodeToString(sum[:])
}

func registerShellHost(t *testing.T, tr client.Transport) client.Host {
	t.Helper()
	return register(t, shellLifecycle{tr: tr})
}

func TestDialChecksHelperSHA256BeforeBridge(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("POSIX shell digest check runs on Unix hosts")
	}
	home := t.TempDir()
	ver := client.HelperVersion()
	helperPath, sentinelPath, goodDigest := writeFakeHelper(t, home, ver)
	defer client.SetHelperDigests(func() []helpers.HelperDigest {
		return []helpers.HelperDigest{{OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: goodDigest}}
	})()

	tr := &shellTransport{home: home}
	h := registerShellHost(t, tr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Matching digest starts the bridge cleanly.
	c, err := client.Dial(ctx, h, false)
	if err != nil {
		t.Fatalf("Dial with matching digest: %v", err)
	}
	c.Close()
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Fatalf("expected helper to run when digest matched: %v", err)
	}

	// 2. Flipping 1 byte stops Dial with digest_mismatch and NEVER runs the helper.
	if err := os.Remove(sentinelPath); err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append(append([]byte(nil), orig...), '\n')
	if err := os.WriteFile(helperPath, tampered, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = client.Dial(ctx, h, false)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "digest_mismatch" || ue.Kind != client.KindFailed {
		t.Fatalf("Dial with tampered helper = %v, want UserError{Code: digest_mismatch, Kind: KindFailed}", err)
	}
	if !strings.Contains(ue.Hint, brand.Name+" host upgrade "+h.Name) {
		t.Fatalf("digest_mismatch hint = %q, want host upgrade hint", ue.Hint)
	}
	if _, statErr := os.Stat(sentinelPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("tampered helper executed! stat(%s) = %v", sentinelPath, statErr)
	}

	// 3. Even on a saved host, digest_mismatch stops instead of auto-reinstalling.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	if err := client.SaveHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	_, err = client.Dial(ctx, h, false)
	if !errors.As(err, &ue) || ue.Code != "digest_mismatch" {
		t.Fatalf("Dial on saved host with tampered helper = %v, want digest_mismatch", err)
	}
	if _, statErr := os.Stat(sentinelPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("tampered helper executed on saved host! stat(%s) = %v", sentinelPath, statErr)
	}

	// 4. Replacing the helper with a directory fails closed with digest_mismatch.
	if err := os.Remove(helperPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(helperPath, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = client.Dial(ctx, h, false)
	if !errors.As(err, &ue) || ue.Code != "digest_mismatch" {
		t.Fatalf("Dial with directory at helper path = %v, want digest_mismatch", err)
	}
	if err := os.Remove(helperPath); err != nil {
		t.Fatal(err)
	}

	// 5. A missing helper still triggers ErrNotInstalled (helper_missing on unsaved host).
	unsaved := registerShellHost(t, tr)
	unsaved.Name = "unsaved-box"
	_, err = client.Dial(ctx, unsaved, false)
	if !errors.As(err, &ue) || ue.Code != "helper_missing" {
		t.Fatalf("Dial with missing helper = %v, want helper_missing", err)
	}
}

func TestDialFailsClosedOnMissingPlatformDigestAndMissingHashTool(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("POSIX shell digest check runs on Unix hosts")
	}
	home := t.TempDir()
	ver := client.HelperVersion()
	_, sentinelPath, goodDigest := writeFakeHelper(t, home, ver)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Digests list does not include this host's platform -> fails closed.
	otherOS := "darwin"
	if runtime.GOOS == "darwin" {
		otherOS = "linux"
	}
	restore := client.SetHelperDigests(func() []helpers.HelperDigest {
		return []helpers.HelperDigest{{OS: otherOS, Arch: "arm64", SHA256: goodDigest}}
	})
	h := registerShellHost(t, &shellTransport{home: home})
	_, err := client.Dial(ctx, h, false)
	restore()
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "digest_mismatch" {
		t.Fatalf("Dial with unmatched platform digest = %v, want digest_mismatch", err)
	}
	if _, statErr := os.Stat(sentinelPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("helper executed without platform digest: %v", statErr)
	}

	// 2. Neither sha256sum nor shasum on PATH -> fails closed with coreutils hint.
	unamePath, err := exec.LookPath("uname")
	if err != nil {
		t.Skip("uname not on PATH")
	}
	emptyBin := t.TempDir()
	if err := os.Symlink(unamePath, filepath.Join(emptyBin, "uname")); err != nil {
		t.Fatal(err)
	}
	defer client.SetHelperDigests(func() []helpers.HelperDigest {
		return []helpers.HelperDigest{{OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: goodDigest}}
	})()
	noToolHost := registerShellHost(t, &shellTransport{home: home, path: emptyBin})
	_, err = client.Dial(ctx, noToolHost, false)
	if !errors.As(err, &ue) || ue.Code != "digest_mismatch" || !strings.Contains(ue.Hint, "coreutils") {
		t.Fatalf("Dial without sha256sum/shasum = %v, want digest_mismatch with coreutils hint", err)
	}
	if _, statErr := os.Stat(sentinelPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("helper executed without hash tool: %v", statErr)
	}

	// 3. Fallback to shasum -a 256 when sha256sum is absent.
	sha256sumPath, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("sha256sum not on PATH")
	}
	shasumBin := t.TempDir()
	if err := os.Symlink(unamePath, filepath.Join(shasumBin, "uname")); err != nil {
		t.Fatal(err)
	}
	catPath, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not on PATH")
	}
	if err := os.Symlink(catPath, filepath.Join(shasumBin, "cat")); err != nil {
		t.Fatal(err)
	}
	// Minimal shasum shim delegating `-a 256 -c -` to real sha256sum.
	shasumShim := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"-a\" ] && [ \"$2\" = \"256\" ]; then shift 2; exec %q \"$@\"; fi\nexit 2\n", sha256sumPath)
	if err := os.WriteFile(filepath.Join(shasumBin, "shasum"), []byte(shasumShim), 0o755); err != nil {
		t.Fatal(err)
	}
	shasumHost := registerShellHost(t, &shellTransport{home: home, path: shasumBin})
	c, err := client.Dial(ctx, shasumHost, false)
	if err != nil {
		t.Fatalf("Dial via shasum fallback: %v", err)
	}
	c.Close()
}

func TestProbeChecksHelperSHA256(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("POSIX shell digest check runs on Unix hosts")
	}
	home := t.TempDir()
	ver := client.HelperVersion()
	tr := &shellTransport{home: home}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	defer client.SetEnsureHelpers(func(context.Context) error { return nil })()

	// 1. Missing helper -> Installed: false, DigestMismatch: false.
	helperPath, _, goodDigest := writeFakeHelper(t, home, ver)
	if err := os.Remove(helperPath); err != nil {
		t.Fatal(err)
	}
	defer client.SetHelperDigests(func() []helpers.HelperDigest {
		return []helpers.HelperDigest{{OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: goodDigest}}
	})()

	p, err := client.Probe(ctx, tr)
	if err != nil {
		t.Fatalf("Probe missing: %v", err)
	}
	if p.Installed || p.DigestMismatch || p.OS != runtime.GOOS || p.Arch != runtime.GOARCH {
		t.Fatalf("Probe missing = %+v, want Installed=false DigestMismatch=false %s/%s", p, runtime.GOOS, runtime.GOARCH)
	}

	// 2. Matching helper -> Installed: true, DigestMismatch: false.
	helperPath, _, goodDigest = writeFakeHelper(t, home, ver)
	p, err = client.Probe(ctx, tr)
	if err != nil {
		t.Fatalf("Probe valid: %v", err)
	}
	if !p.Installed || p.DigestMismatch {
		t.Fatalf("Probe valid = %+v, want Installed=true DigestMismatch=false", p)
	}

	// 3. Tampered helper -> Installed: false, DigestMismatch: true.
	orig, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helperPath, append(orig, 'x'), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err = client.Probe(ctx, tr)
	if err != nil {
		t.Fatalf("Probe tampered: %v", err)
	}
	if p.Installed || !p.DigestMismatch {
		t.Fatalf("Probe tampered = %+v, want Installed=false DigestMismatch=true", p)
	}

	// 4. Missing hash tools -> Probe fails closed with digest_mismatch.
	unamePath, err := exec.LookPath("uname")
	if err != nil {
		t.Skip("uname not on PATH")
	}
	emptyBin := t.TempDir()
	if err := os.Symlink(unamePath, filepath.Join(emptyBin, "uname")); err != nil {
		t.Fatal(err)
	}
	_, err = client.Probe(ctx, &shellTransport{home: home, path: emptyBin})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "digest_mismatch" || !strings.Contains(ue.Hint, "coreutils") {
		t.Fatalf("Probe without sha256sum/shasum = %v, want digest_mismatch with coreutils hint", err)
	}
}

func TestHelperDigestCheckLatency(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("POSIX shell digest check runs on Unix hosts")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not on PATH")
	}
	// Measure local sha256sum -c latency on a realistic 5 MiB binary (spec 14.9.2: <= 25 ms at p50).
	blob := bytes.Repeat([]byte(brand.Name+"-helper-sample-bytes-0123"), 160*1024) // ~5 MiB
	f := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(f, blob, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blob)
	want := hex.EncodeToString(sum[:])

	const runs = 9
	samples := make([]time.Duration, 0, runs)
	for range runs {
		start := time.Now()
		cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf(`printf '%%s  %%s\n' %q %q | sha256sum -c - >/dev/null 2>&1`, want, f))
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, time.Since(start))
	}
	slices.Sort(samples)
	p50 := samples[len(samples)/2]
	t.Logf("sha256sum -c p50 on %d MiB = %s (all: %v)", len(blob)/(1<<20), p50, samples)
}
