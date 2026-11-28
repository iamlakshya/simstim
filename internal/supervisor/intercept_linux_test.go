//go:build linux

package supervisor

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func buildMockNPMPackageTarball(t *testing.T, name, ver string) (tgz []byte, integrity string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"package/package.json": fmt.Sprintf(`{"name":%q,"version":%q,"main":"index.js"}`+"\n", name, ver),
		"package/index.js":     "module.exports = 'hello-from-mock-npm';\n",
	}
	for fname, content := range files {
		hdr := &tar.Header{
			Name: fname,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", fname, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write %s: %v", fname, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	tgz = buf.Bytes()
	sum := sha512.Sum512(tgz)
	return tgz, "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func startMockNPMRegistry(t *testing.T) (*httptest.Server, *tls.Config) {
	t.Helper()
	tgz, integrity := buildMockNPMPackageTarball(t, "p22-demo-pkg", "1.0.0")
	// Published well over 7 days ago so min_release_age=7d allows it.
	oldTime := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)

	ca, err := egress.NewSessionCA()
	if err != nil {
		t.Fatalf("NewSessionCA mock registry: %v", err)
	}
	t.Cleanup(ca.Close)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/p22-demo-pkg":
			doc := map[string]any{
				"name":      "p22-demo-pkg",
				"dist-tags": map[string]string{"latest": "1.0.0"},
				"time": map[string]string{
					"created":  oldTime,
					"modified": oldTime,
					"1.0.0":    oldTime,
				},
				"versions": map[string]any{
					"1.0.0": map[string]any{
						"name":    "p22-demo-pkg",
						"version": "1.0.0",
						"dist": map[string]any{
							"tarball":   "https://registry.npmjs.org/p22-demo-pkg/-/p22-demo-pkg-1.0.0.tgz",
							"integrity": integrity,
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(doc)
		case r.Method == http.MethodGet && r.URL.Path == "/p22-demo-pkg/-/p22-demo-pkg-1.0.0.tgz":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(tgz)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "upstream saw %s %s\n", r.Method, r.URL.Path)
		}
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := chi.ServerName
			if name == "" {
				name = "registry.npmjs.org"
			}
			return ca.LeafForHost(name)
		},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv, &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    ca.CertPool(),
	}
}

func TestSupervisorProcessWallInterceptionAndNoPrivateKeyOnDisk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	helperBin := buildTestHelper(t, ctx, tmp)
	mockReg, upTLS := startMockNPMRegistry(t)
	mockAddr := mockReg.Listener.Addr().String()

	wsHost := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(wsHost, 0o700); err != nil {
		t.Fatal(err)
	}
	pkgJSON := `{"name":"sandbox-test-app","version":"1.0.0"}` + "\n"
	if err := os.WriteFile(filepath.Join(wsHost, "package.json"), []byte(pkgJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	useRealNPM := hasSandboxTool("npm")
	useRealCurl := hasSandboxTool("curl")

	makeEgressStarter := func() func(context.Context, string, egress.StartArgs, egress.AuditSink) (*egress.Client, func(), error) {
		return func(initCtx context.Context, id string, args egress.StartArgs, sink egress.AuditSink) (*egress.Client, func(), error) {
			supConn, egConn := net.Pipe()
			addrs, err := egress.NewAddressPolicyWithHosts(nil)
			if err != nil {
				return nil, nil, err
			}
			dialer := &egress.SafeDialer{
				Addrs:    addrs,
				Resolver: staticIPResolver{addr: netip.MustParseAddr("198.51.100.10")},
				Dial: func(dctx context.Context, network, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(dctx, network, mockAddr)
				},
			}
			egCtx, egCancel := context.WithCancel(context.WithoutCancel(initCtx))
			egDone := make(chan error, 1)
			go func() {
				egDone <- egress.RunOnConn(egCtx, id, egConn, &egress.ServerOptions{
					Addrs:       addrs,
					Dialer:      dialer,
					UpstreamTLS: upTLS,
				})
			}()
			cl, err := egress.NewClient(initCtx, supConn, args, sink)
			if err != nil {
				egCancel()
				_ = supConn.Close()
				_ = egConn.Close()
				<-egDone
				return nil, nil, err
			}
			stop := func() {
				_ = cl.Close()
				egCancel()
				_ = supConn.Close()
				_ = egConn.Close()
				<-egDone
			}
			return cl, stop, nil
		}
	}

	runSession := func(id, cmds string, want []string) string {
		t.Helper()
		now := time.Now().UTC()
		m := &host.Manifest{
			Schema:    1,
			ID:        id,
			Name:      "proj",
			CreatedAt: now,
			Agent:     host.AgentInfo{Kind: "shell", Argv: []string{"/bin/sh"}},
			Cwd:       "/work/proj",
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
			Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
			Run:    host.Runtime{Socket: paths.Socket(id), BootID: host.BootID()},
			Status: host.Status{State: host.StateStarting, Since: now},
			Wall: &host.WallRecord{
				Level:          "process",
				Driver:         process.DriverName,
				Version:        probe.Version,
				Preset:         string(egress.PresetBalanced),
				WorkspaceHost:  wsHost,
				WorkspaceGuest: "/work/proj",
				At:             now,
			},
		}
		if err := m.Save(); err != nil {
			t.Fatalf("save manifest: %v", err)
		}
		sb := NewProcessSandbox(id, helperBin, m)
		supDone := make(chan error, 1)
		go func() {
			supDone <- RunWith(id, sb, Options{
				HeartbeatTimeout: 10 * time.Second,
				TickInterval:     200 * time.Millisecond,
				StopGrace:        3 * time.Second,
				EgressStarter:    makeEgressStarter(),
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

		// While the session is running, verify ZERO files on the host contain
		// PRIVATE KEY under DataDir, StateDir, RuntimeDir or SessionDir.
		assertNoSecretOnDisk(t, "PRIVATE KEY",
			paths.DataDir(),
			paths.StateDir(),
			paths.RuntimeDir(),
			paths.SessionDir(id),
		)

		w := proto.NewWriter(conn)
		rd := bufio.NewReaderSize(conn, 64<<10)
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := w.WriteJSON(proto.THello, proto.Hello{
			Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge",
		}); err != nil {
			t.Fatalf("send hello: %v", err)
		}
		if hf, err := proto.Read(rd); err != nil || hf.Type != proto.THello {
			t.Fatalf("read hello: %v", err)
		}
		if err := w.WriteJSON(proto.TAttach, proto.Attach{Session: id, Cols: 80, Rows: 24}); err != nil {
			t.Fatalf("send attach: %v", err)
		}
		if af, err := proto.Read(rd); err != nil || af.Type != proto.TAttached {
			t.Fatalf("read attached: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
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
					ok := true
					for _, wStr := range want {
						if !strings.Contains(cur, wStr) {
							ok = false
							break
						}
					}
					if ok {
						return
					}
				}
			}
		}()

		select {
		case <-readDone:
		case <-time.After(25 * time.Second):
			_ = conn.Close()
			auditData, _ := os.ReadFile(paths.AuditLog(id))
			t.Fatalf("timed out waiting for %v; got:\n%s\naudit:\n%s", want, out.String(), string(auditData))
		}
		_ = conn.Close()

		// Scan again after intercepted requests completed.
		assertNoSecretOnDisk(t, "PRIVATE KEY",
			paths.DataDir(),
			paths.StateDir(),
			paths.RuntimeDir(),
			paths.SessionDir(id),
		)

		if _, err := host.HolderRequest(id, "kill", nil, 5*time.Second); err != nil {
			t.Fatalf("kill %s: %v", id, err)
		}
		select {
		case err := <-supDone:
			if err != nil {
				t.Fatalf("supervisor %s returned error: %v", id, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("supervisor %s did not exit", id)
		}
		return out.String()
	}

	// Build guest command: verify CA env vars, verify zero PRIVATE KEY inside
	// /run/<brand>/, capture CA fingerprint, test HTTPS GET (200) and HTTPS PUT
	// (403) against registry.npmjs.org over the session CA, and if npm is
	// installed run real `npm install p22-demo-pkg` + `npm publish` (403)!
	runDir := "/run/" + brand.Name
	cmdLines := []string{
		`printf 'CAENV:%s:%s\n' "$SSL_CERT_FILE" "$NODE_EXTRA_CA_CERTS"`,
		`if grep -R -i "PRIVATE KEY" ` + runDir + ` >/dev/null 2>&1; then echo GUEST_KEY_LEAK; else echo GUEST_NO_PRIVKEY; fi`,
		`sha256sum ` + runDir + `/ca.pem | awk '{print "CA_" "SHA:" $1}'`,
		`python3 -c 'import os,ssl,urllib.request,urllib.error
ctx=ssl.create_default_context(cafile=os.environ["SSL_CERT_FILE"])
opener=urllib.request.build_opener(urllib.request.ProxyHandler({"https":"http://127.0.0.1:3128"}), urllib.request.HTTPSHandler(context=ctx))
r=opener.open("https://registry.npmjs.org/p22-demo-pkg")
print("PY_GET:" + str(r.status))
try:
    req=urllib.request.Request("https://registry.npmjs.org/p22-demo-pkg", data=b"{}", method="PUT")
    opener.open(req)
    print("PY_PUT:UNEXPECTED_OK")
except urllib.error.HTTPError as e:
    print("PY_PUT:" + str(e.code))'`,
	}
	want := []string{
		"CAENV:" + runDir + "/ca-bundle.pem:" + runDir + "/ca.pem",
		"GUEST_NO_PRIVKEY",
		"PY_GET:200",
		"PY_PUT:403",
		"SESSION1_DONE",
	}
	if useRealCurl {
		cmdLines = append(cmdLines,
			`curl -sS --http2 -o /dev/null -w 'CURL_GET:%{http_version}:%{http_code}\n' https://registry.npmjs.org/p22-demo-pkg`,
			`curl -sS --http2 -X PUT -d '{}' -o /dev/null -w 'CURL_PUT:%{http_version}:%{http_code}\n' https://registry.npmjs.org/p22-demo-pkg`,
			`curl -sS --http1.1 -H 'Host: evil.example' -o /dev/null -w 'CURL_MISMATCH:%{http_code}\n' https://registry.npmjs.org/p22-demo-pkg`,
		)
		want = append(want, "CURL_GET:2:200", "CURL_PUT:2:403", "CURL_MISMATCH:400")
	}
	if useRealNPM {
		cmdLines = append(cmdLines,
			`npm install --no-audit --no-fund --prefer-online p22-demo-pkg && test -f node_modules/p22-demo-pkg/index.js && echo NPM_INSTALL_OK`,
			`printf '//registry.npmjs.org/:_authToken=dummy-token\n' > /home/agent/.npmrc`,
			`npm publish --registry https://registry.npmjs.org/ 2>&1 | grep -E '403|E403' && echo NPM_PUBLISH_403`,
		)
		want = append(want, "NPM_INSTALL_OK", "NPM_PUBLISH_403")
	}
	cmdLines = append(cmdLines, `echo SESSION1_DONE`)

	out1 := runSession("p22s1001", strings.Join(cmdLines, "\n")+"\n", want)

	// Start a second session and verify it receives a DIFFERENT CA certificate.
	cmd2 := `sha256sum ` + runDir + `/ca.pem | awk '{print "CA_" "SHA:" $1}'` + "\necho SESSION2_DONE\n"
	out2 := runSession("p22s2002", cmd2, []string{"CA_SHA:", "SESSION2_DONE"})

	sha1 := extractSHA256Value(out1, "CA_SHA:")
	sha2 := extractSHA256Value(out2, "CA_SHA:")
	if sha1 == "" || sha2 == "" {
		t.Fatalf("missing 64-hex CA_SHA in outputs: sha1=%q sha2=%q", sha1, sha2)
	}
	if sha1 == sha2 {
		t.Fatalf("two sessions received identical session CA cert sha256=%s", sha1)
	}
}

func hasSandboxTool(name string) bool {
	for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			return true
		}
	}
	return false
}

func extractSHA256Value(output, prefix string) string {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if idx := strings.Index(line, prefix); idx >= 0 {
			val := strings.TrimSpace(line[idx+len(prefix):])
			if fields := strings.Fields(val); len(fields) > 0 && len(fields[0]) == 64 {
				return fields[0]
			}
		}
	}
	return ""
}
