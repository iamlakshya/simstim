package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestForwardWalledSession(t *testing.T) {
	th := newTestHost(t)
	s, err := host.New(host.NewArgs{
		Name: "walled-web",
		Cwd:  "~",
		Wall: "plain",
		Argv: []string{"/bin/sh", "-c", "exec sleep 60"},
	})
	if err != nil {
		t.Fatalf("new walled session: %v", err)
	}

	// Open a real TCP listener in the test process and also grant port 3000 so
	// `ports` shows a granted port, then test `ports` output and `forward`.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Named port forward on a walled session grants a Unix socket, passes
	// -L 127.0.0.1:<local>:<PortSocket> to ssh, prints the browser cookie
	// notice on the first forward, and revokes the grant when ssh exits.
	out := okSaid(t, "forward", "box/walled-web", "45681:"+strconv.Itoa(port))
	wantSock := paths.PortSocket(s.ID, port)
	if !strings.Contains(out, "Forwarding http://localhost:45681 → box:") {
		t.Errorf("forward stderr missing Forwarding line:\n%s", out)
	}
	if !strings.Contains(out, "http://"+s.ID+".localhost:45681") {
		t.Errorf("first walled forward missing browser cookie notice:\n%s", out)
	}
	log, _ := os.ReadFile(filepath.Join(th.h.Home, "ssh.log"))
	if !strings.Contains(string(log), "-L 127.0.0.1:45681:"+wantSock) {
		t.Errorf("walled forward did not pass Unix socket to ssh -L:\n%s", log)
	}
	// Grant created by `forward` must be revoked once `forward` returns.
	if _, err := os.Stat(wantSock); !os.IsNotExist(err) {
		t.Fatalf("port socket %s was not removed after forward exited: %v", wantSock, err)
	}

	// A second forward on the same walled session does not repeat the notice.
	out2 := okSaid(t, "forward", "box/walled-web", "45682:"+strconv.Itoa(port))
	if strings.Contains(out2, ".localhost:") {
		t.Errorf("second walled forward repeated browser cookie notice:\n%s", out2)
	}

	// Grant directly so `ports` lists it as [granted] and `forward` without
	// ports refuses when input is off (exit code 4, forwarding nothing).
	conn, err := client.Dial(context.Background(), th.ch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.GrantPort(context.Background(), s.ID, port); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.RevokePort(context.Background(), s.ID, port) })

	portsOut := ok(t, "ports", "box/walled-web")
	if !strings.Contains(portsOut, "[granted]") {
		t.Errorf("ports output missing [granted]:\n%s", portsOut)
	}
	portsJSON := okJSON(t, "ports", "box/walled-web", "--json")
	var pj struct {
		Ports []client.Port `json:"ports"`
	}
	if err := json.Unmarshal([]byte(portsJSON), &pj); err != nil || len(pj.Ports) == 0 || !pj.Ports[0].Granted {
		t.Fatalf("ports --json = %q (err=%v)", portsJSON, err)
	}

	// Omitting ports on a walled session with input off forwards none (exit 4).
	errOut, code := lineErr(t, "forward", "box/walled-web", "--no-input")
	if code != 4 || !strings.Contains(errOut, "Try: "+brand.Name+" forward box/walled-web ") {
		t.Errorf("walled forward --no-input = exit %d\n%s", code, errOut)
	}

	// Interactive prompt lists claimed/granted ports, lets Enter forward none,
	// and resolves index or port selection.
	listed := []client.Port{{Port: 3000, Address: "127.0.0.1", Process: "node", PID: 12, Claimed: true}}
	var buf bytes.Buffer
	gotNone, err := askWalledPorts(testPrompts("\n", &buf), th.ch, &client.Session{ID: s.ID, Name: "walled-web"}, "box/walled-web", listed)
	if err != nil || len(gotNone) != 0 || !strings.Contains(buf.String(), "No ports forwarded.") {
		t.Fatalf("askWalledPorts Enter = %v, %v\n%s", gotNone, err, buf.String())
	}
	buf.Reset()
	gotPick, err := askWalledPorts(testPrompts("1 8080:3000\n", &buf), th.ch, &client.Session{ID: s.ID, Name: "walled-web"}, "box/walled-web", listed)
	if err != nil || strings.Join(gotPick, ",") != "3000,8080:3000" {
		t.Fatalf("askWalledPorts pick = %v, %v", gotPick, err)
	}
}

func TestPrintPortsWalledMarkers(t *testing.T) {
	var b bytes.Buffer
	printPorts(&b, []client.Port{
		{Port: 3000, Address: "127.0.0.1", PID: 10, Process: "node", Claimed: true},
		{Port: 5173, Address: "127.0.0.1", PID: 11, Process: "vite", Claimed: true, Granted: true},
		{Port: 8080, Address: "127.0.0.1", Granted: true},
	})
	got := b.String()
	for _, want := range []string{"[claimed]", "[claimed, granted]", "- [granted]"} {
		if !strings.Contains(got, want) {
			t.Errorf("printPorts missing %q:\n%s", want, got)
		}
	}
}
