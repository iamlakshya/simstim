package host

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/ring"
	"github.com/iamlakshya/simstim/internal/version"
)

func TestPlaceIn(t *testing.T) {
	unit := UnitName("abcd2345")
	other := UnitName("other234")
	slice := "0::" + userMgr + brand.Slice + "/"
	for _, tc := range []struct {
		name, file string
		want       peerPlace
	}{
		{"the session's own unit", slice + unit + "\n", placeOwn},
		{"below the session's own unit", slice + unit + "/tool\n", placeOwn},
		{"another session's unit", slice + other + "\n", placeInside},
		{"a group named after the session, inside another session's unit", slice + other + "/" + unit + "\n", placeInside},
		{"a slice named again inside another session's unit", slice + other + "/" + brand.Slice + "/" + unit + "\n", placeInside},
		{"a git scope", slice + brand.Name + "-git-0123abcd.scope\n", placeInside},
		{"the slice itself", "0::" + userMgr + brand.Slice + "\n", placeInside},
		{"an SSH login", "0::/user.slice/user-1000.slice/session-3.scope\n", placeOutside},
		{"another unit of the user's", "0::" + userMgr + "app.slice/other.service\n", placeOutside},
		{"a unit that only looks like the session's", "0::" + userMgr + "app.slice/" + unit + "\n", placeOutside},
		{"a v2 root alone", "0::/\n", placeInside},
		{"cgroup v1 and v2, the session's own", "12:memory:/user.slice\n1:name=systemd:" + userMgr + brand.Slice + "/" + unit + "\n" + slice + unit + "\n", placeOwn},
		{"cgroup v1 and v2, another session", "12:memory:/user.slice\n1:name=systemd:" + userMgr + brand.Slice + "/" + other + "\n" + slice + unit + "\n", placeInside},
		{"cgroup v1, an SSH login", "4:memory:/\n1:name=systemd:/user.slice/user-1000.slice/session-3.scope\n", placeOutside},
		// cgroup v1 shows "/" for a process that is exiting, wherever it was.
		{"cgroup v1, only roots, as for a process that exits", "12:memory:/\n4:pids:/\n1:name=systemd:/\n", placeInside},
		// The same, with an unused v2 hierarchy mounted beside it.
		{"cgroup v1 roots beside an unused v2 root", "12:memory:/\n1:name=systemd:/\n0::/\n", placeInside},
		{"empty", "", placeInside},
		{"not a cgroup file", "garbage\nmore: garbage\n", placeInside},
	} {
		if got := placeIn([]byte(tc.file), unit); got != tc.want {
			t.Errorf("%s: place %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The holder turns the peer check on only in its own unit, which a systemd
// user manager gives it, with cgroup v2 or v1.
func TestThePeerCheckIsOnInTheSessionsOwnUnit(t *testing.T) {
	id := "abcd2345"
	for _, tc := range []struct {
		name, self string
		on         bool
	}{
		{"cgroup v2", "0::" + userMgr + brand.Slice + "/" + UnitName(id) + "\n", true},
		{"cgroup v1", "4:memory:/user.slice\n1:name=systemd:" + userMgr + brand.Slice + "/" + UnitName(id) + "\n", true},
		{"another session's unit", "0::" + userMgr + brand.Slice + "/" + UnitName("other234") + "\n", false},
		{"no systemd user manager", "0::/user.slice/user-1000.slice/session-3.scope\n", false},
		{"no cgroup file", "", false},
	} {
		files := map[string]string{}
		if tc.self != "" {
			files["self/cgroup"] = tc.self
		}
		fakeProc(t, files)
		if got := inOwnUnit(id); got != tc.on {
			t.Errorf("%s: peer check %v, want %v", tc.name, got, tc.on)
		}
	}
}

// Only ENOPROTOOPT, from a kernel before Linux 6.5, lets a peer pass without
// a pidfd. Every other error refuses it.
func TestPidfdAnswer(t *testing.T) {
	for _, tc := range []struct {
		err           error
		pidfd, refuse bool
	}{
		{nil, true, false},
		{syscall.ENOPROTOOPT, false, false},
		{fmt.Errorf("getsockopt: %w", syscall.ENOPROTOOPT), false, false},
		{syscall.EINVAL, false, true},
		{syscall.ENODATA, false, true},
		{syscall.EMFILE, false, true},
	} {
		if pidfd, refuse := pidfdAnswer(tc.err); pidfd != tc.pidfd || refuse != tc.refuse {
			t.Errorf("%v: pidfd %v, refuse %v", tc.err, pidfd, refuse)
		}
	}
	// Only ESRCH says that the process has gone.
	for err, want := range map[error]bool{nil: true, syscall.ESRCH: false, syscall.EPERM: true} {
		if got := pidfdAlive(err); got != want {
			t.Errorf("alive after %v: %v", err, got)
		}
	}
}

func TestSocketsCheck(t *testing.T) {
	c := socketsCheck(true, nil)
	if !c.OK || c.Name != "session sockets" || !strings.Contains(c.Detail, "systemd-run --user") || !strings.Contains(c.Detail, "cgroup.procs") {
		t.Errorf("with systemd: %+v", c)
	}
	c = socketsCheck(false, nil)
	if c.OK || !strings.HasPrefix(c.Detail, "weak") || !strings.Contains(c.Fix, "systemd") {
		t.Errorf("without systemd: %+v", c)
	}
	c = socketsCheck(true, []string{"app (abcd2345)"})
	if c.OK || !strings.HasPrefix(c.Detail, "weak: 1 session still runs") || !strings.Contains(c.Detail, "app (abcd2345)") || !strings.HasPrefix(c.Fix, "resume them") {
		t.Errorf("with an older holder: %+v", c)
	}
}

// session makes a session's folder and manifest, with the name.
func sessionFolder(t *testing.T, name string) string {
	t.Helper()
	id := NewID()
	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		t.Fatal(err)
	}
	if err := (&Manifest{Schema: 1, ID: id, Name: name}).Save(); err != nil {
		t.Fatal(err)
	}
	return id
}

// serveHolder starts a holder in this process for the session id, with its
// sockets open and its peer check as check, and serves its session socket.
func serveHolder(t *testing.T, id string, check bool) {
	t.Helper()
	m, err := LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	h := &Holder{id: id, m: m, log: slog.New(slog.DiscardHandler), ring: ring.New(1 << 16), clients: map[*hclient]struct{}{}}
	if err := h.listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.closeSockets)
	h.peerCheck = check
	go h.acceptLoop(h.ln, false)
}

// serveOlderHolder stands in for a holder from an older helper at sock: it
// answers the hello, and "unknown request" to any request.
func serveOlderHolder(t *testing.T, sock string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r, w := bufio.NewReader(c), proto.NewWriter(c)
				if _, err := proto.Read(r); err != nil {
					return
				}
				_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "holder"})
				f, err := proto.Read(r)
				if err != nil {
					return
				}
				var req proto.Request
				_ = json.Unmarshal(f.Payload, &req)
				_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: &proto.Error{Code: "unknown_op", Message: "unknown request " + req.Op}})
			}()
		}
	}()
}

// The doctor asks each running holder whether it checks its peers. Neither
// a hook socket file nor the host's systemd says so.
func TestTheDoctorAsksEachHolder(t *testing.T) {
	secretHome(t)
	fakePeer(t, outsideCgroup(""), nil) // the doctor runs outside every session
	// A holder with the peer check.
	serveHolder(t, sessionFolder(t, "checked"), true)
	// A holder that started without a systemd user manager. The manager
	// came back since, but this holder still runs without the check.
	unchecked := sessionFolder(t, "unchecked")
	serveHolder(t, unchecked, false)
	// A holder from an older helper, beside a hook socket that a newer
	// holder of the same session left behind, as after a rollback.
	old := sessionFolder(t, "before")
	for _, sock := range []string{paths.Socket(old), paths.HookSocket(old)} {
		if err := paths.EnsurePrivateDir(filepath.Dir(sock)); err != nil {
			t.Fatal(err)
		}
	}
	serveOlderHolder(t, paths.Socket(old))
	stale, err := net.Listen("unix", paths.HookSocket(old))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stale.Close() })
	// A session whose holder has stopped is no concern.
	sessionFolder(t, "stopped")

	got := weakHolders()
	want := []string{"before (" + old + ", an older helper)", "unchecked (" + unchecked + ", started without systemd's user manager)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("weak holders = %q, want %q", got, want)
	}
	if c := socketsCheck(true, got); c.OK || !strings.HasPrefix(c.Detail, "weak: 2 sessions still run") {
		t.Errorf("the doctor calls the sockets protected: %+v", c)
	}
}

func TestWithHookSocket(t *testing.T) {
	env := withHookSocket([]string{"PATH=/bin", brand.Env("SOCK") + "=/old.sock", brand.Env("SESSION") + "=abcd2345", brand.Env("HOOK_SOCK") + "=/stale.sock"}, "abcd2345")
	want := []string{"PATH=/bin", brand.Env("SESSION") + "=abcd2345", brand.Env("HOOK_SOCK") + "=" + paths.HookSocket("abcd2345")}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Errorf("env = %q, want %q", env, want)
	}
}

// The hook socket fits wherever the session socket fits: a holder that can
// open its session socket at the longest path the system allows opens its
// hook socket too.
func TestTheHookSocketFitsWhereTheSessionSocketFits(t *testing.T) {
	alertHome(t)
	most := len(syscall.RawSockaddrUnix{}.Path) - 1 // 107 on Linux, 103 on macOS
	id := NewID()
	base, err := os.MkdirTemp("/tmp", "sl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	tail := len("/" + brand.Name + "/s/" + id + ".sock")
	runtime := filepath.Join(base, strings.Repeat("r", most-tail-len(base)-1))
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	if n := len(paths.Socket(id)); n != most {
		t.Fatalf("the session socket's path has %d bytes, want %d", n, most)
	}
	if len(paths.HookSocket(id)) > len(paths.Socket(id)) {
		t.Fatalf("the hook socket %s is longer than the session socket %s", paths.HookSocket(id), paths.Socket(id))
	}
	h := &Holder{id: id, log: slog.New(slog.DiscardHandler)}
	if err := h.listen(); err != nil {
		t.Fatalf("at the longest path: %v", err)
	}
	h.closeSockets()
	// One byte more, and the session socket itself does not fit: the limit
	// above is the system's.
	longer := runtime + "x"
	if err := os.Mkdir(longer, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", longer)
	h = &Holder{id: id, log: slog.New(slog.DiscardHandler)}
	if err := h.listen(); err == nil {
		h.closeSockets()
		t.Fatal("a path one byte over the limit worked")
	}
}

// bridgeConn runs the bridge in this process, over pipes, and says hello.
type bridgeConn struct {
	t *testing.T
	w *proto.Writer
	r *bufio.Reader
}

func newBridgeConn(t *testing.T) *bridgeConn {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})
	go func() {
		_ = RunBridge(inR, outW)
		_ = outW.Close()
		close(done)
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		<-done
	})
	b := &bridgeConn{t: t, w: proto.NewWriter(inW), r: bufio.NewReader(outR)}
	if err := b.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client"}); err != nil {
		t.Fatal(err)
	}
	if f, err := proto.Read(b.r); err != nil || f.Type != proto.THello {
		t.Fatalf("hello: %v %v", f.Type, err)
	}
	return b
}

// next reads the next frame of type ft, with a deadline.
func (b *bridgeConn) next(ft proto.Type) proto.Frame {
	b.t.Helper()
	got := make(chan proto.Frame, 1)
	go func() {
		for {
			f, err := proto.Read(b.r)
			if err != nil {
				close(got)
				return
			}
			if f.Type == ft {
				got <- f
				return
			}
		}
	}()
	select {
	case f, ok := <-got:
		if !ok {
			b.t.Fatalf("the bridge ended before a frame %v", ft)
		}
		return f
	case <-time.After(10 * time.Second):
		b.t.Fatalf("no frame %v from the bridge", ft)
	}
	return proto.Frame{}
}

func (b *bridgeConn) request(op string, args any) proto.Response {
	b.t.Helper()
	req := proto.Request{ID: 9, Op: op}
	req.Args, _ = json.Marshal(args)
	if err := b.w.WriteJSON(proto.TRequest, req); err != nil {
		b.t.Fatal(err)
	}
	var resp proto.Response
	if err := json.Unmarshal(b.next(proto.TResponse).Payload, &resp); err != nil {
		b.t.Fatal(err)
	}
	return resp
}

// A program in a session can run the helper's bridge itself. That bridge
// refuses every request that acts on a session, and every attach: it would
// stop and remove sessions through systemd, past their sockets.
func TestTheBridgeInsideASessionRefusesToActOnASession(t *testing.T) {
	alertHome(t)
	fakeProc(t, map[string]string{"self/cgroup": ownCgroup("abcd2345")})
	b := newBridgeConn(t)
	for op := range sessionOps {
		forbidden(t, op+" from a bridge inside a session", b.request(op, map[string]string{"id": "abcd2345", "text": "x", "name": "x"}))
	}
	if err := b.w.WriteJSON(proto.TAttach, proto.Attach{Session: "abcd2345", Cols: 80, Rows: 24, LastOffset: -1}); err != nil {
		t.Fatal(err)
	}
	if code := errorCode(t, b.next(proto.TError)); code != "forbidden" {
		t.Errorf("attach from a bridge inside a session: %q", code)
	}
	// What acts on no session still works.
	if resp := b.request("ls", nil); !resp.OK {
		t.Errorf("ls from a bridge inside a session: %+v", resp.Error)
	}
}

func TestTheBridgeOutsideEverySessionActsOnSessions(t *testing.T) {
	alertHome(t)
	fakeProc(t, map[string]string{"self/cgroup": outsideCgroup("")})
	userManager(t, "cgroup:[1]", "cgroup:[1]", nil)
	b := newBridgeConn(t)
	if resp := b.request("delete", map[string]string{"id": "abcd2345"}); resp.OK || resp.Error == nil || resp.Error.Code != "not_found" {
		t.Errorf("delete of no session from outside: %+v", resp.Error)
	}
}

// On Linux, a helper that cannot read or parse its own cgroup file counts
// as inside a session, as a peer does. Off Linux, there are no sessions'
// units, and it acts.
func TestTheHelperFailsClosedWhenItCannotPlaceItself(t *testing.T) {
	alertHome(t)
	old := selfCgroupRequired
	t.Cleanup(func() { selfCgroupRequired = old })
	selfCgroupRequired = true
	for name, files := range map[string]map[string]string{
		"a missing file":       {},
		"an unreadable file":   {"self/cgroup/x": "a folder, not a file"},
		"a file with no lines": {"self/cgroup": "garbage\n"},
		"a session's unit":     {"self/cgroup": ownCgroup("abcd2345")},
		"a git scope":          {"self/cgroup": gitScopeCgroup("")},
	} {
		fakeProc(t, files)
		if err := CheckCaller(); !errors.Is(err, errCallerInside) {
			t.Errorf("%s: %v, want forbidden", name, err)
		}
		if _, err := Delete("abcd2345"); !errors.Is(err, errCallerInside) {
			t.Errorf("%s: delete gave %v, want forbidden", name, err)
		}
	}
	// The bridge refuses too, with a missing file.
	fakeProc(t, map[string]string{})
	forbidden(t, "delete from a bridge that cannot place itself", newBridgeConn(t).request("delete", map[string]string{"id": "abcd2345"}))
	// Without a user manager, as on a Docker host, there are no sessions'
	// units: this process lives, so roots only, even beside an unused v2
	// root, say that it is outside.
	userManager(t, "cgroup:[1]", "", nil)
	for _, self := range []string{"0::/\n", "1:name=systemd:/\n0::/\n", outsideCgroup("")} {
		fakeProc(t, map[string]string{"self/cgroup": self})
		if err := CheckCaller(); err != nil {
			t.Errorf("%q: %v", self, err)
		}
	}
	// Off Linux, a missing file means no sessions' units.
	selfCgroupRequired = false
	fakeProc(t, map[string]string{})
	if err := CheckCaller(); err != nil {
		t.Errorf("off Linux, with no cgroup file: %v", err)
	}
}

// userManager sets what CheckCaller reads of the cgroup namespaces: this
// process's, and the user manager's ("" for no manager), or err.
func userManager(t *testing.T, self, manager string, err error) {
	t.Helper()
	old := CgroupNamespaces
	CgroupNamespaces = func() (string, string, error) { return self, manager, err }
	t.Cleanup(func() { CgroupNamespaces = old })
}

// A cgroup namespace shows a process in a session's unit at "/". So where
// the user has a systemd user manager, the helper must share its cgroup
// namespace, and a cgroup file of roots only refuses as well.
func TestTheHelperNeedsTheManagersCgroupNamespace(t *testing.T) {
	alertHome(t)
	old := selfCgroupRequired
	t.Cleanup(func() { selfCgroupRequired = old })
	selfCgroupRequired = true
	for _, tc := range []struct {
		name, self, ns, manager string
		err                     error
		want                    error
	}{
		{"a login shell", outsideCgroup(""), "cgroup:[1]", "cgroup:[1]", nil, nil},
		{"a namespace of its own, at its root", "0::/\n", "cgroup:[2]", "cgroup:[1]", nil, errCallerNamespace},
		{"a namespace of its own, below its root", "0::/tool\n", "cgroup:[2]", "cgroup:[1]", nil, errCallerNamespace},
		{"roots only, in the manager's namespace", "0::/\n", "cgroup:[1]", "cgroup:[1]", nil, errCallerInside},
		{"v1 roots beside a v2 root, in the manager's namespace", "1:name=systemd:/\n0::/\n", "cgroup:[1]", "cgroup:[1]", nil, errCallerInside},
		{"namespaces that cannot be read", outsideCgroup(""), "", "", errors.New("no /proc"), errCallerNamespace},
		{"no user manager, at the root", "0::/\n", "cgroup:[2]", "", nil, nil},
	} {
		fakeProc(t, map[string]string{"self/cgroup": tc.self})
		userManager(t, tc.ns, tc.manager, tc.err)
		if err := CheckCaller(); !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if tc.want == nil {
			continue
		}
		if _, err := Delete("abcd2345"); !errors.Is(err, tc.want) {
			t.Errorf("%s: delete gave %v, want %v", tc.name, err, tc.want)
		}
	}
}
