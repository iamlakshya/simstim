package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// A fake guest plays the guest init's end of the link, so that a test can
// send the supervisor what a hostile guest would.

// fakeGuest is a sandbox whose guest init is the test.
type fakeGuest struct {
	t       *testing.T
	disk    *diskResourcesV1
	conn    net.Conn // the guest's end of the link
	sess    *link.Session
	holders chan *link.Stream
	done    chan struct{}
	once    sync.Once

	mu    sync.Mutex
	ctl   *proto.Writer
	ctlSt *link.Stream
	ops   []string // the ops that the supervisor sent
}

// fixtureDiskResourcesV1 pins only this local fixture's temporary volume.
// It does not certify native runtime storage, mounts or a private disk cap.
func fixtureDiskResourcesV1(t *testing.T) *diskResourcesV1 {
	t.Helper()
	volume, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resources := &diskResourcesV1{volume: volume}
	t.Cleanup(func() { _ = resources.Close() })
	return resources
}

func (f *fakeGuest) ownedDiskResourcesV1() *diskResourcesV1 { return f.disk }

func (f *fakeGuest) Start(ctx context.Context) (net.Conn, error) {
	ours, theirs, err := guest.SocketPair()
	if err != nil {
		return nil, err
	}
	conn, err := net.FileConn(theirs)
	theirs.Close()
	if err != nil {
		f.t.Fatal(err)
	}
	f.conn = conn
	f.sess = link.New(conn, link.Guest, link.Options{})
	go f.serve(context.WithoutCancel(ctx))
	return ours, nil
}

func (f *fakeGuest) Kill() error {
	f.once.Do(func() {
		_ = f.sess.Close()
		_ = f.conn.Close()
		close(f.done)
	})
	return nil
}

func (f *fakeGuest) Wait() error {
	<-f.done
	return nil
}

// serve takes the supervisor's streams: it answers the ctl ops, and hands
// each holder stream to the test.
func (f *fakeGuest) serve(ctx context.Context) {
	for {
		st, err := f.sess.Accept(ctx)
		if err != nil {
			return
		}
		switch st.Kind() {
		case link.KindCtl:
			go f.serveCtl(st)
		case link.KindHolder:
			f.holders <- st
		default:
			_ = st.Close()
		}
	}
}

func (f *fakeGuest) serveCtl(st *link.Stream) {
	w := proto.NewWriter(st)
	f.mu.Lock()
	f.ctl, f.ctlSt = w, st
	f.mu.Unlock()
	r := bufio.NewReader(st)
	for {
		fr, err := proto.Read(r)
		if err != nil {
			return
		}
		var req proto.Request
		if json.Unmarshal(fr.Payload, &req) != nil {
			continue
		}
		f.mu.Lock()
		f.ops = append(f.ops, req.Op)
		f.mu.Unlock()
		switch req.Op {
		case proto.CtlStart:
			res, _ := json.Marshal(proto.CtlStartResult{AgentPID: 7, GuestABI: proto.GuestPTY1000V1})
			_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res})
		case proto.CtlStop:
			_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{}`)})
			_ = f.Kill()
			return
		}
	}
}

// send sends one event, as the guest init would.
func (f *fakeGuest) send(ev proto.CtlEvent) {
	f.t.Helper()
	f.mu.Lock()
	w := f.ctl
	f.mu.Unlock()
	if err := w.WriteJSON(proto.TEvent, ev); err != nil {
		f.t.Fatal(err)
	}
}

// supervised starts the supervisor of a new walled session whose sandbox
// is a fake guest. It returns the session's ID, the fake guest and the
// supervisor's end, which a kill brings.
func supervised(t *testing.T) (string, *fakeGuest, chan error) {
	t.Helper()
	dir := testFolders(t)
	const id = "abcd2345"
	now := time.Now().UTC()
	m := &host.Manifest{Schema: 1, ID: id, Name: "box", CreatedAt: now, Cwd: dir,
		Agent:  host.AgentInfo{Kind: "claude", Argv: []string{"/bin/sh"}},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall:   &host.WallRecord{Level: "none", Driver: "fake", Version: version.Version, At: now}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	f := &fakeGuest{t: t, disk: fixtureDiskResourcesV1(t), holders: make(chan *link.Stream, 8), done: make(chan struct{})}
	old, oldFake := sandboxFor, testFakeSandbox
	sandboxFor, testFakeSandbox = func(*host.Manifest) (sandbox, error) { return f, nil }, true
	t.Cleanup(func() { sandboxFor, testFakeSandbox = old, oldFake })
	ended, fin := make(chan error, 1), make(chan struct{})
	go func() {
		ended <- Run(id)
		close(fin)
	}()
	t.Cleanup(func() {
		_, _ = host.HolderRequest(id, "kill", nil, time.Second)
		select {
		case <-fin:
		case <-time.After(10 * time.Second):
			t.Error("the supervisor did not stop")
		}
	})
	waitFor(t, "the session's start", func() bool {
		m, err := host.LoadManifest(id)
		return err == nil && m.Status.State == host.StateWorking
	})
	return id, f, ended
}

// waitFor polls cond until it is true, or fails the test after 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// dial connects to the session's socket as the bridge does, and returns
// the connection after the hello.
func dial(t *testing.T, id string) (net.Conn, *bufio.Reader, *proto.Writer) {
	t.Helper()
	conn, err := net.Dial("unix", paths.Socket(id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	w, r := proto.NewWriter(conn), bufio.NewReader(conn)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		t.Fatal(err)
	}
	f, err := proto.Read(r)
	var hello proto.Hello
	if err != nil || f.Type != proto.THello || json.Unmarshal(f.Payload, &hello) != nil || hello.Role != "supervisor" {
		t.Fatalf("hello = %v %s, %v", f.Type, f.Payload, err)
	}
	return conn, r, w
}
