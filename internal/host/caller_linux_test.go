package host_test

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// rmAsCaller runs the test binary as a program that runs the helper's rm on
// session id (hosttest's "rm-as-caller"), under the command prefix. It
// returns the process's cgroup file as this test reads it from outside, its
// own view of it, and the result of the rm. It skips when prefix cannot run.
func rmAsCaller(t *testing.T, prefix []string, id, manager string) (outsideView, selfView, result string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := append(append([]string(nil), prefix...), exe, "rm-as-caller", id, manager)
	cmd := exec.Command(argv[0], argv[1:]...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("%q does not run here: %v", argv[:len(prefix)], err)
	}
	r := bufio.NewReader(out)
	line, err := r.ReadString('\n')
	if err != nil {
		_ = cmd.Wait()
		t.Skipf("%q does not run here: %s", argv[:len(prefix)], strings.TrimSpace(stderr.String()))
	}
	selfView = strings.TrimSpace(strings.TrimPrefix(line, "SELF "))
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/cgroup")
	outsideView = strings.TrimSpace(string(b))
	_, _ = io.WriteString(in, "\n")
	rest, _ := io.ReadAll(r)
	_ = cmd.Wait()
	return outsideView, selfView, strings.TrimSpace(strings.TrimPrefix(string(rest), "RESULT "))
}

// A program in a session that moves into a cgroup namespace of its own sees
// itself at "/". It has not left its unit, and the helper's rm refuses it:
// its namespace is not the user manager's.
func TestRmFromANewCgroupNamespaceIsRefused(t *testing.T) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("needs unshare from util-linux")
	}
	hosttest.New(t)
	b := start(t, "b", "sleep", "60")
	ours, err := os.Readlink("/proc/self/ns/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	own, _ := os.ReadFile("/proc/self/cgroup")
	var outsideView, selfView, result string
	for _, prefix := range [][]string{{unshare, "--cgroup"}, {unshare, "--user", "--map-root-user", "--cgroup"}} {
		ok := t.Run(strings.Join(prefix[1:], " "), func(t *testing.T) {
			outsideView, selfView, result = rmAsCaller(t, prefix, b.ID, ours)
		})
		if ok && result != "" {
			break
		}
	}
	if result == "" {
		t.Skip("this system lets no process make a cgroup namespace: neither unshare form ran")
	}
	// From here, the process is where this test is: unshare moved it to no
	// other cgroup. It sees itself at the root of its own namespace.
	if outsideView != strings.TrimSpace(string(own)) || selfView != `"0::/"` {
		t.Errorf("the process sees itself at %s, and is at %q; this test is at %q", selfView, outsideView, strings.TrimSpace(string(own)))
	}
	if !strings.Contains(result, "cgroup namespace") {
		t.Errorf("rm from a new cgroup namespace: %q, want forbidden", result)
	}
	if !holderRuns(t, b.ID) {
		t.Fatal("session b was stopped")
	}
	// Control: the same program in this test's namespace, on a host without
	// a user manager, deletes b.
	if _, _, result := rmAsCaller(t, nil, b.ID, "none"); result != "deleted" {
		t.Errorf("rm in this namespace, without a user manager: %q", result)
	}
}

// holderRuns reports whether session id's holder runs.
func holderRuns(t *testing.T, id string) bool {
	t.Helper()
	list, err := host.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return s.Alive
		}
	}
	return false
}
