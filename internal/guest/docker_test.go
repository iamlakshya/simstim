//go:build unix

package guest

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindDockerd(t *testing.T) {
	dir := t.TempDir()
	nonExec := filepath.Join(dir, "nonexec")
	if err := os.WriteFile(nonExec, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	execBin := filepath.Join(dir, "dockerd")
	if err := os.WriteFile(execBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findDockerd(dir, nonExec, execBin); got != execBin {
		t.Fatalf("findDockerd = %q, want %q", got, execBin)
	}
	if got := findDockerd(dir, nonExec, filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("findDockerd(missing) = %q, want empty", got)
	}
}

func TestDockerLazyActivator(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for fake dockerd test")
	}
	dir := t.TempDir()
	startedFlag := filepath.Join(dir, "started")
	fakeDockerd := filepath.Join(dir, "fake-dockerd")
	script := "#!" + py + "\n" + `
import http.server, os, signal, socketserver, sys

sock = ""
for arg in sys.argv[1:]:
    if arg.startswith("--host=unix://"):
        sock = arg[len("--host=unix://"):]
if not sock:
    sys.exit(2)
with open(` + `"` + startedFlag + `"` + `, "a") as f:
    f.write("started\n")

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"ServerVersion":"fake-dockerd-1.0"}\n'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass

class UnixHTTPServer(socketserver.UnixStreamServer):
    allow_reuse_address = True

if os.path.exists(sock):
    os.unlink(sock)
srv = UnixHTTPServer(sock, Handler)
signal.signal(signal.SIGTERM, lambda *a: os._exit(0))
srv.serve_forever()
`
	if err := os.WriteFile(fakeDockerd, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pubSock := filepath.Join(dir, "docker.sock")
	mgr, err := startDockerActivator(ctx, dockerOptions{
		DockerdPath: fakeDockerd,
		PublicSock:  pubSock,
		DaemonSock:  filepath.Join(dir, "dockerd.sock"),
		DataRoot:    filepath.Join(dir, "data"),
		ExecRoot:    filepath.Join(dir, "exec"),
		PIDFile:     filepath.Join(dir, "docker.pid"),
		ProxyURL:    "http://127.0.0.1:3128",
		StartWait:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("startDockerActivator: %v", err)
	}
	defer mgr.Stop(2 * time.Second)

	// Verify lazy start: dockerd must NOT have started before first client connect.
	if _, err := os.Stat(startedFlag); !os.IsNotExist(err) {
		t.Fatal("dockerd started eagerly before first client connection")
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", pubSock)
			},
		},
		Timeout: 5 * time.Second,
	}

	// First request lazily starts fake-dockerd and succeeds.
	for i := range 2 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/info", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "fake-dockerd-1.0") {
			t.Fatalf("request %d = %d %q", i, resp.StatusCode, body)
		}
	}

	// Fake dockerd should have been started only once across both requests.
	raw, err := os.ReadFile(startedFlag)
	if err != nil {
		t.Fatalf("read startedFlag: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(raw)), "\n"); len(lines) != 1 {
		t.Errorf("dockerd started %d times, want 1", len(lines))
	}

	mgr.Stop(2 * time.Second)
	if _, err := os.Stat(pubSock); !os.IsNotExist(err) {
		t.Errorf("public socket left behind after Stop: %v", err)
	}
}
