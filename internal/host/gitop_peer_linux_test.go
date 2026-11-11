package host

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The variables that make TestHelperGitScopeFilter the filter's program.
const (
	attackSock = "GITSCOPE_ATTACK_SOCK"
	attackID   = "GITSCOPE_ATTACK_ID"
	attackOut  = "GITSCOPE_ATTACK_OUT"
)

// A clean filter that a session plants in its repository runs when the
// helper runs git there, in the git scope (spec 19.3). From there, it tries
// to attach to another session, type into it, send to it and kill it. The
// holder's peer check (spec 10.4) refuses each, and the session is
// untouched.
//
// The kernel's part is faked: the fake systemd-run puts the process that it
// starts in the scope that its options name, in a fake /proc, and a process
// is where its nearest parent there is, as with cgroups. A process with no
// such parent counts as outside every session. So the filter is refused only
// when git really ran in a scope under the sessions' slice.
func TestAFilterInTheGitScopeCannotReachASession(t *testing.T) {
	skipOnGit254(t)
	repo := setupRepo(t)
	var mu sync.Mutex
	var placed []string
	r := newPeerRig(t, rigPeer{check: true, cgroup: noCgroup, read: func(c net.Conn) (peer, error) {
		p, err := peerOf(c)
		if err != nil {
			return p, err
		}
		cgroup := outsideCgroup("")
		for pid := p.pid; pid > 1; pid = parentPid(pid) {
			if b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup")); err == nil {
				cgroup = string(b)
				break
			}
		}
		mu.Lock()
		placed = append(placed, strings.TrimSpace(cgroup))
		mu.Unlock()
		self := filepath.Join(procRoot, strconv.Itoa(p.pid), "cgroup")
		if err := os.MkdirAll(filepath.Dir(self), 0o700); err != nil {
			return peer{}, err
		}
		return p, os.WriteFile(self, []byte(cgroup), 0o600)
	}})
	sock := paths.Socket(r.h.id)

	fakeSocket(t, true)
	fakeBin(t, map[string]string{
		"systemctl": "echo running\n",
		"systemd-run": "unit=; slice=\n" +
			"while [ $# -gt 0 ]; do case $1 in --unit=*) unit=${1#--unit=}; shift;; --slice=*) slice=${1#--slice=}; shift;; --*) shift;; *) break;; esac; done\n" +
			"mkdir -p " + procRoot + "/$$\n" +
			"echo \"0::" + userMgr + "$slice/$unit\" > " + procRoot + "/$$/cgroup\n" +
			"exec \"$@\"\n",
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "attempts")
	filter := filepath.Join(t.TempDir(), "filter.sh")
	put(t, filter, fmt.Sprintf("#!/bin/sh\n%s=%q %s=%q %s=%q %q -test.run='^TestHelperGitScopeFilter$' >/dev/null 2>&1\ncat\n",
		attackSock, sock, attackID, r.h.id, attackOut, out, exe), 0o755)
	put(t, filepath.Join(repo, "README"), "app\n", 0o644)
	put(t, filepath.Join(repo, ".gitattributes"), "README filter=planted\n", 0o644)
	for _, args := range [][]string{{"add", "README", ".gitattributes"}, {"commit", "-q", "-m", "planted"}, {"config", "filter.planted.clean", filter}} {
		git2(t, repo, args...)
	}
	// A new time, so that git status reads the file through the filter.
	later := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(filepath.Join(repo, "README"), later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOp(gitOpRepoInfo, repoReq(repo, true)); err != nil {
		t.Fatal(err)
	}

	want := []string{"attach forbidden", "input forbidden", "send forbidden", "kill forbidden"}
	if got := logLines(out); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the filter's attempts = %q, want %q", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	scope := "0::" + userMgr + brand.Slice + "/" + brand.Name + "-git-"
	if len(placed) != len(want) {
		t.Errorf("the holder placed %d peers, want %d: %q", len(placed), len(want), placed)
	}
	for _, c := range placed {
		if !strings.HasPrefix(c, scope) {
			t.Errorf("a peer was at %q, not in a git scope under %s", c, brand.Slice)
		}
	}
	r.untouched()
}

// parentPid is the parent of process pid, from the real /proc, or 0.
func parentPid(pid int) int {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// "pid (comm) state ppid ...": comm may hold spaces and parentheses.
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}

// TestHelperGitScopeFilter is the planted filter's program, and not a test
// of its own: TestAFilterInTheGitScopeCannotReachASession runs it. It tries
// each attempt on a connection of its own, and writes how each ended.
func TestHelperGitScopeFilter(t *testing.T) {
	sock, id, out := os.Getenv(attackSock), os.Getenv(attackID), os.Getenv(attackOut)
	if sock == "" {
		t.Skip("only the planted filter runs this")
	}
	attach, _ := json.Marshal(proto.Attach{Session: id, Cols: 80, Rows: 24, LastOffset: -1})
	send, _ := json.Marshal(proto.Request{ID: 7, Op: "send", Args: json.RawMessage(`{"text":"typed","enter":true}`)})
	kill, _ := json.Marshal(proto.Request{ID: 7, Op: "kill"})
	var lines []string
	for _, a := range []struct {
		what string
		t    proto.Type
		p    []byte
	}{{"attach", proto.TAttach, attach}, {"input", proto.TInput, []byte("typed\r")}, {"send", proto.TRequest, send}, {"kill", proto.TRequest, kill}} {
		lines = append(lines, a.what+" "+attempt(sock, a.t, a.p))
	}
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// attempt says hello to the holder at sock as a bridge, sends one frame, and
// returns the code of the error that answers it, or what else came.
func attempt(sock string, ty proto.Type, payload []byte) string {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return "no connection: " + err.Error()
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	w, r := proto.NewWriter(c), bufio.NewReader(c)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		return "no hello: " + err.Error()
	}
	if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
		return fmt.Sprintf("no hello back: %v", err)
	}
	if err := w.Write(ty, payload); err != nil {
		return "not sent: " + err.Error()
	}
	for {
		f, err := proto.Read(r)
		if err != nil {
			return "closed: " + err.Error()
		}
		var e proto.Error
		var resp proto.Response
		switch {
		case f.Type == proto.TError && json.Unmarshal(f.Payload, &e) == nil:
			return e.Code
		case f.Type == proto.TResponse && json.Unmarshal(f.Payload, &resp) == nil && resp.Error != nil:
			return resp.Error.Code
		case f.Type == proto.TResponse:
			return "OK"
		case f.Type == proto.TAttached:
			return "attached"
		}
	}
}
