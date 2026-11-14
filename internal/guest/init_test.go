package guest_test

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proc/proctest"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The tests of this binary may start it again as a session's holder.
func TestMain(m *testing.M) {
	guest.SetPlainHolderSpawnerForTest(func() (*syscall.SysProcAttr, error) { return &syscall.SysProcAttr{}, nil })
	hosttest.Main(m)
}

const testID = "abcd2345"

// guestInit starts a guest init whose sandbox's root is a folder of the
// test, and returns the supervisor's end of its link.
func guestInit(t *testing.T) (*link.Session, *guest.Init, string) {
	t.Helper()
	h := hosttest.New(t)
	root := filepath.Join(h.Root, "g")
	ours, theirs, err := guest.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.FileConn(theirs)
	theirs.Close()
	if err != nil {
		t.Fatal(err)
	}
	g := guest.New(conn, root, slog.New(slog.DiscardHandler))
	go g.Serve()
	sup := link.New(ours, link.Supervisor, link.Options{})
	t.Cleanup(func() {
		_ = sup.Close()
		select {
		case <-g.Done():
		case <-time.After(10 * time.Second):
			t.Error("the guest init did not end with its link")
		}
	})
	return sup, g, root
}

// ctl opens the ctl stream and returns a function that sends one op and
// returns its answer, and the events that the guest init sends.
func ctl(t *testing.T, sup *link.Session) (func(op string, args any) proto.Response, <-chan proto.CtlEvent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := sup.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := proto.NewWriter(st)
	answers := make(chan proto.Response, 4)
	events := make(chan proto.CtlEvent, 256)
	go func() {
		r := bufio.NewReader(st)
		for {
			f, err := proto.Read(r)
			if err != nil {
				return
			}
			switch f.Type {
			case proto.TResponse:
				var resp proto.Response
				_ = json.Unmarshal(f.Payload, &resp)
				answers <- resp
			case proto.TEvent:
				var ev proto.CtlEvent
				_ = json.Unmarshal(f.Payload, &ev)
				events <- ev
			default:
				// The guest init sends nothing else on the ctl stream.
			}
		}
	}()
	var id uint64
	return func(op string, args any) proto.Response {
		t.Helper()
		id++
		b, _ := json.Marshal(args)
		if err := w.WriteJSON(proto.TRequest, proto.Request{ID: id, Op: op, Args: b}); err != nil {
			t.Fatal(err)
		}
		select {
		case resp := <-answers:
			return resp
		case <-time.After(15 * time.Second):
			t.Fatalf("no answer to %s", op)
		}
		return proto.Response{}
	}, events
}

// The guest init starts the holder in guest mode, with the guest's own
// folders under the sandbox's root, and passes the holder's events on. A
// holder stream reaches the session's holder, and one for another session
// is refused. A stop ends the holder and the guest init.
func TestTheGuestInitStartsAndCarriesTheHolder(t *testing.T) {
	sup, g, root := guestInit(t)
	request, events := ctl(t, sup)
	env := []string{"HOME=" + host.GuestHome, "PATH=/usr/bin:/bin", brand.Env("HOOK_SOCK") + "=" + host.GuestHookSocket(), "TERM=xterm-256color"}
	resp := request(proto.CtlStart, proto.CtlStartArgs{Session: testID, Argv: []string{"/bin/sh", "-c", `echo "HOME=$HOME"; exec sleep 60`},
		Env: env, Cwd: root, Cols: 80, Rows: 24, Secrets: map[string]string{"TEST_TOKEN": "s3cret"}})
	var res proto.CtlStartResult
	if !resp.OK || json.Unmarshal(resp.Result, &res) != nil || res.AgentPID <= 0 {
		t.Fatalf("start = %+v", resp)
	}
	files := guest.Files(root)
	if _, err := os.Stat(files.Hook); err != nil {
		t.Errorf("no hook socket in the sandbox: %v", err)
	}
	m, err := host.LoadManifestFile(files.Manifest)
	if err != nil || !strings.Contains(strings.Join(m.Env, "\n"), "HOME="+filepath.Join(root, host.GuestHome)) {
		t.Fatalf("the holder's manifest = %+v, %v", m, err)
	}
	if strings.Contains(string(mustRead(t, files.Manifest)), "s3cret") {
		t.Error("the holder's manifest holds a secret's value")
	}
	waitEvent(t, events, func(ev proto.CtlEvent) bool { return ev.Event == proto.EventStatus && ev.Session == testID })

	// A stream for the session reaches its holder, both ways.
	waitPeek(t, sup, "HOME="+filepath.Join(root, host.GuestHome))
	// A stream for another session is refused.
	other := holderStream(t, sup, "zzzz2222")
	if _, err := other.Read(make([]byte, 1)); err == nil {
		t.Error("a stream for another session was served")
	}
	// A second start is refused.
	if resp := request(proto.CtlStart, proto.CtlStartArgs{Session: testID, Argv: []string{"true"}}); resp.OK || resp.Error == nil || resp.Error.Hint == "" {
		t.Errorf("a second start = %+v", resp)
	}
	if resp := request(proto.CtlStop, proto.CtlStopArgs{GraceMS: 2000}); !resp.OK {
		t.Fatalf("stop = %+v", resp)
	}
	select {
	case <-g.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the guest init did not end after a stop")
	}
}

// A start that fails says why before the guest init ends.
func TestAFailedStartSaysWhy(t *testing.T) {
	sup, g, root := guestInit(t)
	request, _ := ctl(t, sup)
	if resp := request(proto.CtlStart, proto.CtlStartArgs{Session: "Not An ID"}); resp.OK || resp.Error == nil || resp.Error.Hint == "" {
		t.Fatalf("a start without a valid session = %+v", resp)
	}
	sup2, g2, _ := guestInit(t)
	request2, _ := ctl(t, sup2)
	resp := request2(proto.CtlStart, proto.CtlStartArgs{Session: testID, Argv: []string{filepath.Join(root, "no-such-program")}, Env: []string{"PATH=/usr/bin:/bin"}, Cwd: "/"})
	if resp.OK || resp.Error == nil || resp.Error.Code != "start_failed" || !strings.Contains(resp.Error.Message, "no-such-program") {
		t.Fatalf("a start of a missing program = %+v", resp)
	}
	select {
	case <-g2.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the guest init did not end after its holder did")
	}
	_ = g
}

// waitPeek waits until the session's output, as the holder's peek gives
// it over a holder stream, holds want.
func waitPeek(t *testing.T, sup *link.Session, want string) {
	t.Helper()
	st := holderStream(t, sup, testID)
	defer st.Close()
	w, r := proto.NewWriter(st), bufio.NewReader(st)
	_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"})
	if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
		t.Fatalf("the holder's hello = %v, %v", f.Type, err)
	}
	for seen := ""; !strings.Contains(seen, want); time.Sleep(50 * time.Millisecond) {
		_ = w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "peek"})
		f, err := proto.Read(r)
		if err != nil {
			t.Fatalf("no output %q: %v (last %q)", want, err, seen)
		}
		var resp proto.Response
		var peek struct {
			Data []byte `json:"data"`
		}
		if json.Unmarshal(f.Payload, &resp) != nil || json.Unmarshal(resp.Result, &peek) != nil {
			t.Fatalf("peek = %s", f.Payload)
		}
		seen = string(peek.Data)
	}
}

// Secrets that the trusted side does not have show in the agent's
// terminal, as the holder of a session without a wall says it.
func TestMissingSecretsShowInTheTerminal(t *testing.T) {
	sup, _, root := guestInit(t)
	request, _ := ctl(t, sup)
	resp := request(proto.CtlStart, proto.CtlStartArgs{Session: testID, Argv: []string{"/bin/sh", "-c", "exec sleep 60"},
		Env: []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"}, Cwd: root, Missing: []string{"TEST_TOKEN"}})
	if !resp.OK {
		t.Fatalf("start = %+v", resp)
	}
	waitPeek(t, sup, "the agent starts without its secrets (TEST_TOKEN)")
}

func holderStream(t *testing.T, sup *link.Session, id string) *link.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := sup.Open(ctx, link.KindHolder, map[string]string{"session": id})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetDeadline(time.Now().Add(10 * time.Second))
	return st
}

func waitEvent(t *testing.T, events <-chan proto.CtlEvent, want func(proto.CtlEvent) bool) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if want(ev) {
				return
			}
		case <-timeout:
			t.Fatal("no such event")
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A path in the guest's own folders maps under the sandbox's root, and
// any other stays as it is.
func TestFilesAreUnderTheRoot(t *testing.T) {
	f := guest.Files("/tmp/r")
	if f.Sock != "/tmp/r"+host.GuestRunDir()+"/holder.sock" || f.Hook != "/tmp/r"+host.GuestHookSocket() {
		t.Errorf("files = %+v", f)
	}
	if f := guest.Files(""); f.Hook != host.GuestHookSocket() {
		t.Errorf("in a real wall, files = %+v", f)
	}
}

// When the guest init's output and error pipes have lost their reader, its
// log write on SIGTERM fails with EPIPE instead of killing the process by
// SIGPIPE before its end runs: it still stops the holder and the agent and
// exits with code 0.
func TestTheGuestInitSurvivesABrokenOutputPipe(t *testing.T) {
	h := hosttest.New(t)
	root := filepath.Join(h.Root, "g")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ours, theirs, err := guest.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		ours.Close()
		theirs.Close()
		t.Fatal(err)
	}
	initEnv := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", guest.RootEnv + "=" + root}
	for _, k := range []string{"GORACE", "GOCOVERDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			initEnv = append(initEnv, k+"="+v)
		}
	}
	cmd := exec.CommandContext(context.Background(), exe, "wall-init")
	cmd.Env = initEnv
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		ours.Close()
		theirs.Close()
		pr.Close()
		pw.Close()
		t.Fatal(err)
	}
	theirs.Close()
	pw.Close()
	sup := link.New(ours, link.Supervisor, link.Options{})
	t.Cleanup(func() {
		_ = pr.Close()
		_ = sup.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	request, _ := ctl(t, sup)
	pidf := filepath.Join(h.Root, "init-pipe-agent.pid")
	env := []string{"HOME=" + host.GuestHome, "PATH=/usr/bin:/bin", brand.Env("HOOK_SOCK") + "=" + host.GuestHookSocket(), "TERM=xterm-256color"}
	resp := request(proto.CtlStart, proto.CtlStartArgs{
		Session: testID,
		Argv:    []string{"/bin/sh", "-c", "trap '' HUP TERM; echo $$ > '" + pidf + "'; exec sleep 300"},
		Env:     env,
		Cwd:     root,
		Cols:    80,
		Rows:    24,
	})
	if !resp.OK {
		t.Fatalf("start = %+v", resp)
	}
	var agentPID int
	hosttest.WaitFor(t, 10*time.Second, "agent pid file", func() bool {
		b, err := os.ReadFile(pidf)
		agentPID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && agentPID > 0
	})
	if ph := proctest.Pin(agentPID, "sleep"); ph != nil {
		t.Cleanup(func() {
			_ = ph.Signal(syscall.SIGKILL)
			ph.Close()
		})
	}

	// Close the read end of the output/error pipe so the SIGTERM log write hits EPIPE.
	_ = pr.Close()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wall-init stopped on SIGTERM with broken pipes = %v, want clean exit 0", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("wall-init did not stop within 15s")
	}

	hosttest.WaitFor(t, 10*time.Second, "stubborn agent stopped by wall-init", func() bool {
		return guestProcGone(agentPID)
	})
	saved, err := host.LoadManifestFile(guest.Files(root).Manifest)
	if err != nil || saved.Status.Reason != "stopped with the host" {
		t.Fatalf("saved holder manifest = %+v, %v", saved, err)
	}
}

func guestProcGone(pid int) bool {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return true
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		return i >= 0 && strings.HasPrefix(strings.TrimSpace(s[i+1:]), "Z")
	}
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	st := strings.TrimSpace(string(out))
	return err != nil || st == "" || strings.HasPrefix(st, "Z")
}
