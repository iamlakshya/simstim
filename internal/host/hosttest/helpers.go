package hosttest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
)

// OldName gives the session id the name name in its manifest, as a helper
// before names were checked could: a name that new and rename now refuse,
// such as one that looks like an ID. It stops the session's holder first,
// so that no later save of the holder puts the old name back. The session
// stays, with its agent ended.
func OldName(t testing.TB, id, name string) {
	t.Helper()
	if m, err := host.LoadManifest(id); err == nil && host.IsNoneSupervisor(m) {
		if pid := livePID(id); pid > 0 {
			if _, err := host.StopContext(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if err := Reap(pid, 10*time.Second); err != nil {
				t.Fatal(err)
			}
		}
	} else if pid := livePID(id); pid > 0 {
		_, _ = host.HolderRequest(id, "kill", nil, 3*time.Second)
		if err := Reap(pid, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	m, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	m.Name = name
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
}

// WaitFor polls cond until it is true, or fails the test after timeout.
func WaitFor(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SkipOnGit254 skips a test whose session makes a worktree, or counts the
// changed files of its repository, when this machine's git is 2.54.x: the
// helper refuses that git for a step that fires hooks (spec 19.3).
func SkipOnGit254(t testing.TB) {
	t.Helper()
	out, _ := exec.CommandContext(context.Background(), "git", "version").Output()
	if regexp.MustCompile(`^git version 2\.54(\.|\s|$)`).Match(out) {
		t.Skip("the helper refuses git 2.54 for a step that fires hooks")
	}
}

// Script writes an executable shell script into a folder of the test's own,
// and returns its path.
func (h *Host) Script(name, body string) string {
	h.t.Helper()
	dir := filepath.Join(h.Root, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if !strings.HasPrefix(body, "#!") {
		body = "#!/bin/sh\n" + body
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		h.t.Fatal(err)
	}
	return p
}

// InSession makes this test's process look, to the helper's own check
// (host.CheckCaller), like a program in the unit of session id, until the
// test ends. It overrides New's outside. Holders that the test starts are
// other processes: they keep their own cgroup.
func InSession(t testing.TB, id string) {
	t.Helper()
	uid := os.Getuid()
	cgroup := fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/%s/%s\n", uid, uid, brand.Slice, host.UnitName(id))
	old := host.SelfCgroup
	host.SelfCgroup = func() ([]byte, error) { return []byte(cgroup), nil }
	t.Cleanup(func() { host.SelfCgroup = old })
}
