package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// A hostile guest, which the test scripts in full: it hands the ctl
// stream and each holder stream to the test, and answers nothing by
// itself.
type hostile struct {
	disk    *diskResourcesV1
	conn    net.Conn
	sess    *link.Session
	ctls    chan *link.Stream
	holders chan *link.Stream
	done    chan struct{}
	once    sync.Once
}

func (f *hostile) ownedDiskResourcesV1() *diskResourcesV1 { return f.disk }

func (f *hostile) Start(ctx context.Context) (net.Conn, error) {
	ours, theirs, err := guest.SocketPair()
	if err != nil {
		return nil, err
	}
	conn, err := net.FileConn(theirs)
	theirs.Close()
	if err != nil {
		return nil, err
	}
	f.conn = conn
	f.sess = link.New(conn, link.Guest, link.Options{})
	go func() {
		for {
			st, err := f.sess.Accept(context.WithoutCancel(ctx))
			if err != nil {
				return
			}
			switch st.Kind() {
			case link.KindCtl:
				f.ctls <- st
			case link.KindHolder:
				f.holders <- st
			default:
				_ = st.Close()
			}
		}
	}()
	return ours, nil
}

func (f *hostile) Kill() error {
	f.once.Do(func() {
		if f.sess != nil {
			_ = f.sess.Close()
			_ = f.conn.Close()
		}
		close(f.done)
	})
	return nil
}

func (f *hostile) Wait() error { <-f.done; return nil }

// hostileRun is a supervisor whose guest is hostile.
type hostileRun struct {
	id      string
	g       *hostile
	ended   chan error
	ctl     *link.Stream
	r       *bufio.Reader
	w       *proto.Writer
	ctlReqs chan proto.Request
}

// hostileStart starts a supervisor whose guest is hostile, for a session
// with env, and returns once the supervisor opened the ctl stream. The
// test answers start, or does not.
func hostileStart(t *testing.T, env ...string) *hostileRun {
	t.Helper()
	dir := testFolders(t)
	const id = "abcd2345"
	t0 := time.Now().UTC()
	m := &host.Manifest{Schema: 1, ID: id, Name: "box", CreatedAt: t0, Cwd: dir, Env: env,
		Agent:  host.AgentInfo{Kind: "claude", Argv: []string{"/bin/sh"}},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Status: host.Status{State: host.StateStarting, Since: t0},
		Wall:   &host.WallRecord{Level: "none", Driver: "fake", Version: version.Version, At: t0}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	g := &hostile{disk: fixtureDiskResourcesV1(t), ctls: make(chan *link.Stream, 1), holders: make(chan *link.Stream, 64), done: make(chan struct{})}
	old, oldFake := sandboxFor, testFakeSandbox
	sandboxFor, testFakeSandbox = func(*host.Manifest) (sandbox, error) { return g, nil }, true
	t.Cleanup(func() { sandboxFor, testFakeSandbox = old, oldFake })
	ended, fin := make(chan error, 1), make(chan struct{})
	go func() {
		ended <- Run(id)
		close(fin)
	}()
	t.Cleanup(func() {
		_, _ = host.HolderRequest(id, "kill", nil, time.Second)
		_ = g.Kill()
		select {
		case <-fin:
		case <-time.After(20 * time.Second):
			t.Error("the supervisor did not stop")
		}
	})
	var st *link.Stream
	select {
	case st = <-g.ctls:
	case err := <-ended:
		t.Fatalf("supervisor exited before ctl stream: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no ctl stream")
	}
	return &hostileRun{id: id, g: g, ended: ended, ctl: st, r: bufio.NewReader(st), w: proto.NewWriter(st), ctlReqs: make(chan proto.Request, 16)}
}

// nextCtlReq returns the next non-stop ctl request received after answerStart.
func (h *hostileRun) nextCtlReq(t *testing.T) proto.Request {
	t.Helper()
	select {
	case req := <-h.ctlReqs:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ctl request")
		return proto.Request{}
	}
}

// readReq reads the next request on the ctl stream.
func (h *hostileRun) readReq(t *testing.T) proto.Request {
	t.Helper()
	f, err := proto.Read(h.r)
	if err != nil {
		t.Fatalf("read a request: %v", err)
	}
	var req proto.Request
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		t.Fatal(err)
	}
	return req
}

// answerStart answers start, waits for the session's start, then serves
// stop in the background.
func (h *hostileRun) answerStart(t *testing.T) {
	t.Helper()
	req := h.readReq(t)
	if req.Op != proto.CtlStart {
		t.Fatalf("op %s, want start", req.Op)
	}
	res, _ := json.Marshal(proto.CtlStartResult{AgentPID: 7, GuestABI: proto.GuestPTY1000V1})
	if err := h.w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "working", func() bool {
		m, err := host.LoadManifest(h.id)
		return err == nil && m.Status.State == host.StateWorking
	})
	go func() {
		for {
			f, err := proto.Read(h.r)
			if err != nil {
				return
			}
			var req proto.Request
			if json.Unmarshal(f.Payload, &req) != nil {
				continue
			}
			if req.Op == proto.CtlStop {
				_ = h.w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{}`)})
				_ = h.g.Kill()
				return
			}
			select {
			case h.ctlReqs <- req:
			default:
			}
		}
	}()
}

func (h *hostileRun) send(t *testing.T, ev proto.CtlEvent) {
	t.Helper()
	if err := h.w.WriteJSON(proto.TEvent, ev); err != nil {
		t.Fatal(err)
	}
}

// waitEnd waits for the supervisor's end.
func (h *hostileRun) waitEnd(t *testing.T, within time.Duration) error {
	t.Helper()
	select {
	case err := <-h.ended:
		return err
	case <-time.After(within):
		t.Fatalf("the supervisor did not end within %s", within)
	}
	return nil
}

// testFolders points the host's folders at a new short folder.
func testFolders(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sv")
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for k, v := range map[string]string{"HOME": dir, "XDG_STATE_HOME": filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"), "XDG_DATA_HOME": filepath.Join(dir, "data"),
		"XDG_RUNTIME_DIR": dir, "DBUS_SESSION_BUS_ADDRESS": ""} {
		t.Setenv(k, v)
	}
	return dir
}

// fakeCgroup points the supervisor's memory watch at a fake cgroup folder,
// at 100 MiB of a 1 GiB cap, and returns a function that sets one of its
// files.
func fakeCgroup(t *testing.T) func(name, v string) {
	t.Helper()
	cg := t.TempDir()
	set := func(name, v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cg, name), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set("memory.max", strconv.Itoa(1<<30))
	set("memory.current", strconv.Itoa(100<<20))
	set("memory.events", "max 0\noom 0\noom_kill 0")
	set("memory.stat", "anon 0\nfile 0\nshmem 0\nslab_reclaimable 0")
	old := unitDir
	unitDir = func(string) string { return cg }
	t.Cleanup(func() { unitDir = old })
	return set
}

// alertsTo sends every alert of the host's sessions to a webhook of the
// test, and returns the kinds that it gets, in order.
func alertsTo(t *testing.T) <-chan string {
	t.Helper()
	got := make(chan string, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ev alerts.Event
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &ev) == nil {
			got <- ev.Event
		}
	}))
	t.Cleanup(srv.Close)
	if err := os.MkdirAll(filepath.Dir(paths.AlertsConfig()), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "events = [\"exited\", \"failed\", \"oom\", \"memory\", \"sandbox_error\", \"link_silent\", \"started\"]\n" +
		"[[destination]]\ntype = \"webhook\"\nurl = \"" + srv.URL + "\"\n"
	if err := os.WriteFile(paths.AlertsConfig(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return got
}

// waitAlerts waits until the webhook got each of the kinds.
func waitAlerts(t *testing.T, got <-chan string, kinds ...string) {
	t.Helper()
	var seen []string
	timeout := time.After(10 * time.Second)
	for {
		missing := false
		for _, k := range kinds {
			missing = missing || !slices.Contains(seen, k)
		}
		if !missing {
			return
		}
		select {
		case k := <-got:
			seen = append(seen, k)
		case <-timeout:
			t.Fatalf("the alerts were %v, want %v among them", seen, kinds)
		}
	}
}
