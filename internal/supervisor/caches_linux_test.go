//go:build linux

package supervisor

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/sandbox"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func buildTestHostHelper(t *testing.T, ctx context.Context, tmp string) string {
	t.Helper()
	helperBin := filepath.Join(tmp, "bin", brand.HostBinary)
	if err := os.MkdirAll(filepath.Dir(helperBin), 0o700); err != nil {
		t.Fatal(err)
	}
	ldflags := "-X github.com/iamlakshya/" + brand.Name + "/internal/version.Version=" + version.Version
	buildCmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", helperBin, "../../cmd/host")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/host: %v\n%s", err, string(out))
	}
	return helperBin
}

func runProcessWallCycle(t *testing.T, id, helperBin string, m *host.Manifest, cmds string, expectContains []string, timeout time.Duration) string {
	t.Helper()
	m.Status = host.Status{State: host.StateStarting, Since: time.Now().UTC()}
	if err := m.Save(); err != nil {
		t.Fatalf("save manifest before cycle: %v", err)
	}
	sb := NewProcessSandbox(id, helperBin, m)
	supDone := make(chan error, 1)
	go func() {
		supDone <- RunWith(id, sb, Options{
			HeartbeatTimeout: 10 * time.Second,
			TickInterval:     200 * time.Millisecond,
			StopGrace:        3 * time.Second,
			EgressExe:        helperBin,
		})
	}()

	var conn net.Conn
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var derr error
		conn, derr = net.DialTimeout("unix", paths.Socket(id), time.Second)
		if derr == nil {
			if lm, lerr := host.LoadManifest(id); lerr == nil && lm.Status.State != host.StateStarting {
				break
			}
			_ = conn.Close()
			conn = nil
		}
		select {
		case err := <-supDone:
			t.Fatalf("supervisor exited early: %v", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("timed out waiting for supervisor socket")
	}

	w := proto.NewWriter(conn)
	rd := bufio.NewReaderSize(conn, 64<<10)
	if err := w.WriteJSON(proto.THello, proto.Hello{
		Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge",
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	if hf, err := proto.Read(rd); err != nil || hf.Type != proto.THello {
		t.Fatalf("read hello: frame=%v err=%v", hf.Type, err)
	}
	if err := w.WriteJSON(proto.TAttach, proto.Attach{Session: id, Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("send attach: %v", err)
	}
	if af, err := proto.Read(rd); err != nil || af.Type != proto.TAttached {
		t.Fatalf("read attached: type=0x%x payload=%q err=%v", af.Type, string(af.Payload), err)
	}
	if err := w.Write(proto.TInput, []byte(cmds)); err != nil {
		t.Fatalf("write input: %v", err)
	}

	var out strings.Builder
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			fr, rerr := proto.Read(rd)
			if rerr != nil {
				return
			}
			if fr.Type == proto.TOutput {
				if len(fr.Payload) > 8 {
					out.Write(fr.Payload[8:])
				}
				cur := out.String()
				allFound := true
				for _, want := range expectContains {
					if !strings.Contains(cur, want) {
						allFound = false
						break
					}
				}
				if allFound {
					return
				}
			}
		}
	}()

	select {
	case <-readDone:
	case <-time.After(timeout):
		_ = conn.Close()
		t.Fatalf("timed out waiting for %v; got:\n%s", expectContains, out.String())
	}
	_ = conn.Close()

	if _, err := host.HolderRequest(id, "kill", nil, 5*time.Second); err != nil {
		t.Fatalf("HolderRequest kill: %v", err)
	}
	select {
	case err := <-supDone:
		if err != nil {
			t.Fatalf("supervisor returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not exit after kill")
	}
	return out.String()
}

func TestSupervisorProcessWallKeptCachesIsolationAndDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	probe := process.Driver{}.Probe(ctx)
	if !probe.OK {
		t.Skipf("process wall unavailable on this host: %s (%s)", probe.Reason, probe.Fix)
	}

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	helperBin := buildTestHostHelper(t, ctx, tmp)
	newManifest := func(id string, cacheDirs []string) *host.Manifest {
		ws := filepath.Join(tmp, "ws-"+id)
		if err := os.MkdirAll(ws, 0o700); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		return &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      "cache-" + id,
			CreatedAt: now,
			Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
			Cwd:       "/work/ws-" + id,
			Env: []string{
				"HOME=" + process.GuestHome,
				"USER=agent",
				"LOGNAME=agent",
				"LANG=C.UTF-8",
				"PATH=/usr/local/bin:/usr/bin:/bin",
				"TERM=xterm-256color",
				"HTTP_PROXY=" + process.GuestProxyURL,
				"HTTPS_PROXY=" + process.GuestProxyURL,
				"NO_PROXY=localhost,127.0.0.1,::1",
			},
			Term:   host.TermInfo{Term: "xterm-256color", Cols: 120, Rows: 40},
			Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
			Status: host.Status{State: host.StateStarting, Since: now},
			Wall: &host.WallRecord{
				Level:          "process",
				Driver:         process.DriverName,
				Version:        probe.Version,
				Preset:         string(egress.PresetLocked),
				WorkspaceHost:  ws,
				WorkspaceGuest: "/work/ws-" + id,
				StateDirs:      []string{".claude/projects"},
				CacheDirs:      cacheDirs,
				At:             now,
			},
		}
	}

	c1ID, c2ID, c3ID := "ca234567", "cb234567", "cc234567"
	m1 := newManifest(c1ID, sandbox.DefaultCachePaths())
	m2 := newManifest(c2ID, sandbox.DefaultCachePaths())
	m3 := newManifest(c3ID, nil) // keep_caches = false

	// Sandbox 1 Cycle 1: populate caches (including read-only Go mod dir) + write .cargo/bin & .bashrc.
	c1Cycle1 := strings.Join([]string{
		`printf 'PNPM_ENV:%s:%s\n' "$pnpm_config_store_dir" "$npm_config_store_dir"`,
		`echo C1_NPM > /home/agent/.npm/_cacache/c1.txt`,
		`echo C1_PNPM > /home/agent/.local/share/pnpm/store/c1.txt`,
		`echo C1_CARGO > /home/agent/.cargo/registry/c1.txt`,
		`mkdir -p /home/agent/go/pkg/mod/example.com/m@v1.0.0`,
		`echo C1_GOMOD > /home/agent/go/pkg/mod/example.com/m@v1.0.0/go.mod`,
		`chmod 0444 /home/agent/go/pkg/mod/example.com/m@v1.0.0/go.mod`,
		`chmod 0555 /home/agent/go/pkg/mod/example.com/m@v1.0.0`,
		`mkdir -p /home/agent/.cargo/bin`,
		`echo EVIL_CARGO_BIN > /home/agent/.cargo/bin/evil-tool`,
		`echo EVIL_BASHRC > /home/agent/.bashrc`,
		`printf '%s_%s\n' C1 CYCLE1_OK`,
	}, "\n") + "\n"
	runProcessWallCycle(t, c1ID, helperBin, m1, c1Cycle1, []string{
		"PNPM_ENV:/home/agent/.local/share/pnpm/store:/home/agent/.local/share/pnpm/store",
		"C1_CYCLE1_OK",
	}, 20*time.Second)

	// Sandbox 2 Cycle 1: must NEVER see sandbox 1's caches.
	c2Cycle1 := strings.Join([]string{
		`if [ -e /home/agent/.npm/_cacache/c1.txt ]; then printf '%s_%s\n' LEAK NPM; else printf '%s_%s\n' ISOLATED NPM; fi`,
		`if [ -e /home/agent/.local/share/pnpm/store/c1.txt ]; then printf '%s_%s\n' LEAK PNPM; else printf '%s_%s\n' ISOLATED PNPM; fi`,
		`if [ -e /home/agent/.cargo/registry/c1.txt ]; then printf '%s_%s\n' LEAK CARGO; else printf '%s_%s\n' ISOLATED CARGO; fi`,
		`if [ -e /home/agent/go/pkg/mod/example.com/m@v1.0.0/go.mod ]; then printf '%s_%s\n' LEAK GO; else printf '%s_%s\n' ISOLATED GO; fi`,
		`echo C2_NPM > /home/agent/.npm/_cacache/c2.txt`,
		`printf '%s_%s\n' C2 CYCLE1_OK`,
	}, "\n") + "\n"
	runProcessWallCycle(t, c2ID, helperBin, m2, c2Cycle1, []string{
		"ISOLATED_NPM",
		"ISOLATED_PNPM",
		"ISOLATED_CARGO",
		"ISOLATED_GO",
		"C2_CYCLE1_OK",
	}, 20*time.Second)

	// Sandbox 1 Cycle 2 (after restart): c1's caches survive; c2's caches are invisible; .cargo/bin and .bashrc are gone.
	c1Cycle2 := strings.Join([]string{
		`cat /home/agent/.npm/_cacache/c1.txt`,
		`cat /home/agent/.local/share/pnpm/store/c1.txt`,
		`cat /home/agent/.cargo/registry/c1.txt`,
		`cat /home/agent/go/pkg/mod/example.com/m@v1.0.0/go.mod`,
		`if [ -e /home/agent/.npm/_cacache/c2.txt ]; then printf '%s_%s\n' LEAK C2; else printf '%s_%s\n' NO C2; fi`,
		`if [ -e /home/agent/.cargo/bin/evil-tool ]; then printf '%s_%s\n' CARGO BIN_LEAKED; else printf '%s_%s\n' CARGO BIN_GONE; fi`,
		`if [ -e /home/agent/.bashrc ]; then printf '%s_%s\n' BASHRC LEAKED; else printf '%s_%s\n' BASHRC GONE; fi`,
		`printf '%s_%s\n' C1 CYCLE2_OK`,
	}, "\n") + "\n"
	runProcessWallCycle(t, c1ID, helperBin, m1, c1Cycle2, []string{
		"C1_NPM",
		"C1_PNPM",
		"C1_CARGO",
		"C1_GOMOD",
		"NO_C2",
		"CARGO_BIN_GONE",
		"BASHRC_GONE",
		"C1_CYCLE2_OK",
	}, 20*time.Second)

	// Sandbox 3 (keep_caches = false): caches do not persist across restart.
	runProcessWallCycle(t, c3ID, helperBin, m3,
		"mkdir -p /home/agent/.npm/_cacache && echo C3_EPHEMERAL > /home/agent/.npm/_cacache/c3.txt && printf '%s_%s\\n' C3 ONE_OK\n",
		[]string{"C3_ONE_OK"}, 20*time.Second)
	runProcessWallCycle(t, c3ID, helperBin, m3,
		"if [ -e /home/agent/.npm/_cacache/c3.txt ]; then printf '%s_%s\\n' C3 LEAKED; else printf '%s_%s\\n' C3 GONE; fi\n",
		[]string{"C3_GONE"}, 20*time.Second)

	// Delete c1 (with 0555 Go module cache inside): session folder and caches must be removed cleanly.
	if _, err := host.Delete(c1ID); err != nil {
		t.Fatalf("host.Delete(%s) with read-only Go cache: %v", c1ID, err)
	}
	if _, err := os.Stat(paths.SessionCachesDir(c1ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SessionCachesDir(%s) still exists after rm: err=%v", c1ID, err)
	}
	if _, err := os.Stat(paths.SessionDir(c1ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SessionDir(%s) still exists after rm: err=%v", c1ID, err)
	}
	// Deleting c1 leaves c2's caches intact.
	if _, err := os.Stat(filepath.Join(paths.SessionCachesDir(c2ID), ".npm", "_cacache", "c2.txt")); err != nil {
		t.Fatalf("c2 cache damaged when c1 was deleted: %v", err)
	}
}

type fixturePkg struct {
	name      string
	tarball   []byte
	shasum    string
	integrity string
}

func buildFixtureRegistry(t *testing.T, count int) (server *httptest.Server, fetches *atomic.Int64, pkgJSON []byte) {
	t.Helper()
	pkgs := make(map[string]fixturePkg, count)
	deps := make(map[string]string, count)
	payload := strings.Repeat("exports.value = 'fixture-payload-0123456789abcdef';\n", 512) // ~26 KiB per package

	for i := 1; i <= count; i++ {
		name := fmt.Sprintf("fixture-pkg-%02d", i)
		deps[name] = "1.0.0"

		var gzBuf bytes.Buffer
		gz := gzip.NewWriter(&gzBuf)
		tw := tar.NewWriter(gz)
		files := map[string]string{
			"package/package.json": fmt.Sprintf(`{"name":%q,"version":"1.0.0","main":"index.js"}`, name),
			"package/index.js":     payload,
		}
		for fname, content := range files {
			hdr := &tar.Header{
				Name:    fname,
				Mode:    0o644,
				Size:    int64(len(content)),
				ModTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		tb := gzBuf.Bytes()
		s1 := sha1.Sum(tb)
		s512 := sha512.Sum512(tb)
		pkgs[name] = fixturePkg{
			name:      name,
			tarball:   tb,
			shasum:    hex.EncodeToString(s1[:]),
			integrity: "sha512-" + base64.StdEncoding.EncodeToString(s512[:]),
		}
	}

	var fetchCount atomic.Int64
	oldTime := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if name, rest, ok := strings.Cut(p, "/-/"); ok && strings.HasSuffix(rest, ".tgz") {
			fp, exists := pkgs[name]
			if !exists {
				http.NotFound(w, r)
				return
			}
			fetchCount.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(fp.tarball)
			return
		}
		fp, exists := pkgs[p]
		if !exists {
			http.NotFound(w, r)
			return
		}
		doc := map[string]any{
			"name":      fp.name,
			"dist-tags": map[string]string{"latest": "1.0.0"},
			"time": map[string]string{
				"created":  oldTime,
				"modified": oldTime,
				"1.0.0":    oldTime,
			},
			"versions": map[string]any{
				"1.0.0": map[string]any{
					"name":    fp.name,
					"version": "1.0.0",
					"dist": map[string]string{
						"tarball":   srv.URL + "/" + fp.name + "/-/" + fp.name + "-1.0.0.tgz",
						"shasum":    fp.shasum,
						"integrity": fp.integrity,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)

	pj, err := json.MarshalIndent(map[string]any{
		"name":         "cache-bench-fixture",
		"version":      "1.0.0",
		"private":      true,
		"dependencies": deps,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return srv, &fetchCount, append(pj, '\n')
}

func parseElapsedMS(t *testing.T, out, tag string) int64 {
	t.Helper()
	prefix := tag + ":"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		for strings.HasPrefix(line, "$ ") {
			line = strings.TrimPrefix(line, "$ ")
		}
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			v, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				t.Fatalf("parse %s from %q: %v\nfull output:\n%s", tag, line, err, out)
			}
			return v
		}
	}
	t.Fatalf("output missing timing tag %q:\n%s", tag, out)
	return 0
}

func medianInt64(vals []int64) int64 {
	cp := slices.Clone(vals)
	slices.Sort(cp)
	return cp[len(cp)/2]
}

func TestSupervisorProcessWallNpmPnpmReinstallTiming(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	probe := process.Driver{}.Probe(ctx)
	if !probe.OK {
		t.Skipf("process wall unavailable on this host: %s (%s)", probe.Reason, probe.Fix)
	}

	npmPath, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm not on PATH")
	}
	pnpmPath, err := exec.LookPath("pnpm")
	if err != nil {
		t.Skip("pnpm not on PATH")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	realNode, err := filepath.EvalSymlinks(nodePath)
	if err != nil {
		t.Fatal(err)
	}
	realNPM, err := filepath.EvalSymlinks(npmPath)
	if err != nil {
		t.Fatal(err)
	}
	realPNPM, err := filepath.EvalSymlinks(pnpmPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = realNPM
	nodeRoot := filepath.Dir(filepath.Dir(realNode))
	pnpmDir := filepath.Dir(realPNPM)

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	helperBin := buildTestHostHelper(t, ctx, tmp)
	reg, fetches, pkgJSON := buildFixtureRegistry(t, 25)

	wsHost := filepath.Join(tmp, "bench-proj")
	npmApp := filepath.Join(wsHost, "npm-app")
	pnpmApp := filepath.Join(wsHost, "pnpm-app")
	for _, d := range []string{npmApp, pnpmApp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "package.json"), pkgJSON, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	id := "cb234567"
	now := time.Now().UTC()
	m := &host.Manifest{
		Schema:    1,
		ID:        id,
		Name:      "cache-bench",
		CreatedAt: now,
		Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
		Cwd:       "/work/bench-proj",
		Env: []string{
			"HOME=" + process.GuestHome,
			"USER=agent",
			"LOGNAME=agent",
			"LANG=C.UTF-8",
			"PATH=/opt/tools/node/bin:/opt/tools/pnpm:/usr/local/bin:/usr/bin:/bin",
			"TERM=xterm-256color",
			"npm_config_registry=" + reg.URL,
			"pnpm_config_registry=" + reg.URL,
			"npm_config_audit=false",
			"npm_config_fund=false",
			"npm_config_update_notifier=false",
			"pnpm_config_manage_package_manager_versions=false",
		},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 120, Rows: 40},
		Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall: &host.WallRecord{
			Level:          "process",
			Driver:         process.DriverName,
			Version:        probe.Version,
			Preset:         string(egress.PresetLocked),
			WorkspaceHost:  wsHost,
			WorkspaceGuest: "/work/bench-proj",
			CacheDirs:      sandbox.DefaultCachePaths(),
			ExtraRO: []host.WallMount{
				{Source: nodeRoot, Dest: "/opt/tools/node", ReadOnly: true},
				{Source: pnpmDir, Dest: "/opt/tools/pnpm", ReadOnly: true},
			},
			ShareNet: true,
			At:       now,
		},
	}

	// Cycle 1: cold install populates kept caches + lockfiles, then 3 warm reinstalls with NO restart.
	cycle1Script := strings.Join([]string{
		`measure() { label="$1"; shift; t0=$(date +%s%3N); "$@" >/tmp/m.log 2>&1 || { printf 'FAIL_%s:' "$label"; tail -n 5 /tmp/m.log; return 1; }; t1=$(date +%s%3N); printf '%s:%d\n' "$label" $((t1 - t0)); }`,
		`cd /work/bench-proj/npm-app`,
		`measure NPM_COLD npm install --prefer-offline`,
		`for i in 1 2 3; do rm -rf node_modules && measure "NPM_WARM_$i" npm ci --offline; done`,
		`cd /work/bench-proj/pnpm-app`,
		`printf 'PNPM_STORE:%s\n' "$(pnpm store path)"`,
		`measure PNPM_COLD pnpm install --prefer-offline`,
		`for i in 1 2 3; do rm -rf node_modules && measure "PNPM_WARM_$i" pnpm install --offline --frozen-lockfile; done`,
		`printf '%s_%s\n' BENCH CYCLE1_DONE`,
	}, "\n") + "\n"

	out1 := runProcessWallCycle(t, id, helperBin, m, cycle1Script, []string{
		"PNPM_STORE:/home/agent/.local/share/pnpm/store",
		"BENCH_CYCLE1_DONE",
	}, 60*time.Second)

	coldFetches := fetches.Load()
	if coldFetches < 25 {
		t.Fatalf("cold installs fetched only %d tarballs, want >= 25\n%s", coldFetches, out1)
	}
	npmWarm := medianInt64([]int64{
		parseElapsedMS(t, out1, "NPM_WARM_1"),
		parseElapsedMS(t, out1, "NPM_WARM_2"),
		parseElapsedMS(t, out1, "NPM_WARM_3"),
	})
	pnpmWarm := medianInt64([]int64{
		parseElapsedMS(t, out1, "PNPM_WARM_1"),
		parseElapsedMS(t, out1, "PNPM_WARM_2"),
		parseElapsedMS(t, out1, "PNPM_WARM_3"),
	})

	// Reset tarball counter before Cycle 2 (fresh sandbox after stop+restart, kept caches reattached).
	fetches.Store(0)
	cycle2Script := strings.Join([]string{
		`measure() { label="$1"; shift; t0=$(date +%s%3N); "$@" >/tmp/m.log 2>&1 || { printf 'FAIL_%s:' "$label"; tail -n 5 /tmp/m.log; return 1; }; t1=$(date +%s%3N); printf '%s:%d\n' "$label" $((t1 - t0)); }`,
		`cd /work/bench-proj/npm-app`,
		`for i in 1 2 3; do rm -rf node_modules && measure "NPM_RESTART_$i" npm ci --offline; done`,
		`cd /work/bench-proj/pnpm-app`,
		`for i in 1 2 3; do rm -rf node_modules && measure "PNPM_RESTART_$i" pnpm install --offline --frozen-lockfile; done`,
		`printf '%s_%s\n' BENCH CYCLE2_DONE`,
	}, "\n") + "\n"

	out2 := runProcessWallCycle(t, id, helperBin, m, cycle2Script, []string{
		"BENCH_CYCLE2_DONE",
	}, 60*time.Second)

	if restartFetches := fetches.Load(); restartFetches != 0 {
		t.Fatalf("reinstalls after restart fetched %d tarballs from registry, want 0 (must hit kept caches)", restartFetches)
	}

	npmRestart := medianInt64([]int64{
		parseElapsedMS(t, out2, "NPM_RESTART_1"),
		parseElapsedMS(t, out2, "NPM_RESTART_2"),
		parseElapsedMS(t, out2, "NPM_RESTART_3"),
	})
	pnpmRestart := medianInt64([]int64{
		parseElapsedMS(t, out2, "PNPM_RESTART_1"),
		parseElapsedMS(t, out2, "PNPM_RESTART_2"),
		parseElapsedMS(t, out2, "PNPM_RESTART_3"),
	})

	npmCold := parseElapsedMS(t, out1, "NPM_COLD")
	pnpmCold := parseElapsedMS(t, out1, "PNPM_COLD")
	npmDeltaPct := float64(npmRestart-npmWarm) * 100.0 / float64(max(npmWarm, 1))
	pnpmDeltaPct := float64(pnpmRestart-pnpmWarm) * 100.0 / float64(max(pnpmWarm, 1))
	t.Logf("npm  reinstall timings (25 pkgs): cold=%dms no_restart_median=%dms after_restart_median=%dms delta=%+.1f%%",
		npmCold, npmWarm, npmRestart, npmDeltaPct)
	t.Logf("pnpm reinstall timings (25 pkgs): cold=%dms no_restart_median=%dms after_restart_median=%dms delta=%+.1f%%",
		pnpmCold, pnpmWarm, pnpmRestart, pnpmDeltaPct)

	// Spec 12 target: within 20% of a reinstall with no restart (with a 150ms floor
	// for sub-second OS scheduler jitter under parallel CI load).
	npmLimit := max(npmWarm*120/100, npmWarm+150)
	if npmRestart > npmLimit {
		t.Errorf("npm restart median %dms exceeded limit %dms (no-restart median %dms, delta %+.1f%%)", npmRestart, npmLimit, npmWarm, npmDeltaPct)
	}
	pnpmLimit := max(pnpmWarm*120/100, pnpmWarm+150)
	if pnpmRestart > pnpmLimit {
		t.Errorf("pnpm restart median %dms exceeded limit %dms (no-restart median %dms, delta %+.1f%%)", pnpmRestart, pnpmLimit, pnpmWarm, pnpmDeltaPct)
	}
}
