package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func runtimeTestSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type localHostBridge struct {
	dataDir string
	home    string
}

func (l *localHostBridge) Start(ctx context.Context, _ string) (Proc, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	//nolint:contextcheck // host.RunBridge runs in-process over io.Pipe without a request context
	go func() {
		err := host.RunBridge(inR, outW)
		_ = outW.Close()
		done <- err
	}()
	go func() {
		<-ctx.Done()
		_ = inW.Close()
		_ = outR.Close()
	}()
	return &pipeBridgeProc{stdin: inW, stdout: outR, done: done}, nil
}

func (l *localHostBridge) Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
	env := slices.Clone(os.Environ())
	env = append(env, "HOME="+l.home, "XDG_DATA_HOME="+l.dataDir)
	cmd.Env = env
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), errors.New(strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (l *localHostBridge) ReachHint() string { return "check local test bridge" }

type pipeBridgeProc struct {
	stdin  io.WriteCloser
	stdout io.Reader
	done   chan error
}

func (p *pipeBridgeProc) Stdin() io.WriteCloser { return p.stdin }
func (p *pipeBridgeProc) Stdout() io.Reader     { return p.stdout }
func (p *pipeBridgeProc) Stderr() string        { return "" }
func (p *pipeBridgeProc) Kill()                 { _ = p.stdin.Close() }
func (p *pipeBridgeProc) Wait() error           { return <-p.done }

func dialLocalBridge(t *testing.T) (*Conn, *localHostBridge) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	data := filepath.Join(dir, "data")
	state := filepath.Join(dir, "state")
	cfg := filepath.Join(dir, "config")
	cache := filepath.Join(dir, "cache")
	for _, d := range []string{home, data, state, cfg, cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", cache)

	tr := &localHostBridge{dataDir: data, home: home}
	h := Host{Name: "box", SSH: "box"}
	c, err := dial(context.Background(), h, route{t: tr})
	if err != nil {
		t.Fatalf("dial local bridge: %v", err)
	}
	t.Cleanup(c.Close)
	return c, tr
}

func TestEnsureRuntimeLaptopModeAndCacheReuse(t *testing.T) {
	conn, tr := dialLocalBridge(t)
	oldAllow := allowInsecureClientRuntimeURL
	allowInsecureClientRuntimeURL = true
	t.Cleanup(func() { allowInsecureClientRuntimeURL = oldAllow })

	var hits atomic.Int32
	vmmBytes := bytes.Repeat([]byte("V"), 4096)
	kernBytes := bytes.Repeat([]byte("K"), 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/vmm":
			_, _ = w.Write(vmmBytes)
		case "/kernel":
			_, _ = w.Write(kernBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	manifest := api.RuntimeManifest{
		Release: "0.9.0",
		Runtime: "2026.11.0",
		Items: []api.RuntimeAsset{
			{Kind: "vmm", Name: "cloud-hypervisor", Arch: "amd64", SHA256: runtimeTestSHA(vmmBytes), Size: int64(len(vmmBytes)), URL: srv.URL + "/vmm"},
			{Kind: "kernel", Name: "guest", Arch: "amd64", SHA256: runtimeTestSHA(kernBytes), Size: int64(len(kernBytes)), URL: srv.URL + "/kernel"},
			{Kind: "kernel", Name: "guest-arm", Arch: "arm64", SHA256: strings.Repeat("a", 64), Size: 100, URL: srv.URL + "/unused"},
		},
	}

	var out bytes.Buffer
	res, err := EnsureRuntime(context.Background(), conn, EnsureRuntimeOptions{
		Mode:       api.ImagesFetchLaptop,
		Manifest:   manifest,
		Arch:       "amd64",
		Transport:  tr,
		HTTPClient: srv.Client(),
		Out:        &out,
	})
	if err != nil {
		t.Fatalf("EnsureRuntime laptop: %v", err)
	}
	if res.Mode != api.ImagesFetchLaptop || len(res.Installed) != 2 || hits.Load() != 2 {
		t.Fatalf("res=%+v hits=%d", res, hits.Load())
	}
	if !strings.Contains(out.String(), "cloud-hypervisor") || !strings.Contains(out.String(), "sha256 ok") {
		t.Errorf("progress output missing expected fields:\n%s", out.String())
	}

	// Verify host store modes and laptop cache presence.
	if fi, err := os.Stat(paths.ArtefactFile(runtimeTestSHA(vmmBytes))); err != nil || fi.Mode().Perm() != 0o555 {
		t.Fatalf("vmm mode = %v, err=%v", fi, err)
	}
	if fi, err := os.Stat(paths.ArtefactFile(runtimeTestSHA(kernBytes))); err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("kernel mode = %v, err=%v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(RuntimeCacheDir(), runtimeTestSHA(vmmBytes))); err != nil {
		t.Fatalf("laptop cache missing vmm: %v", err)
	}

	// Calling again skips everything (already present on host).
	res2, err := EnsureRuntime(context.Background(), conn, EnsureRuntimeOptions{
		Mode:       api.ImagesFetchLaptop,
		Manifest:   manifest,
		Arch:       "amd64",
		Transport:  tr,
		HTTPClient: srv.Client(),
	})
	if err != nil || len(res2.Installed) != 0 || res2.AlreadyPresent != 2 || hits.Load() != 2 {
		t.Fatalf("second run = %+v, err=%v, hits=%d", res2, err, hits.Load())
	}

	// Removing from host store re-uploads from laptop cache without hitting HTTP again.
	if err := os.Remove(paths.ArtefactFile(runtimeTestSHA(vmmBytes))); err != nil {
		t.Fatal(err)
	}
	res3, err := EnsureRuntime(context.Background(), conn, EnsureRuntimeOptions{
		Mode:       api.ImagesFetchLaptop,
		Manifest:   manifest,
		Arch:       "amd64",
		Transport:  tr,
		HTTPClient: srv.Client(),
	})
	if err != nil || len(res3.Installed) != 1 || res3.AlreadyPresent != 1 || hits.Load() != 2 {
		t.Fatalf("cache-backed re-upload = %+v, err=%v, hits=%d", res3, err, hits.Load())
	}
}

func TestEnsureRuntimeAutoFallsBackOnceToLaptopOnHostFailure(t *testing.T) {
	conn, tr := dialLocalBridge(t)
	oldAllow := allowInsecureClientRuntimeURL
	allowInsecureClientRuntimeURL = true
	t.Cleanup(func() { allowInsecureClientRuntimeURL = oldAllow })

	payload := bytes.Repeat([]byte("F"), 1024)
	// Host uses its own HTTPS-only client (so http:// fails on the host!),
	// while laptop uses srv.Client() with allowInsecureClientRuntimeURL=true.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	manifest := api.RuntimeManifest{
		Release: "0.9.0",
		Runtime: "2026.11.0",
		Items: []api.RuntimeAsset{{
			Kind: "kernel", Name: "guest", Arch: "amd64", SHA256: runtimeTestSHA(payload), Size: int64(len(payload)), URL: srv.URL + "/guest",
		}},
	}

	reachable := true
	var out bytes.Buffer
	res, err := EnsureRuntime(context.Background(), conn, EnsureRuntimeOptions{
		Mode:       api.ImagesFetchAuto,
		Manifest:   manifest,
		Arch:       "amd64",
		Probe:      &api.CapProbe{ReleaseReachable: &reachable},
		Transport:  tr,
		HTTPClient: srv.Client(),
		Out:        &out,
	})
	if err != nil {
		t.Fatalf("auto fallback failed: %v", err)
	}
	if !res.FellBack || res.Mode != api.ImagesFetchLaptop || len(res.Installed) != 1 {
		t.Fatalf("expected single fallback to laptop, got %+v", res)
	}
	if !strings.Contains(out.String(), "falling back to laptop copy") || !strings.Contains(out.String(), "sha256 ok") {
		t.Errorf("fallback notice missing:\n%s", out.String())
	}

	// Explicit "host" mode must NOT fall back when host download fails.
	if err := os.Remove(paths.ArtefactFile(runtimeTestSHA(payload))); err != nil {
		t.Fatal(err)
	}
	_, err = EnsureRuntime(context.Background(), conn, EnsureRuntimeOptions{
		Mode:       api.ImagesFetchHost,
		Manifest:   manifest,
		Arch:       "amd64",
		Transport:  tr,
		HTTPClient: srv.Client(),
	})
	if err == nil {
		t.Fatal("explicit host mode unexpectedly succeeded on failing host download")
	}
}

func TestEnsureRuntimeAutoUsesLaptopWhenReleaseUnreachable(t *testing.T) {
	conn, tr := dialLocalBridge(t)
	oldAllow := allowInsecureClientRuntimeURL
	allowInsecureClientRuntimeURL = true
	t.Cleanup(func() { allowInsecureClientRuntimeURL = oldAllow })

	payload := bytes.Repeat([]byte("U"), 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	unreachable := false
	var out bytes.Buffer
	res, err := EnsureRuntime(context.Background(), conn, EnsureRuntimeOptions{
		Mode: api.ImagesFetchAuto,
		Manifest: api.RuntimeManifest{
			Runtime: "2026.11.0",
			Items: []api.RuntimeAsset{{
				Kind: "tools", Name: "tools", Arch: "amd64", SHA256: runtimeTestSHA(payload), Size: int64(len(payload)), URL: srv.URL + "/tools",
			}},
		},
		Arch:       "amd64",
		Probe:      &api.CapProbe{ReleaseReachable: &unreachable},
		Transport:  tr,
		HTTPClient: srv.Client(),
		Out:        &out,
	})
	if err != nil {
		t.Fatalf("auto unreachable: %v", err)
	}
	if res.Mode != api.ImagesFetchLaptop || res.FellBack || len(res.Installed) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(out.String(), "not reachable") {
		t.Errorf("expected unreachable notice:\n%s", out.String())
	}
}

func TestEnsureRuntimeHostModeViaTLSAndStatusPolling(t *testing.T) {
	conn, tr := dialLocalBridge(t)
	payload := bytes.Repeat([]byte("H"), 16384)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	// Note: host.FetchRuntime uses its own TLS roots, so untrusted httptest TLS
	// server fails with certificate error — verify explicit host mode returns
	// that error cleanly (and real HTTPS is verified on host-a and host package tests).
	_, err := EnsureRuntime(context.Background(), conn, EnsureRuntimeOptions{
		Mode: api.ImagesFetchHost,
		Manifest: api.RuntimeManifest{
			Runtime: "2026.11.0",
			Items: []api.RuntimeAsset{{
				Kind: "kernel", Name: "guest", Arch: "amd64", SHA256: runtimeTestSHA(payload), Size: int64(len(payload)), URL: srv.URL + "/guest",
			}},
		},
		Arch:         "amd64",
		Transport:    tr,
		PollInterval: 50 * time.Millisecond,
	})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "runtime_fetch_failed" {
		t.Fatalf("untrusted TLS host fetch = %v, want runtime_fetch_failed", err)
	}
}

func TestEnsureLaptopCacheRejectsBadDigestAndOversize(t *testing.T) {
	oldAllow := allowInsecureClientRuntimeURL
	allowInsecureClientRuntimeURL = true
	t.Cleanup(func() { allowInsecureClientRuntimeURL = oldAllow })

	want := bytes.Repeat([]byte("X"), 512)
	wantHex := runtimeTestSHA(want)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad":
			_, _ = w.Write(bytes.Repeat([]byte("Y"), 512))
		case "/big":
			fl, _ := w.(http.Flusher)
			_, _ = w.Write(bytes.Repeat([]byte("Z"), 256))
			if fl != nil {
				fl.Flush()
			}
			_, _ = w.Write(bytes.Repeat([]byte("Z"), 400))
		}
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	_, err := ensureLaptopCachedRuntimeFile(context.Background(), srv.Client(), cacheDir, api.RuntimeAsset{
		Kind: "kernel", Name: "guest", SHA256: wantHex, Size: int64(len(want)), URL: srv.URL + "/bad",
	})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "digest_mismatch" {
		t.Fatalf("bad digest = %v, want digest_mismatch", err)
	}
	_, err = ensureLaptopCachedRuntimeFile(context.Background(), srv.Client(), cacheDir, api.RuntimeAsset{
		Kind: "kernel", Name: "guest", SHA256: wantHex, Size: int64(len(want)), URL: srv.URL + "/big",
	})
	if !errors.As(err, &ue) || ue.Code != "runtime_too_big" {
		t.Fatalf("oversize = %v, want runtime_too_big", err)
	}
	if entries, err := os.ReadDir(cacheDir); err != nil || len(entries) != 0 {
		t.Fatalf("cacheDir not clean after rejected downloads: %+v, err=%v", entries, err)
	}
}

func TestEnsureRuntimeHostModeSuccessAndStatusPolling(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)

	var polled atomic.Int32
	fetchStarted := make(chan struct{})
	pollSeen := make(chan struct{})
	var closePollSeen atomic.Bool

	payload := bytes.Repeat([]byte("M"), 32768)
	wantHex := runtimeTestSHA(payload)

	go func() {
		defer func() {
			_ = outW.Close()
			done <- nil
		}()
		r := bufio.NewReader(inR)
		w := proto.NewWriter(outW)
		if _, err := proto.Read(r); err != nil {
			return
		}
		_ = w.WriteJSON(proto.THello, proto.Hello{
			Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version,
			Role: "bridge", Features: []string{"sandbox", "runtime"},
		})
		for {
			f, err := proto.Read(r)
			if err != nil {
				return
			}
			if f.Type != proto.TRequest {
				continue
			}
			var req proto.Request
			if json.Unmarshal(f.Payload, &req) != nil {
				continue
			}
			go func(req proto.Request) {
				switch req.Op {
				case "runtime.status":
					st := api.RuntimeStatusResult{Files: map[string]int64{}}
					select {
					case <-fetchStarted:
						polled.Add(1)
						st.Active = []api.RuntimeTransfer{{
							SHA256: wantHex, Name: "cloud-hypervisor", Bytes: int64(len(payload) / 2), Size: int64(len(payload)),
						}}
						if closePollSeen.CompareAndSwap(false, true) {
							close(pollSeen)
						}
					default:
					}
					raw, _ := json.Marshal(st)
					_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: raw})
				case "runtime.fetch":
					close(fetchStarted)
					select {
					case <-pollSeen:
					case <-time.After(2 * time.Second):
					}
					res := api.RuntimeInstallResult{
						Runtime: "2026.11.0",
						Installed: []api.RuntimeInstalledFile{{
							Kind: "vmm", Name: "cloud-hypervisor", SHA256: wantHex,
							Size: int64(len(payload)), DurationMS: 42,
						}},
					}
					raw, _ := json.Marshal(res)
					_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: raw})
				}
			}(req)
		}
	}()

	tr := &fixedProcTransport{proc: &pipeBridgeProc{stdin: inW, stdout: outR, done: done}}
	conn, err := dial(context.Background(), Host{Name: "cloud", SSH: "cloud"}, route{t: tr})
	if err != nil {
		t.Fatalf("dial fake runtime bridge: %v", err)
	}
	defer conn.Close()

	reachable := true
	var out bytes.Buffer
	res, err := EnsureRuntime(context.Background(), conn, EnsureRuntimeOptions{
		Mode: api.ImagesFetchAuto,
		Manifest: api.RuntimeManifest{
			Release: "0.9.0",
			Runtime: "2026.11.0",
			Items: []api.RuntimeAsset{{
				Kind: "vmm", Name: "cloud-hypervisor", Arch: "amd64",
				SHA256: wantHex, Size: int64(len(payload)), URL: "https://example.invalid/vmm",
			}},
		},
		Arch:         "amd64",
		Probe:        &api.CapProbe{ReleaseReachable: &reachable},
		PollInterval: 20 * time.Millisecond,
		Out:          &out,
	})
	if err != nil {
		t.Fatalf("EnsureRuntime auto->host: %v", err)
	}
	if res.Mode != api.ImagesFetchHost || res.FellBack || len(res.Installed) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if polled.Load() < 1 {
		t.Errorf("expected at least 1 runtime.status poll during runtime.fetch, got %d", polled.Load())
	}
	if !strings.Contains(out.String(), "fetching cloud-hypervisor") || !strings.Contains(out.String(), "sha256 ok") {
		t.Errorf("expected polling and completion lines:\n%s", out.String())
	}
}

type fixedProcTransport struct {
	proc Proc
}

func (f *fixedProcTransport) Start(context.Context, string) (Proc, error) { return f.proc, nil }
func (f *fixedProcTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("not used")
}
func (f *fixedProcTransport) ReachHint() string { return "fake" }

func TestDefaultSettingsImagesFetch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := LoadSettings()
	if s.Images.Fetch != api.ImagesFetchAuto {
		t.Fatalf("default images.fetch = %q, want %q", s.Images.Fetch, api.ImagesFetchAuto)
	}
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SettingsPath(), []byte("[images]\nfetch = \"laptop\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, warn := LoadSettingsChecked()
	if warn != "" || s.Images.Fetch != api.ImagesFetchLaptop {
		t.Fatalf("loaded images.fetch = (%q, %q), want (laptop, empty)", s.Images.Fetch, warn)
	}
}

func TestEnsureRuntimeInvalidModeSingleHint(t *testing.T) {
	_, err := EnsureRuntime(context.Background(), nil, EnsureRuntimeOptions{Mode: "peer"})
	var ue *UserError
	if !errors.As(err, &ue) {
		t.Fatalf("EnsureRuntime invalid mode = %v, want *UserError", err)
	}
	if ue.Code != "bad_value" || ue.Kind != KindUsage {
		t.Fatalf("unexpected error metadata: %+v", ue)
	}
	if strings.Contains(ue.Msg, "Try:") {
		t.Fatalf("UserError.Msg must not embed a second Try: hint, got %q", ue.Msg)
	}
	if ue.Hint == "" {
		t.Fatal("UserError.Hint is empty")
	}
}
