package guest

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestParseMicroVMFlags(t *testing.T) {
	cfg := parseMicroVMFlags([]string{"--keep-system", "--sudo", "--docker"})
	if cfg.SelfTest || !cfg.KeepSystem || !cfg.Sudo || !cfg.Docker {
		t.Fatalf("parseMicroVMFlags = %+v", cfg)
	}
	st := parseMicroVMFlags([]string{"--self-test"})
	if !st.SelfTest || st.KeepSystem || st.Sudo || st.Docker {
		t.Fatalf("parseMicroVMFlags(--self-test) = %+v", st)
	}
}

func TestAllAgentStateDirs(t *testing.T) {
	dirs := allAgentStateDirs()
	for _, want := range []string{".claude/projects", ".codex/sessions", ".local/share/opencode"} {
		if !slices.Contains(dirs, want) {
			t.Errorf("allAgentStateDirs() missing %q: %v", want, dirs)
		}
	}
	if !slices.IsSorted(dirs) {
		t.Errorf("allAgentStateDirs() not sorted: %v", dirs)
	}
}

func TestConfigureGuestEtcAndSudoers(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "passwd"), []byte("root:x:0:0:root:/root:/bin/bash\nnobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "group"), []byte("root:x:0:\ntty:x:5:\ndocker:x:988:existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "sudoers"), []byte("root ALL=(ALL:ALL) ALL\n"), 0o440); err != nil {
		t.Fatal(err)
	}

	gid, err := configureGuestEtc(root, vmBootConfig{Sudo: true, Docker: true})
	if err != nil {
		t.Fatalf("configureGuestEtc(sudo+docker): %v", err)
	}
	if gid != 988 {
		t.Errorf("dockerGID = %d, want existing 988", gid)
	}

	passwd, _ := os.ReadFile(filepath.Join(etc, "passwd"))
	for _, want := range []string{"holder:x:900:900:", "agent:x:1000:1000:"} {
		if !strings.Contains(string(passwd), want) {
			t.Errorf("/etc/passwd missing %q:\n%s", want, passwd)
		}
	}
	group, _ := os.ReadFile(filepath.Join(etc, "group"))
	for _, want := range []string{"holder:x:900:", "agent:x:1000:", "docker:x:988:existing,agent"} {
		if !strings.Contains(string(group), want) {
			t.Errorf("/etc/group missing %q:\n%s", want, group)
		}
	}
	resolv, _ := os.ReadFile(filepath.Join(etc, "resolv.conf"))
	if strings.TrimSpace(string(resolv)) != "nameserver 127.0.0.53" {
		t.Errorf("/etc/resolv.conf = %q", resolv)
	}
	sudoAgent, err := os.ReadFile(filepath.Join(etc, "sudoers.d", "agent"))
	if err != nil {
		t.Fatalf("read /etc/sudoers.d/agent: %v", err)
	}
	if string(sudoAgent) != "agent ALL=(ALL:ALL) NOPASSWD: ALL\n" {
		t.Errorf("sudoers.d/agent = %q", sudoAgent)
	}
	fi, _ := os.Stat(filepath.Join(etc, "sudoers.d", "agent"))
	if fi.Mode().Perm() != 0o440 {
		t.Errorf("sudoers.d/agent mode = %o, want 0440", fi.Mode().Perm())
	}
	sudoers, _ := os.ReadFile(filepath.Join(etc, "sudoers"))
	if !strings.Contains(string(sudoers), "@includedir /etc/sudoers.d") {
		t.Errorf("/etc/sudoers missing @includedir:\n%s", sudoers)
	}

	// Turning sudo off must remove /etc/sudoers.d/agent (important with --keep-system).
	if _, err := configureGuestEtc(root, vmBootConfig{Sudo: false, Docker: false}); err != nil {
		t.Fatalf("configureGuestEtc(no sudo): %v", err)
	}
	if _, err := os.Stat(filepath.Join(etc, "sudoers.d", "agent")); !os.IsNotExist(err) {
		t.Errorf("expected /etc/sudoers.d/agent to be removed when sudo=false, err=%v", err)
	}
}

func TestEnsureGroupEntriesAllocatesDockerGID(t *testing.T) {
	out, gid := ensureGroupEntries("root:x:0:\ntaken:x:999:\n", true)
	if gid != 998 {
		t.Fatalf("allocated docker GID = %d, want 998 (avoiding collision with 999)", gid)
	}
	if !strings.Contains(out, "docker:x:998:agent") {
		t.Errorf("group output missing docker:x:998:agent:\n%s", out)
	}
}

func TestReadGuestMem(t *testing.T) {
	dir := t.TempDir()
	meminfo := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(meminfo, []byte("MemTotal:        1048576 kB\nMemFree:          262144 kB\nMemAvailable:     524288 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.events"), []byte("low 0\nhigh 1\noom_kill 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := readGuestMem(meminfo, dir)
	if !ok {
		t.Fatal("readGuestMem returned !ok")
	}
	if got.Total != 1048576*1024 || got.Available != 524288*1024 || got.Used != 524288*1024 || got.OOMKills != 3 {
		t.Fatalf("readGuestMem = %+v", got)
	}
	if _, ok := readGuestMem(filepath.Join(dir, "missing"), dir); ok {
		t.Fatal("expected missing meminfo to return !ok")
	}
}

type fakeTransparentOpener struct {
	gotHdrCh chan egress.StreamHeader
	sess     *link.Session
	peer     *link.Session
}

func (f *fakeTransparentOpener) OpenEgress(ctx context.Context, hdr egress.StreamHeader) (*link.Stream, error) {
	select {
	case f.gotHdrCh <- hdr:
	default:
	}
	return f.sess.Open(ctx, link.KindEgress, hdr)
}

func TestTransparentProxyForwardAndLoopGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	guestSess := link.New(c1, link.Guest, link.Options{})
	supSess := link.New(c2, link.Supervisor, link.Options{})
	defer guestSess.Close()
	defer supSess.Close()

	opener := &fakeTransparentOpener{
		gotHdrCh: make(chan egress.StreamHeader, 4),
		sess:     guestSess,
		peer:     supSess,
	}

	// Echo server on supervisor side for KindEgress streams.
	go func() {
		for {
			st, err := supSess.Accept(ctx)
			if err != nil {
				return
			}
			go func(s *link.Stream) {
				defer s.Close()
				_, _ = io.Copy(s, s)
			}(st)
		}
	}()

	t.Run("forwards non-loopback original destination", func(t *testing.T) {
		wantDst := netip.MustParseAddrPort("198.18.0.42:443")
		tp, err := listenTransparentWith(ctx, opener, "127.0.0.1:0", func(net.Conn) (netip.AddrPort, error) {
			return wantDst, nil
		})
		if err != nil {
			t.Fatalf("listenTransparentWith: %v", err)
		}
		defer tp.Close()
		go tp.Serve(ctx)

		conn, err := net.DialTimeout("tcp", tp.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatalf("dial transparent proxy: %v", err)
		}
		defer conn.Close()

		msg := "hello-transparent"
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.(*net.TCPConn).CloseWrite()
		got, err := io.ReadAll(conn)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != msg {
			t.Fatalf("echo = %q, want %q", got, msg)
		}
		select {
		case hdr := <-opener.gotHdrCh:
			if hdr.Via != "transparent" || hdr.Host != "198.18.0.42" || hdr.Port != 443 {
				t.Fatalf("stream header = %+v", hdr)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for StreamHeader")
		}
	})

	t.Run("loop guard refuses loopback original destination", func(t *testing.T) {
		tp, err := listenTransparentWith(ctx, opener, "127.0.0.1:0", func(net.Conn) (netip.AddrPort, error) {
			return netip.MustParseAddrPort("127.0.0.1:3129"), nil
		})
		if err != nil {
			t.Fatalf("listenTransparentWith: %v", err)
		}
		defer tp.Close()
		go tp.Serve(ctx)

		conn, err := net.DialTimeout("tcp", tp.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 16)
		if n, err := conn.Read(buf); n > 0 || err == nil {
			t.Fatalf("expected immediate EOF from loop guard, got n=%d err=%v", n, err)
		}
		select {
		case hdr := <-opener.gotHdrCh:
			t.Fatalf("loop guard unexpectedly opened egress stream: %+v", hdr)
		default:
		}
	})
}

func TestStartHolderDockerMissingAndMemClaims(t *testing.T) {
	// The memory report reads /proc/meminfo, which only Linux has, and the
	// guest init runs only on Linux.
	if runtime.GOOS != "linux" {
		t.Skip("the guest's memory report needs Linux")
	}
	oldHB := HeartbeatInterval
	HeartbeatInterval = 30 * time.Millisecond
	t.Cleanup(func() { HeartbeatInterval = oldHB })

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	// A frame that never comes fails the test, rather than hanging it.
	if err := c2.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}

	runDir := t.TempDir()
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunConnWithOptions(c1, runDir, RunOptions{
			ReportMem:    true,
			Docker:       true,
			DockerdPaths: []string{filepath.Join(runDir, "no-dockerd")},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sup := link.New(c2, link.Supervisor, link.Options{})
	defer sup.Close()

	ctl, err := sup.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatalf("open ctl: %v", err)
	}
	defer ctl.Close()

	w := proto.NewWriter(ctl)
	rd := bufio.NewReader(ctl)

	reqBytes, _ := json.Marshal(StartReq{Session: "dock1", Agent: "shell", Argv: []string{"/bin/sh"}})
	if err := w.WriteJSONCtx(ctx, proto.TRequest, proto.Request{ID: 1, Op: "start", Args: reqBytes}); err != nil {
		t.Fatalf("write start: %v", err)
	}

	var gotDockerErr, gotHeartbeat, gotMem bool
	for !gotDockerErr || !gotHeartbeat || !gotMem {
		f, err := proto.Read(rd)
		if err != nil {
			t.Fatalf("read ctl frame: %v", err)
		}
		switch f.Type {
		case proto.TResponse:
			var resp proto.Response
			if err := json.Unmarshal(f.Payload, &resp); err != nil {
				t.Fatalf("decode resp: %v", err)
			}
			if resp.OK || resp.Error == nil || resp.Error.Code != "docker_missing" || !strings.Contains(resp.Error.Hint, "--image") {
				t.Fatalf("unexpected start response: %+v", resp)
			}
			gotDockerErr = true
		case proto.TEvent:
			var ev Event
			if err := json.Unmarshal(f.Payload, &ev); err != nil {
				continue
			}
			if ev.Type == "heartbeat" {
				gotHeartbeat = true
			}
			if ev.Type == "mem" && ev.MemTotal > 0 {
				gotMem = true
			}
		default:
		}
	}
	_ = sup.Close()
	<-errCh
}

func TestEnsureAgentsPath(t *testing.T) {
	binDir := t.TempDir()

	// Prepends when PATH does not contain binDir.
	env := ensureAgentsPath([]string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/agent"}, binDir)
	if got := envLookup(env, "PATH"); got != binDir+":/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("PATH = %q, want %q", got, binDir+":/usr/local/bin:/usr/bin:/bin")
	}

	// Moves binDir to the front without duplicating when present later.
	env = ensureAgentsPath([]string{"PATH=/usr/bin:" + binDir + ":/bin"}, binDir)
	if got := envLookup(env, "PATH"); got != binDir+":/usr/bin:/bin" {
		t.Fatalf("deduped PATH = %q, want %q", got, binDir+":/usr/bin:/bin")
	}

	// Idempotent when already first.
	env = ensureAgentsPath([]string{"PATH=" + binDir + ":/usr/bin"}, binDir)
	if got := envLookup(env, "PATH"); got != binDir+":/usr/bin" {
		t.Fatalf("idempotent PATH = %q", got)
	}

	// Sets default PATH when PATH is absent.
	env = ensureAgentsPath([]string{"HOME=/home/agent"}, binDir)
	if got := envLookup(env, "PATH"); got != binDir+":/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("empty PATH = %q", got)
	}

	// Leaves PATH untouched when binDir does not exist.
	missing := filepath.Join(t.TempDir(), "missing")
	env = ensureAgentsPath([]string{"PATH=/usr/bin:/bin"}, missing)
	if got := envLookup(env, "PATH"); got != "/usr/bin:/bin" {
		t.Fatalf("missing dir modified PATH = %q", got)
	}
}

func TestTransparentListenAddrAndLoopGuard(t *testing.T) {
	if got := transparentListenAddr(false); got != DefaultTransparentAddr {
		t.Fatalf("transparentListenAddr(false) = %q, want %q", got, DefaultTransparentAddr)
	}
	if got := transparentListenAddr(true); got != DockerTransparentAddr {
		t.Fatalf("transparentListenAddr(true) = %q, want %q", got, DockerTransparentAddr)
	}

	dockerBridgeIP := netip.AddrFrom4([4]byte{172, 17, 0, 1})
	localBridgeAddr := &net.TCPAddr{IP: dockerBridgeIP.AsSlice(), Port: DefaultTransparentPort}
	if !shouldRefuseTransparentDst(netip.AddrPortFrom(dockerBridgeIP, DefaultTransparentPort), localBridgeAddr, DefaultTransparentPort) {
		t.Error("expected loop guard to refuse docker bridge LocalAddr on proxy port")
	}
	if !shouldRefuseTransparentDst(netip.AddrPortFrom(dockerBridgeIP, DefaultTransparentPort), nil, DefaultTransparentPort) {
		t.Error("expected loop guard to refuse private IP on proxy port")
	}
	if !shouldRefuseTransparentDst(netip.AddrPortFrom(netip.MustParseAddr(DummyIfaceIP), DefaultTransparentPort), nil, DefaultTransparentPort) {
		t.Error("expected loop guard to refuse DummyIfaceIP on proxy port")
	}
	if shouldRefuseTransparentDst(netip.MustParseAddrPort("198.18.0.42:443"), localBridgeAddr, DefaultTransparentPort) {
		t.Error("loop guard unexpectedly refused synthetic egress destination")
	}
	if shouldRefuseTransparentDst(netip.MustParseAddrPort("198.18.0.42:3129"), localBridgeAddr, DefaultTransparentPort) {
		t.Error("loop guard unexpectedly refused synthetic egress destination on port 3129")
	}
}
