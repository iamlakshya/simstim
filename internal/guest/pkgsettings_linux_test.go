//go:build linux

package guest

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func socketpairStream() ([2]int, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return [2]int{}, err
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	return fds, nil
}

// TestPackageSettingsGoEnvInBwrapSandbox runs the real `go env` command inside
// a bubblewrap sandbox with ApplyPackageSettings + ProxyEnv applied and checks
// spec 7.10's Go acceptance criteria plus guest-home pnpm and Bun files.
func TestPackageSettingsGoEnvInBwrapSandbox(t *testing.T) {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap not installed")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not installed")
	}

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer probeCancel()
	if out, err := exec.CommandContext(probeCtx, bwrapPath, "--unshare-user", "--unshare-pid", "--unshare-net", "--ro-bind", "/", "/", "--", "/bin/true").CombinedOutput(); err != nil {
		t.Skipf("bwrap sandbox unavailable here: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	runInBwrap := func(t *testing.T, level, age string, userEnv map[string]string, shellCmd string) string {
		t.Helper()
		home := t.TempDir()
		base := []string{
			"HOME=/home/agent",
			"USER=agent",
			"PATH=/usr/local/bin:/usr/bin:/bin:" + filepath.Dir(goPath),
			"GOFLAGS=-mod=vendor",
			"GOPRIVATE=host-leak.example/*",
		}
		for k, v := range ProxyEnv("http://127.0.0.1:3128", agents.Agent{}) {
			base = append(base, k+"="+v)
		}
		env, err := ApplyPackageSettings(home, base, userEnv, PackageSettingsConfig{
			Level:         level,
			MinReleaseAge: age,
			Now:           time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("ApplyPackageSettings: %v", err)
		}

		args := []string{
			"--unshare-user",
			"--unshare-pid",
			"--unshare-ipc",
			"--unshare-uts",
			"--unshare-net",
			"--uid", "1000",
			"--gid", "1000",
			"--cap-drop", "ALL",
			"--die-with-parent",
			"--proc", "/proc",
			"--dev", "/dev",
			"--ro-bind", "/usr", "/usr",
			"--ro-bind", "/etc", "/etc",
			"--tmpfs", "/tmp",
			"--bind", home, "/home/agent",
			"--chdir", "/home/agent",
			"--clearenv",
		}
		for _, dir := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
			if fi, serr := os.Lstat(dir); serr == nil {
				if fi.Mode()&os.ModeSymlink != 0 {
					if tgt, rerr := os.Readlink(dir); rerr == nil {
						args = append(args, "--symlink", tgt, dir)
					}
				} else if fi.IsDir() {
					args = append(args, "--ro-bind", dir, dir)
				}
			}
		}
		goDir := filepath.Dir(filepath.Dir(goPath))
		if !strings.HasPrefix(goPath, "/usr/") {
			args = append(args, "--ro-bind", goDir, goDir)
		}
		for _, kv := range env {
			if k, v, ok := strings.Cut(kv, "="); ok {
				args = append(args, "--setenv", k, v)
			}
		}
		args = append(args, "--", "/bin/sh", "-c", shellCmd)

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bwrapPath, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("bwrap failed: %v\n%s", err, string(out))
		}
		return string(out)
	}

	// 1. Guarded 7d: go env values match spec 7.10 and GOFLAGS is empty; home files exist.
	out := runInBwrap(t, api.PackageLevelGuarded, "7d", nil, `
set -eu
echo "GOTOOLCHAIN=$(go env GOTOOLCHAIN)"
echo "GOPROXY=$(go env GOPROXY)"
echo "GOSUMDB=$(go env GOSUMDB)"
echo "GOVCS=$(go env GOVCS)"
echo "GOFLAGS=$(go env GOFLAGS)"
echo "GOPRIVATE=$(go env GOPRIVATE)"
echo "NPM_AGE=${npm_config_min_release_age}"
echo "NPM_SCRIPTS=${npm_config_ignore_scripts}"
echo "NPM_PROXY=${npm_config_https_proxy}"
echo "CARGO_PROXY=${CARGO_HTTP_PROXY}"
echo "YARN_AGE=${YARN_NPM_MINIMAL_AGE_GATE}"
echo "YARN_SCRIPTS=${YARN_ENABLE_SCRIPTS}"
echo "PIP_AGE=${PIP_UPLOADED_PRIOR_TO}"
echo "UV_AGE=${UV_EXCLUDE_NEWER}"
echo "PNPM_FILE=$(cat /home/agent/.config/pnpm/config.yaml | tr '\n' '|')"
echo "BUN_FILE=$(cat /home/agent/.bunfig.toml | tr '\n' '|')"
`)
	for _, want := range []string{
		"GOTOOLCHAIN=local",
		"GOPROXY=https://proxy.golang.org",
		"GOSUMDB=sum.golang.org",
		"GOVCS=*:off",
		"GOFLAGS=\n",
		"GOPRIVATE=\n",
		"NPM_AGE=7",
		"NPM_SCRIPTS=true",
		"NPM_PROXY=http://127.0.0.1:3128",
		"CARGO_PROXY=http://127.0.0.1:3128",
		"YARN_AGE=10080",
		"YARN_SCRIPTS=false",
		"PIP_AGE=2026-11-21T12:00:00Z",
		"UV_AGE=2026-11-21T12:00:00Z",
		"PNPM_FILE=ignoreScripts: true|minimumReleaseAge: 10080|",
		"BUN_FILE=[install]|ignoreScripts = true|minimumReleaseAge = 604800|",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guarded sandbox output missing %q:\n%s", want, out)
		}
	}

	// 2. sandbox.env overrides win inside the sandbox.
	overrideOut := runInBwrap(t, api.PackageLevelStrict, "7d", map[string]string{
		"GOPROXY":                   "https://corp.example/go",
		"GOPRIVATE":                 "corp.example/*",
		"npm_config_ignore_scripts": "false",
	}, `
set -eu
echo "GOPROXY=$(go env GOPROXY)"
echo "GOPRIVATE=$(go env GOPRIVATE)"
echo "NPM_SCRIPTS=${npm_config_ignore_scripts}"
echo "PIP_BIN=${PIP_ONLY_BINARY}"
echo "UV_NB=${UV_NO_BUILD}"
`)
	for _, want := range []string{
		"GOPROXY=https://corp.example/go",
		"GOPRIVATE=corp.example/*",
		"NPM_SCRIPTS=false",
		"PIP_BIN=:all:",
		"UV_NB=1",
	} {
		if !strings.Contains(overrideOut, want) {
			t.Errorf("strict+override sandbox output missing %q:\n%s", want, overrideOut)
		}
	}

	// 3. off level writes no home files and no guard variables, while keeping proxy variables.
	offOut := runInBwrap(t, api.PackageLevelOff, "7d", nil, `
set -eu
test ! -e /home/agent/.config/pnpm/config.yaml
test ! -e /home/agent/.bunfig.toml
echo "NPM_SCRIPTS=${npm_config_ignore_scripts:-unset}"
echo "HTTPS_PROXY=${HTTPS_PROXY}"
echo "CARGO_PROXY=${CARGO_HTTP_PROXY}"
echo "OFF_OK"
`)
	for _, want := range []string{
		"NPM_SCRIPTS=unset",
		"HTTPS_PROXY=http://127.0.0.1:3128",
		"CARGO_PROXY=http://127.0.0.1:3128",
		"OFF_OK",
	} {
		if !strings.Contains(offOut, want) {
			t.Errorf("off sandbox output missing %q:\n%s", want, offOut)
		}
	}
}

// TestGuestInitAppliesPackageSettingsEndToEnd proves wall-init (RunConn)
// applies package manager settings when handling a ctl "start" request.
func TestGuestInitAppliesPackageSettingsEndToEnd(t *testing.T) {
	// Build a temporary host helper so wall-init can spawn `<helper> holder`.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "helper")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", bin, "../../cmd/host")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/host: %v\n%s", err, string(out))
	}

	// Run `<helper> wall-init` over a real socketpair so os.Executable() inside
	// wall-init resolves to the helper binary.
	fds, err := socketpairStream()
	if err != nil {
		t.Fatal(err)
	}
	supFile := os.NewFile(uintptr(fds[0]), "sup")
	guestFile := os.NewFile(uintptr(fds[1]), "guest")

	runDir := filepath.Join(t.TempDir(), "run")
	guestHome := filepath.Join(t.TempDir(), "home")
	workDir := filepath.Join(t.TempDir(), "work")
	for _, d := range []string{runDir, guestHome, workDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.CommandContext(ctx, bin, "wall-init")
	cmd.ExtraFiles = []*os.File{guestFile}
	cmd.Env = append(os.Environ(), brand.Env("GUEST_RUN_DIR")+"="+runDir)
	if err := cmd.Start(); err != nil {
		_ = supFile.Close()
		_ = guestFile.Close()
		t.Fatalf("start wall-init: %v", err)
	}
	_ = guestFile.Close()
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	conn, err := net.FileConn(supFile)
	_ = supFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	sess := link.New(conn, link.Supervisor, link.Options{})
	defer func() { _ = sess.Close() }()

	ctl, err := sess.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatalf("open ctl: %v", err)
	}
	w := proto.NewWriter(ctl)
	r := bufio.NewReader(ctl)

	outFile := filepath.Join(workDir, "env.txt")
	argsBytes, _ := json.Marshal(StartReq{
		Session:         "pkginit1",
		Name:            "pkg",
		Agent:           "shell",
		Argv:            []string{"/bin/sh", "-c", "env > " + outFile + "; sleep 5"},
		Env:             []string{"HOME=" + guestHome, "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=xterm-256color"},
		Cwd:             workDir,
		Term:            "xterm-256color",
		Cols:            80,
		Rows:            24,
		PackageManagers: api.PackageLevelStrict,
		MinReleaseAge:   "7d",
		UserEnv:         []string{"npm_config_ignore_scripts=false"},
	})
	if err := w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "start", Args: argsBytes}); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := proto.Read(r)
		if err != nil {
			t.Fatalf("read start response: %v", err)
		}
		if f.Type != proto.TResponse {
			continue
		}
		var resp proto.Response
		if err := json.Unmarshal(f.Payload, &resp); err != nil {
			t.Fatal(err)
		}
		if !resp.OK {
			t.Fatalf("start failed: %+v", resp.Error)
		}
		break
	}

	// Wait for the agent shell to write env.txt.
	var envText string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, rerr := os.ReadFile(outFile)
		if rerr == nil && strings.Contains(string(b), "PIP_ONLY_BINARY=") {
			envText = string(b)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, want := range []string{
		"npm_config_ignore_scripts=false",
		"npm_config_min_release_age=7",
		"YARN_ENABLE_SCRIPTS=false",
		"YARN_NPM_MINIMAL_AGE_GATE=10080",
		"PIP_ONLY_BINARY=:all:",
		"UV_NO_BUILD=1",
		"GOTOOLCHAIN=local",
		"GOPROXY=https://proxy.golang.org",
		"GOSUMDB=sum.golang.org",
		"GOVCS=*:off",
	} {
		if !strings.Contains(envText, want) {
			t.Errorf("agent env missing %q:\n%s", want, envText)
		}
	}
	if strings.Contains(envText, "\nGOFLAGS=") {
		t.Errorf("agent env unexpectedly set GOFLAGS:\n%s", envText)
	}
	pnpmBytes, err := os.ReadFile(filepath.Join(guestHome, ".config", "pnpm", "config.yaml"))
	if err != nil || !strings.Contains(string(pnpmBytes), "minimumReleaseAge: 10080") {
		t.Errorf("pnpm config.yaml = %q, err=%v", string(pnpmBytes), err)
	}
	bunBytes, err := os.ReadFile(filepath.Join(guestHome, ".bunfig.toml"))
	if err != nil || !strings.Contains(string(bunBytes), "minimumReleaseAge = 604800") {
		t.Errorf(".bunfig.toml = %q, err=%v", string(bunBytes), err)
	}

	stopArgs, _ := json.Marshal(StopReq{GraceMS: 1000})
	_ = w.WriteJSON(proto.TRequest, proto.Request{ID: 2, Op: "stop", Args: stopArgs})
}

// TestLiveHostAPackageManagersInBwrap is an opt-in live proof run on host-a
// (with <BRAND>_LIVE_HOST_TEST=1). It runs bwrap --unshare-user --unshare-pid
// --unshare-net with a guest loopback proxy bridged to egress (default balanced
// policy) and verifies package manager env vars, home files, overrides, and
// registry CONNECT routing for npm, pip, cargo, and go.
func TestLiveHostAPackageManagersInBwrap(t *testing.T) {
	if os.Getenv(brand.Env("LIVE_HOST_TEST")) != "1" {
		t.Skip("set " + brand.Env("LIVE_HOST_TEST") + "=1 to run the live bwrap package-manager proof")
	}
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		t.Fatalf("bwrap missing: %v", err)
	}
	pyPath, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 missing: %v", err)
	}

	registries := []string{"registry.npmjs.org", "pypi.org", "proxy.golang.org", "crates.io"}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9),
		Subject:      pkix.Name{CommonName: "live-reg"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     append(append([]string(nil), registries...), "evil.example"),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "reg-ok:"+r.Host)
	}))
	up.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	up.StartTLS()
	defer up.Close()

	ap, err := egress.NewAddressPolicyWithHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	fakePub := netip.AddrFrom4([4]byte{93, 184, 216, 34})
	res := staticResolver{"evil.example": {fakePub}}
	for _, h := range registries {
		res[h] = []netip.Addr{fakePub}
	}
	sd := &egress.SafeDialer{
		Addrs:    ap,
		Resolver: res,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, up.Listener.Addr().String())
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	supConn, egConn := net.Pipe()
	egDone := make(chan error, 1)
	go func() {
		egDone <- egress.RunOnConn(ctx, "live-host-a", egConn, &egress.ServerOptions{Addrs: ap, Dialer: sd})
	}()
	cli, err := egress.NewClient(ctx, supConn, egress.StartArgs{Session: "host-a/pkg1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = cli.Close()
		<-egDone
	}()

	gp, err := ListenProxy(ctx, cli, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go gp.Serve(ctx)
	defer func() { _ = gp.Close() }()

	// Bridge the proxy over a Unix domain socket mounted inside bwrap's isolated netns.
	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "proxy.sock")
	unixLn, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unixLn.Close() }()
	go func() {
		for {
			uc, uerr := unixLn.Accept()
			if uerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				pc, perr := net.DialTimeout("tcp", gp.Addr().String(), 2*time.Second)
				if perr != nil {
					return
				}
				defer func() { _ = pc.Close() }()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(pc, c); close(done) }()
				_, _ = io.Copy(c, pc)
				<-done
			}(uc)
		}
	}()

	home := t.TempDir()
	base := []string{"HOME=/home/agent", "USER=agent", "PATH=/usr/local/bin:/usr/bin:/bin"}
	for k, v := range ProxyEnv("http://127.0.0.1:3128", agents.Agent{}) {
		base = append(base, k+"="+v)
	}
	env, err := ApplyPackageSettings(home, base, nil, PackageSettingsConfig{
		Level:         api.PackageLevelStrict,
		MinReleaseAge: "7d",
		Now:           time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	script := `
import os, socket, sys

for k, want in [
    ("npm_config_min_release_age", "7"),
    ("npm_config_ignore_scripts", "true"),
    ("npm_config_https_proxy", "http://127.0.0.1:3128"),
    ("CARGO_HTTP_PROXY", "http://127.0.0.1:3128"),
    ("YARN_NPM_MINIMAL_AGE_GATE", "10080"),
    ("YARN_ENABLE_SCRIPTS", "false"),
    ("PIP_UPLOADED_PRIOR_TO", "2026-11-21T12:00:00Z"),
    ("PIP_ONLY_BINARY", ":all:"),
    ("UV_EXCLUDE_NEWER", "2026-11-21T12:00:00Z"),
    ("UV_NO_BUILD", "1"),
    ("GOTOOLCHAIN", "local"),
    ("GOPROXY", "https://proxy.golang.org"),
    ("GOSUMDB", "sum.golang.org"),
    ("GOVCS", "*:off"),
]:
    got = os.environ.get(k)
    assert got == want, f"{k}={got!r}, want {want!r}"
assert "GOFLAGS" not in os.environ, "GOFLAGS must be absent"

with open("/home/agent/.config/pnpm/config.yaml") as f:
    assert "minimumReleaseAge: 10080" in f.read()
with open("/home/agent/.bunfig.toml") as f:
    assert "minimumReleaseAge = 604800" in f.read()

def proxy_connect(host):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(5)
    s.connect("/run/proxy/proxy.sock")
    s.sendall(f"CONNECT {host}:443 HTTP/1.1\r\nHost: {host}:443\r\n\r\n".encode())
    line = s.recv(256).decode("utf-8", "replace").splitlines()[0]
    s.close()
    return line

for reg in ("registry.npmjs.org", "pypi.org", "proxy.golang.org", "crates.io"):
    line = proxy_connect(reg)
    assert " 200 " in line, f"{reg}: got {line!r}"
evil = proxy_connect("evil.example")
assert " 403 " in evil, f"evil.example: got {evil!r}"
print("HOST_A_PKG_OK")
`
	args := []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-net",
		"--uid", "1000", "--gid", "1000", "--cap-drop", "ALL", "--die-with-parent",
		"--proc", "/proc", "--dev", "/dev",
		"--ro-bind", "/usr", "/usr", "--ro-bind", "/etc", "/etc",
		"--tmpfs", "/tmp",
		"--bind", home, "/home/agent",
		"--bind", sockDir, "/run/proxy",
		"--clearenv",
	}
	for _, dir := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		if fi, serr := os.Lstat(dir); serr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				if tgt, rerr := os.Readlink(dir); rerr == nil {
					args = append(args, "--symlink", tgt, dir)
				}
			} else if fi.IsDir() {
				args = append(args, "--ro-bind", dir, dir)
			}
		}
	}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			args = append(args, "--setenv", k, v)
		}
	}
	args = append(args, "--", pyPath, "-c", script)

	out, err := exec.CommandContext(ctx, bwrapPath, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("live host-a bwrap proof failed: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "HOST_A_PKG_OK") {
		t.Fatalf("missing HOST_A_PKG_OK:\n%s", string(out))
	}
}
