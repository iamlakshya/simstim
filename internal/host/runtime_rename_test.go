package host_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// A rename and the holder's answers (docs/contributing/cli-spec.md 9.5).

// fakeID is the session of the fake holders.
const fakeID = "abcd2345"

// fakeHolder serves the socket of the session fakeID as a holder of this
// helper would, and answers its nth request (from 1) with answer(n): a
// response, or nil to close the connection without one. When last is true,
// it stops listening after that answer, as a holder that went away.
func fakeHolder(t *testing.T, answer func(n int) (resp *proto.Response, last bool)) {
	t.Helper()
	serveHolder(t, host.HolderFeatures, answer)
}

// serveHolder is fakeHolder for a holder whose hello names features. A
// holder of an older helper names none.
func serveHolder(t *testing.T, features []string, answer func(n int) (resp *proto.Response, last bool)) {
	t.Helper()
	sock := paths.Socket(fakeID)
	if err := paths.EnsurePrivateDir(filepath.Dir(sock)); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r, w := bufio.NewReader(c), proto.NewWriter(c)
				if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
					return // a check that the socket answers
				}
				_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "holder", Features: features})
				f, err := proto.Read(r)
				if err != nil {
					return
				}
				var req proto.Request
				_ = json.Unmarshal(f.Payload, &req)
				resp, last := answer(int(n.Add(1)))
				if last {
					ln.Close()
				}
				if resp != nil {
					resp.ID = req.ID
					_ = w.WriteJSON(proto.TResponse, resp)
				}
			}()
		}
	}()
}

// stoppedSession saves the manifest of the session fakeID, which does not
// run.
func stoppedSession(t *testing.T) {
	t.Helper()
	if err := (&host.Manifest{Schema: 1, ID: fakeID, Name: "a"}).Save(); err != nil {
		t.Fatal(err)
	}
}

// renamed is the error of a rename to b, after which the manifest's name
// must be want.
func renamedTo(t *testing.T, id, want string) error {
	t.Helper()
	err := host.Rename(id, "b")
	if m, lerr := host.LoadManifest(id); lerr != nil || m.Name != want {
		t.Errorf("after the rename: %+v, %v, want the name %s", m, lerr, want)
	}
	return err
}

var ok = &proto.Response{OK: true, Result: json.RawMessage(`{"name":"b"}`)}

// A holder that answers that it is ending, and goes: the rename takes the
// lock, looks again, and renames the stopped session on disk.
func TestARenameWhoseHolderAnswersExiting(t *testing.T) {
	hosttest.New(t)
	const id = fakeID
	stoppedSession(t)
	fakeHolder(t, func(int) (*proto.Response, bool) {
		return &proto.Response{Error: &proto.Error{Code: "exiting", Message: "the session is ending, so it was not renamed"}}, true
	})
	if err := renamedTo(t, id, "b"); err != nil {
		t.Errorf("the rename: %v", err)
	}
}

// A holder that goes away twice: the rename says that the session stopped
// or started again, with busy, and changes nothing.
func TestARenameWhoseHoldersGoTwice(t *testing.T) {
	hosttest.New(t)
	const id = fakeID
	stoppedSession(t)
	fakeHolder(t, func(int) (*proto.Response, bool) { return nil, false })
	err := renamedTo(t, id, "a")
	if codeOf(err) != "busy" || !strings.Contains(err.Error(), "stopped or started again") {
		t.Errorf("the rename: %v", err)
	}
}

// A holder that does not answer: the rename says so after one wait, with
// busy, and does not take it for a holder that went away.
func TestARenameWhoseHolderDoesNotAnswer(t *testing.T) {
	hosttest.New(t)
	const id = fakeID
	stoppedSession(t)
	old := *host.RenameWait
	t.Cleanup(func() { *host.RenameWait = old })
	*host.RenameWait = 100 * time.Millisecond
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	fakeHolder(t, func(int) (*proto.Response, bool) {
		<-hang
		return nil, false
	})
	start := time.Now()
	err := renamedTo(t, id, "a")
	if codeOf(err) != "busy" || !strings.Contains(err.Error(), "did not answer in time") || hintOf(err) == "" {
		t.Errorf("the rename: %v", err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("the rename waited %v: twice", d)
	}
}

// A holder that waits for its secrets answers "starting": the rename waits
// for the start's lock, and looks again.
func TestARenameWhoseHolderIsStarting(t *testing.T) {
	hosttest.New(t)
	const id = fakeID
	stoppedSession(t)
	unlock, err := host.LockSession(id)
	if err != nil {
		t.Fatal(err)
	}
	fakeHolder(t, func(n int) (*proto.Response, bool) {
		if n == 1 {
			go func() {
				time.Sleep(300 * time.Millisecond)
				unlock()
			}()
			return &proto.Response{Error: &proto.Error{Code: "starting", Message: "the session is starting"}}, false
		}
		return ok, false
	})
	if err := host.Rename(id, "b"); err != nil {
		t.Errorf("the rename: %v", err)
	}
}

// The reviewer's probe (review 5, finding 5): a rename during a start that
// waits for its secrets waits for the start, then renames the new holder.
// The holder's last save keeps the name.
func TestARenameDuringAStartWithSecrets(t *testing.T) {
	hosttest.New(t)
	old := *host.Deliver
	t.Cleanup(func() { *host.Deliver = old })
	renamed := make(chan error, 1)
	var id string
	*host.Deliver = func(sid string, s map[string]api.Secret) error {
		id = sid
		go func() { renamed <- host.Rename(sid, "b") }()
		time.Sleep(1500 * time.Millisecond)
		return old(sid, s)
	}
	if _, err := host.New(host.NewArgs{Name: "a", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24,
		Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-renamed:
		if err != nil {
			t.Fatalf("the rename during the start: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the rename never ended")
	}
	pid := hosttest.HolderPID(t, id)
	_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
	if err := hosttest.Reap(pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if m, err := host.LoadManifest(id); err != nil || m.Name != "b" {
		t.Errorf("after the holder's last save: %+v, %v", m, err)
	}
}

// A resume whose secrets cannot reach the new holder stops that holder
// first, as a start does: the agent never starts without its secrets.
func TestAFailedDeliveryOfSecretsStopsTheResume(t *testing.T) {
	h := hosttest.New(t)
	script := `[ -e "$HOME/ran" ] && touch "$HOME/second"; touch "$HOME/ran"; exit 3`
	s, err := host.New(host.NewArgs{Name: "a", Cwd: "~", Argv: []string{"/bin/sh", "-c", script}, Cols: 80, Rows: 24,
		Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s.ID, host.StateErrored)
	old := *host.Deliver
	t.Cleanup(func() { *host.Deliver = old })
	*host.Deliver = func(string, map[string]api.Secret) error { return errors.New("the test drops the secrets") }
	_, err = host.Resume(api.ResumeArgs{ID: s.ID, Secrets: map[string]api.Secret{"TEST_TOKEN": "s3cret"}})
	if err == nil {
		t.Fatal("a resume whose secrets did not arrive succeeded")
	}
	if holderRuns(t, s.ID) {
		t.Error("the new holder still runs, and would start its agent without its secrets")
	}
	if _, err := os.Stat(filepath.Join(h.Home, "second")); err == nil {
		t.Error("the agent started again without its secrets")
	}
}
