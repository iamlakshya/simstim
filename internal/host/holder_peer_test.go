package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/ring"
	"github.com/iamlakshya/simstim/internal/version"
)

// userMgr is the cgroup of a user's systemd user manager, as ops.go builds
// it, for uid 1000.
var userMgr = fmt.Sprintf("/user.slice/user-%d.slice/user@%d.service/", 1000, 1000)

// The cgroup files of a fake peer, for the session id.
func outsideCgroup(string) string { return "0::/user.slice/user-1000.slice/session-3.scope\n" }

func otherCgroup(string) string {
	return "0::" + userMgr + brand.Slice + "/" + UnitName("othersess") + "\n"
}

func ownCgroup(id string) string { return "0::" + userMgr + brand.Slice + "/" + UnitName(id) + "\n" }

func gitScopeCgroup(string) string {
	return "0::" + userMgr + brand.Slice + "/" + brand.Name + "-git-0123abcd.scope\n"
}

func noCgroup(string) string { return "" }

// fakePid is the pid of every fake peer.
const fakePid = 4242

// fakePeerCgroup is the path of the fake peer's cgroup file, once fakePeer has
// made the fake /proc.
func fakePeerCgroup() string { return filepath.Join(procRoot, "4242", "cgroup") }

// fakePeer makes the peer of every connection the process fakePid, of this
// user. Its cgroup file holds cgroup, or is missing when cgroup is "". It
// lives while alive says so, and nil stands for a kernel without pidfds. It
// returns the count of pidfds closed.
func fakePeer(t *testing.T, cgroup string, alive func() bool) *atomic.Int32 {
	t.Helper()
	files := map[string]string{}
	if cgroup != "" {
		files["4242/cgroup"] = cgroup
	}
	fakeProc(t, files)
	closed := &atomic.Int32{}
	setReadPeer(t, func(net.Conn) (peer, error) {
		p := peer{pid: fakePid, uid: os.Getuid(), alive: alive}
		if alive != nil {
			p.done = func() { closed.Add(1) }
		}
		return p, nil
	})
	return closed
}

// setReadPeer replaces readPeer for the rest of the test. Call it before the
// holder serves, so that no connection reads it while it changes.
func setReadPeer(t *testing.T, read func(net.Conn) (peer, error)) {
	old := readPeer
	readPeer = read
	t.Cleanup(func() { readPeer = old })
}

// readyBytes are what the rig's agent prints, in raw mode, before it reads.
const readyBytes = "READY\x1b[?2004h\n"

// peerRig is a holder in this process, with both sockets open. Its agent
// prints readyBytes, then writes the first 6 bytes it reads to typed.
type peerRig struct {
	t      *testing.T
	h      *Holder
	typed  string
	closed *atomic.Int32 // the fake peer's pidfds closed
}

// rigPeer is the fake peer of a rig's connections.
type rigPeer struct {
	check  bool                         // the holder's peer check
	cgroup func(string) string          // the peer's cgroup file, for the session's ID
	alive  func() bool                  // nil: no pidfd
	read   func(net.Conn) (peer, error) // in place of the fake peer, when set
}

// peerHolder makes a holder for a new session in this process, with both
// sockets open, and the fake peer fp. Its agent does not run yet.
func peerHolder(t *testing.T, fp rigPeer, m *Manifest) (*Holder, *atomic.Int32) {
	t.Helper()
	secretHome(t)
	closed := fakePeer(t, fp.cgroup(m.ID), fp.alive)
	if fp.read != nil {
		setReadPeer(t, fp.read)
	}
	if err := paths.EnsurePrivateDir(paths.SessionDir(m.ID)); err != nil {
		t.Fatal(err)
	}
	h := &Holder{id: m.ID, m: m, log: slog.New(slog.DiscardHandler), ring: ring.New(1 << 16),
		clients: map[*hclient]struct{}{}, readDone: make(chan struct{})}
	if err := h.listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.closeSockets)
	h.peerCheck = fp.check
	return h, closed
}

func newPeerRig(t *testing.T, fp rigPeer) *peerRig {
	t.Helper()
	typed := filepath.Join(t.TempDir(), "typed")
	m := &Manifest{Schema: 1, ID: NewID(), Name: "peer",
		Agent: AgentInfo{Kind: "custom", Argv: []string{"/bin/sh", "-c", `stty raw -echo; printf 'READY\033[?2004h\n'; head -c 6 > "$0"; exec sleep 30`, typed}},
		Cwd:   t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, Term: TermInfo{Cols: 80, Rows: 24}}
	h, closed := peerHolder(t, fp, m)
	if err := h.start(); err != nil {
		t.Fatal(err)
	}
	go h.acceptLoop(h.ln, false)
	go h.acceptLoop(h.hookLn, true)
	go h.readLoop()
	// Kill the agent's group, close the terminal, reap the agent, and wait
	// for the reader, so that no test leaves a process behind. Check both.
	t.Cleanup(func() {
		pid := h.cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		h.ptmx.Close()
		_ = h.cmd.Wait()
		if h.cmd.ProcessState == nil {
			t.Error("the rig did not reap its agent")
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("the rig's agent %d is still there: %v", pid, err)
		}
		select {
		case <-h.readDone:
		case <-time.After(5 * time.Second):
			t.Error("the rig's reader did not end")
		}
	})
	r := &peerRig{t: t, h: h, typed: typed, closed: closed}
	waitUntil(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.ring.End() >= int64(len(readyBytes))
	})
	return r
}

// rawConn is one connection to a holder's socket.
type rawConn struct {
	t *testing.T
	r *bufio.Reader
	w *proto.Writer
}

// dialHolder connects to sock, says hello as role, and returns the first
// frame that comes back.
func dialHolder(t *testing.T, sock, role string) (*rawConn, proto.Frame) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	rc := &rawConn{t: t, r: bufio.NewReader(c), w: proto.NewWriter(c)}
	if err := rc.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: role}); err != nil {
		t.Fatal(err)
	}
	return rc, rc.next()
}

func (r *peerRig) dial(sock, role string) (*rawConn, proto.Frame) {
	r.t.Helper()
	return dialHolder(r.t, sock, role)
}

// session connects to the session socket as the bridge does.
func (r *peerRig) session() *rawConn {
	r.t.Helper()
	rc, f := r.dial(paths.Socket(r.h.id), "bridge")
	if f.Type != proto.THello {
		r.t.Fatalf("the holder answered the hello with %v: %s", f.Type, f.Payload)
	}
	return rc
}

func (rc *rawConn) next() proto.Frame {
	rc.t.Helper()
	f, err := proto.Read(rc.r)
	if err != nil {
		rc.t.Fatalf("read a frame: %v", err)
	}
	return f
}

func (rc *rawConn) request(op string, args any) proto.Response {
	rc.t.Helper()
	req := proto.Request{ID: 7, Op: op}
	req.Args, _ = json.Marshal(args)
	if err := rc.w.WriteJSON(proto.TRequest, req); err != nil {
		rc.t.Fatal(err)
	}
	for {
		f := rc.next()
		if f.Type != proto.TResponse {
			continue
		}
		var resp proto.Response
		if err := json.Unmarshal(f.Payload, &resp); err != nil || resp.ID != 7 {
			rc.t.Fatalf("response %s (%v)", f.Payload, err)
		}
		return resp
	}
}

// ping sends a ping and checks the pong.
func (rc *rawConn) ping() {
	rc.t.Helper()
	if err := rc.w.Write(proto.TPing, []byte("p1")); err != nil {
		rc.t.Fatal(err)
	}
	if f := rc.next(); f.Type != proto.TPong || string(f.Payload) != "p1" {
		rc.t.Errorf("ping: %v %q", f.Type, f.Payload)
	}
}

// closedByHolder reports whether the holder closed the connection.
func (rc *rawConn) closedByHolder() bool {
	_, err := proto.Read(rc.r)
	return err != nil
}

// errorCode returns the code of an error frame, and checks its hint.
func errorCode(t *testing.T, f proto.Frame) string {
	t.Helper()
	var e proto.Error
	if f.Type != proto.TError || json.Unmarshal(f.Payload, &e) != nil {
		t.Fatalf("frame %v %s, want an error", f.Type, f.Payload)
	}
	if e.Message == "" || e.Hint == "" {
		t.Errorf("the error %+v needs a message and a hint", e)
	}
	return e.Code
}

// forbidden checks that a response is the error forbidden, with a hint.
func forbidden(t *testing.T, what string, resp proto.Response) {
	t.Helper()
	if resp.OK || resp.Error == nil || resp.Error.Code != "forbidden" || resp.Error.Hint == "" {
		t.Errorf("%s: got %+v, want forbidden with a hint", what, resp)
	}
}

// untouched checks that the session got no input, kept its name and runs.
func (r *peerRig) untouched() {
	r.t.Helper()
	time.Sleep(100 * time.Millisecond)
	if b, _ := os.ReadFile(r.typed); len(b) > 0 {
		r.t.Errorf("the agent read %q", b)
	}
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	if r.h.exited || r.h.m.Name != "peer" || r.h.ring.End() != int64(len(readyBytes)) || len(r.h.clients) != 0 || r.h.hookSeen {
		r.t.Errorf("the session changed: exited %v, name %q, %d bytes, %d clients, hook seen %v",
			r.h.exited, r.h.m.Name, r.h.ring.End(), len(r.h.clients), r.h.hookSeen)
	}
}

// refusedRequests are every request that a peer without full access gets
// forbidden for. A kill that passed would end this test's process.
var refusedRequests = []struct {
	op   string
	args any
}{
	{"send", map[string]any{"text": "typed", "enter": true}},
	{"kill", nil},
	{"peek", map[string]int{"bytes": 100}},
	{"info", nil},
	{"rename", map[string]string{"name": "taken"}},
	{"ports", nil},
	{"hook", HookEvent{Event: "notification"}},
	{"secrets", secretsMsg{}},
	{"debug.start", nil},
}

// refusedFrames are the frames other than requests that such a peer gets
// forbidden for.
var refusedFrames = []struct {
	t proto.Type
	p string
}{
	{proto.TAttach, `{"cols":80,"rows":24,"last_offset":-1}`},
	{proto.TInput, "typed"},
	{proto.TResize, `{"cols":10,"rows":10}`},
}

// refusesEverything checks that a peer gets hello and ping, and forbidden
// for attach, input, send, kill, peek and every other request.
func (r *peerRig) refusesEverything() {
	r.t.Helper()
	r.session().ping()
	for _, rq := range refusedRequests {
		rc := r.session()
		forbidden(r.t, rq.op, rc.request(rq.op, rq.args))
		if !rc.closedByHolder() {
			r.t.Errorf("%s: the connection stays open after forbidden", rq.op)
		}
	}
	for _, f := range refusedFrames {
		rc := r.session()
		if err := rc.w.Write(f.t, []byte(f.p)); err != nil {
			r.t.Fatal(err)
		}
		if code := errorCode(r.t, rc.next()); code != "forbidden" {
			r.t.Errorf("frame %v: code %q", f.t, code)
		}
		if !rc.closedByHolder() {
			r.t.Errorf("frame %v: the connection stays open after forbidden", f.t)
		}
	}
	r.untouched()
}

func TestAProgramInAnotherSessionIsRefused(t *testing.T) {
	newPeerRig(t, rigPeer{check: true, cgroup: otherCgroup}).refusesEverything()
}

func TestAProgramInItsOwnSessionIsRefusedToo(t *testing.T) {
	newPeerRig(t, rigPeer{check: true, cgroup: ownCgroup}).refusesEverything()
}

func TestAGitScopeIsInside(t *testing.T) {
	newPeerRig(t, rigPeer{check: true, cgroup: gitScopeCgroup}).refusesEverything()
}

func TestAPeerWhoseCgroupCannotBeReadIsInside(t *testing.T) {
	newPeerRig(t, rigPeer{check: true, cgroup: noCgroup}).refusesEverything()
}

func TestAPeerThatCannotBeReadIsInside(t *testing.T) {
	t.Run("no credentials", func(t *testing.T) {
		newPeerRig(t, rigPeer{check: true, cgroup: outsideCgroup,
			read: func(net.Conn) (peer, error) { return peer{}, errors.New("no peer") }}).refusesEverything()
	})
	t.Run("no pid", func(t *testing.T) {
		// SO_PEERCRED gives pid 0 for a peer in a pid namespace that the
		// holder cannot see, and SO_PEERPIDFD can still give a pidfd: it
		// must be closed.
		var closed atomic.Int32
		newPeerRig(t, rigPeer{check: true, cgroup: outsideCgroup, read: func(net.Conn) (peer, error) {
			return peer{pid: 0, uid: os.Getuid(), alive: func() bool { return true }, done: func() { closed.Add(1) }}, nil
		}}).refusesEverything()
		if n := closed.Load(); n < int32(1+len(refusedRequests)+len(refusedFrames)) {
			t.Errorf("%d pidfds closed, want one for each connection", n)
		}
	})
	t.Run("another user", func(t *testing.T) {
		// Root, or another user, cannot pass, wherever its cgroup is.
		newPeerRig(t, rigPeer{check: true, cgroup: outsideCgroup, read: func(net.Conn) (peer, error) {
			return peer{pid: fakePid, uid: os.Getuid() + 1}, nil
		}}).refusesEverything()
	})
}

func TestAReusedPidCannotPass(t *testing.T) {
	// The process that connected is gone: the pid's cgroup file, which says
	// outside, may be another process's now.
	r := newPeerRig(t, rigPeer{check: true, cgroup: outsideCgroup, alive: func() bool { return false }})
	r.refusesEverything()
	if r.closed.Load() == 0 {
		t.Error("the pidfds stay open")
	}
}

// The zero value of access lets nothing through, not even a ping.
// It goes through the holder's own dispatcher, as a connection does.
func TestNoAccessRefusesEverything(t *testing.T) {
	r := newPeerRig(t, rigPeer{cgroup: outsideCgroup})
	for _, f := range []proto.Frame{
		{Type: proto.TPing, Payload: []byte("p")},
		{Type: proto.TInput, Payload: []byte("abcdef")},
		{Type: proto.TRequest, Payload: []byte(`{"id":1,"op":"send","args":{"text":"abcdef"}}`)},
	} {
		var in, out bytes.Buffer
		if err := proto.NewWriter(&in).Write(f.Type, f.Payload); err != nil {
			t.Fatal(err)
		}
		c := &hclient{w: proto.NewWriter(&out), done: make(chan struct{})}
		r.h.dispatch(c, bufio.NewReader(&in))
		if !bytes.Contains(out.Bytes(), []byte(`"forbidden"`)) {
			t.Errorf("frame %v without access got %q", f.Type, out.Bytes())
		}
	}
	r.untouched()
}

// TestAttachFromOutsideKeepsEveryByte attaches through the peer check, from
// a peer outside every session, and checks the bytes both ways: the agent's
// output reaches the client unchanged, and so does the client's input.
func TestAttachFromOutsideKeepsEveryByte(t *testing.T) {
	r := newPeerRig(t, rigPeer{check: true, cgroup: outsideCgroup, alive: func() bool { return true }})
	rc := r.session()
	if err := rc.w.WriteJSON(proto.TAttach, proto.Attach{Session: r.h.id, Cols: 80, Rows: 24, LastOffset: -1}); err != nil {
		t.Fatal(err)
	}
	if f := rc.next(); f.Type != proto.TAttached {
		t.Fatalf("attach: %v %s", f.Type, f.Payload)
	}
	var got []byte
	for len(got) < len(readyBytes) {
		f := rc.next()
		if f.Type != proto.TOutput {
			continue
		}
		off, data, err := proto.DecodeOutput(f.Payload)
		if err != nil || off != int64(len(got)) {
			t.Fatalf("output at %d: %v", off, err)
		}
		got = append(got, data...)
	}
	if string(got) != readyBytes {
		t.Fatalf("output = %q, want %q", got, readyBytes)
	}
	in := []byte("\x1b[A\x03\r\x00")
	if err := rc.w.Write(proto.TInput, in); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		b, _ := os.ReadFile(r.typed)
		return bytes.Equal(b, in)
	})
	if r.closed.Load() == 0 {
		t.Error("the pidfd stays open after the check")
	}
}

func TestAPeerOutsideMayDoEverything(t *testing.T) {
	r := newPeerRig(t, rigPeer{check: true, cgroup: outsideCgroup, alive: func() bool { return true }})
	for _, op := range []string{"info", "peek", "ports"} {
		if resp := r.session().request(op, nil); !resp.OK {
			t.Errorf("%s from outside: %+v", op, resp.Error)
		}
	}
	if resp := r.session().request("rename", map[string]string{"name": "renamed"}); !resp.OK {
		t.Errorf("rename from outside: %+v", resp.Error)
	}
	if resp := r.session().request("send", map[string]any{"text": "abcdef"}); !resp.OK {
		t.Errorf("send from outside: %+v", resp.Error)
	}
	waitUntil(t, func() bool {
		b, _ := os.ReadFile(r.typed)
		return string(b) == "abcdef"
	})
	if n := r.closed.Load(); n < 5 {
		t.Errorf("%d pidfds closed after 5 checks", n)
	}
}

// The session socket takes no hook events, from any peer (spec 10.4).
