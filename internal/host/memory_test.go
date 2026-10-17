package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// meminfo is a /proc/meminfo for a host with total bytes of memory and no
// swap.
func meminfo(total int64) string { return meminfoSwap(total, 0) }

// meminfoSwap is a /proc/meminfo for a host with total bytes of memory and
// swap bytes of swap.
func meminfoSwap(total, swap int64) string {
	return fmt.Sprintf("MemTotal:       %d kB\nMemFree:          123456 kB\nMemAvailable:    234567 kB\n"+
		"SwapCached:            0 kB\nSwapTotal:      %d kB\nSwapFree:       %d kB\n", total/1024, swap/1024, swap/1024)
}

// fakeSystemctl replaces systemctl for the rest of the test. Each call is
// recorded, and gets out and err back.
func fakeSystemctl(t *testing.T, out string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	old := systemctl
	systemctl = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte(out), err
	}
	t.Cleanup(func() { systemctl = old })
	return &calls
}

// fakeCgroup points cgroupRoot at a new folder, for the rest of the test.
// With delegated, the user's manager has the memory controller. With a
// memoryMax, the sessions' slice runs there, with that limit and with
// memoryCurrent in use.
func fakeCgroup(t *testing.T, delegated bool, memoryMax, memoryCurrent string) {
	t.Helper()
	old := cgroupRoot
	cgroupRoot = t.TempDir()
	t.Cleanup(func() { cgroupRoot = old })
	files := map[string]string{}
	if delegated {
		files[filepath.Join(userManagerCgroup(), "cgroup.controllers")] = "cpu io memory pids\n"
	}
	if memoryMax != "" {
		files[filepath.Join(sliceCgroup(), "memory.max")] = memoryMax + "\n"
		files[filepath.Join(sliceCgroup(), "memory.current")] = memoryCurrent + "\n"
	}
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The default cap is the host's memory minus GKE's reserve and a margin of
// 100 MiB: the table in decision 0024.
func TestDefaultMemoryMax(t *testing.T) {
	for _, c := range []struct {
		total, reserve, want int64 // the reserve and the cap in MiB
	}{
		{0, 0, 0}, // not known: no cap
		{512 * mib, 355, 256},
		{1 * gib, 356, 668},
		{2 * gib, 612, 1436},
		{3891 * mib, 1072, 2818}, // a VM sold as 4 GB
		{4 * gib, 1124, 2972},
		{8 * gib, 1943, 6248},
		{16 * gib, 2762, 13621},
		{32 * gib, 3745, 29022},
		{64 * gib, 5711, 59824},
		{256 * gib, 12265, 249878},
	} {
		fakeProc(t, map[string]string{"meminfo": meminfo(c.total)})
		if c.total == 0 {
			fakeProc(t, map[string]string{})
		}
		if got := MemTotal(); got != c.total {
			t.Errorf("MemTotal = %d, want %d", got, c.total)
		}
		if c.total > 0 {
			if got := MemoryReserve(c.total) / mib; got != c.reserve {
				t.Errorf("reserve of %d MiB = %d MiB, want %d MiB", c.total/mib, got, c.reserve)
			}
		}
		if got := DefaultMemoryMax(); got != c.want*mib {
			t.Errorf("cap of %d MiB = %d MiB, want %d MiB", c.total/mib, got/mib, c.want)
		}
	}
}

// A session gets the default when it asks for nothing, and never more than
// all sessions together may use.
func TestSessionMemoryMax(t *testing.T) {
	fakeProc(t, map[string]string{"meminfo": meminfo(4 * gib)})
	for asked, want := range map[int64]int64{0: 2972 * mib, 512 * mib: 512 * mib, 2 * gib: 2 * gib, 3 * gib: 2972 * mib, 64 * gib: 2972 * mib} {
		if got := sessionMemoryMax(asked); got != want {
			t.Errorf("a session that asked for %d MiB gets %d MiB, want %d MiB", asked/mib, got/mib, want/mib)
		}
	}
	// With the host's memory unknown, there is no limit to lower it to.
	fakeProc(t, map[string]string{})
	if got := sessionMemoryMax(64 * gib); got != 64*gib {
		t.Errorf("with no limit, a session that asked for 64 GiB gets %d MiB", got/mib)
	}
}

func TestLimitSessions(t *testing.T) {
	const limit = 2972 * mib // 3116367872 bytes
	set := []string{"set-property", brand.Slice, "MemoryMax=3116367872"}
	if got := sliceLimitArgs(limit); !reflect.DeepEqual(got, set) {
		t.Fatalf("args = %q, want %q", got, set)
	}
	for _, c := range []struct {
		why          string
		delegated    bool
		max, current string // the running slice's memory.max and memory.current, or "" when it does not run
		calls        int
		err          string
	}{
		{"the limit is in place", true, "3116367872", "1073741824", 0, ""},
		{"no slice runs", true, "", "", 1, ""},
		{"the slice has no limit", true, "max", "1073741824", 1, ""},
		{"the slice has an old limit", true, "1610612736", "1073741824", 1, ""},
		{"the slice has a smaller limit, and sessions use more", true, "max", "3221225472", 0, "the sessions use 3072 MiB together, more than the new limit of 2972 MiB, so a later start sets it"},
		{"no memory controller, as on cgroup v1", false, "", "", 0, ""},
		{"no memory controller, and a slice without memory files", false, "max", "0", 0, ""},
	} {
		fakeCgroup(t, c.delegated, c.max, c.current)
		calls := fakeSystemctl(t, "", nil)
		err := limitSessions(limit)
		if len(*calls) != c.calls || (c.calls == 1 && !reflect.DeepEqual((*calls)[0], set)) || (err == nil) != (c.err == "") || (err != nil && err.Error() != c.err) {
			t.Errorf("%s: %v, calls %q", c.why, err, *calls)
		}
	}
	// No limit to set when the host's memory is not known.
	fakeCgroup(t, true, "", "")
	calls := fakeSystemctl(t, "", nil)
	if err := limitSessions(0); err != nil || len(*calls) != 0 {
		t.Errorf("limit 0: %v, calls %q", err, *calls)
	}
	// A failure says what systemctl said.
	fakeSystemctl(t, "Access denied\n", errors.New("exit status 1"))
	if err := limitSessions(limit); err == nil || !strings.Contains(err.Error(), "Access denied") {
		t.Errorf("a failed set-property = %v", err)
	}
}

// A failure to limit the slice goes to the session's log only after the
// holder answers. A start that fails reports its own reason, never the
// slice's.
func TestHolderStarted(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	run, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	t.Setenv("XDG_RUNTIME_DIR", run)
	sliceErr := errors.New("Access denied")
	logged := func(id string) bool {
		b, _ := os.ReadFile(paths.HolderLog(id))
		return strings.Contains(string(b), "could not limit the memory of all sessions")
	}

	// The holder could not start the agent: that is the reason.
	failed := &Manifest{ID: "failedst", Status: Status{State: StateErrored, Reason: "the agent did not start: boom"}}
	if err := failed.Save(); err != nil {
		t.Fatal(err)
	}
	if err := holderStarted(failed.ID, sliceErr, time.Second); err == nil || err.Error() != "the agent did not start: boom" || logged(failed.ID) {
		t.Errorf("a failed start = %v, with the slice's warning logged: %v", err, logged(failed.ID))
	}
	// Nothing answers, and the log is empty.
	if err := holderStarted("silentst", sliceErr, 100*time.Millisecond); err == nil || strings.Contains(err.Error(), "limit") || logged("silentst") {
		t.Errorf("a start that timed out = %v", err)
	}

	// The holder answers: the warning goes to its log.
	const id = "startedx"
	if err := paths.EnsurePrivateDir(filepath.Dir(paths.Socket(id))); err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		t.Fatal(err)
	}
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", paths.Socket(id))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if err := holderStarted(id, sliceErr, 5*time.Second); err != nil || !logged(id) {
		t.Errorf("a start that worked = %v, with the slice's warning logged: %v", err, logged(id))
	}
	b, _ := os.ReadFile(paths.HolderLog(id))
	if got := logText(strings.TrimSpace(string(b))); got != "could not limit the memory of all sessions: Access denied" {
		t.Errorf("the log says %q", got)
	}
}

func TestSessionsMemoryCheck(t *testing.T) {
	fakeProc(t, map[string]string{"meminfo": meminfo(4 * gib)})
	const base = "sessions may use 2.9 GiB together, and 1.1 GiB is kept for the system"
	const noSwap = ". Swap: none, and swap would not give sessions more room"
	fix := "systemctl --user set-property " + brand.Slice + " MemoryMax=3116367872"
	for _, c := range []struct {
		show         string // the limit that systemctl shows
		max, current string // the running slice's memory.max and memory.current, or "" when it does not run
		ok           bool
		detail, fix  string
	}{
		{"3116367872", "3116367872", "1073741824", true, base + noSwap, ""},
		{"3116367872", "", "", true, base + noSwap, ""},
		// The slice does not run: the next start sets the limit, also after
		// the host was resized.
		{"infinity", "", "", true, base + ", from the next start" + noSwap, ""},
		{"1610612736", "", "", true, base + ", from the next start" + noSwap, ""},
		{"infinity", "max", "1073741824", false, "sessions may use all the host's memory together, not 2.9 GiB" + noSwap, fix},
		{"1610612736", "1610612736", "1073741824", false, "sessions may use 1.5 GiB together, not 2.9 GiB" + noSwap, fix},
		// The sessions use more than the new limit: it waits for them.
		{"1610612736", "1610612736", "3221225472", false, "the sessions use 3072 MiB together, more than the 2972 MiB they may use, so their limit waits" + noSwap, "a later start sets the limit once the sessions use less. To set it sooner, delete a session you do not need."},
	} {
		fakeCgroup(t, true, c.max, c.current)
		fakeSystemctl(t, c.show+"\n", nil)
		got := sessionsMemoryCheck()
		if got.OK != c.ok || got.Detail != c.detail || got.Fix != c.fix {
			t.Errorf("with %q, %q and %q: %+v", c.show, c.max, c.current, got)
		}
	}
	fakeProc(t, map[string]string{})
	if got := sessionsMemoryCheck(); got.OK || !strings.Contains(got.Detail, "not known") || strings.Contains(got.Detail, "Swap") {
		t.Errorf("with the memory unknown: %+v", got)
	}
}

// The memory line shows the host's swap, and says that it gives sessions no
// room: they run with MemorySwapMax=0.
func TestSessionsMemoryCheckShowsSwap(t *testing.T) {
	fakeProc(t, map[string]string{"meminfo": meminfoSwap(4*gib, 2*gib)})
	fakeCgroup(t, true, "3116367872", "1073741824")
	fakeSystemctl(t, "3116367872\n", nil)
	if got := swapTotal(); got != 2*gib {
		t.Errorf("swapTotal = %d, want %d", got, int64(2*gib))
	}
	want := "sessions may use 2.9 GiB together, and 1.1 GiB is kept for the system. Swap: 2.0 GiB, which does not give sessions more room"
	if got := sessionsMemoryCheck(); !got.OK || got.Detail != want {
		t.Errorf("with 2 GiB of swap: %+v, want the detail %q", got, want)
	}
	// Swap adds nothing to what sessions may use.
	if got := DefaultMemoryMax(); got != 2972*mib {
		t.Errorf("with 2 GiB of swap, the cap is %d MiB, want 2972 MiB", got/mib)
	}
}

// An agent's stated memory is a requirement for the machine. The doctor
// passes a host with at least that much memory, and warns about a host with
// less. Each line also shows a session's default cap. An agent whose docs
// name no memory, or that is not installed, gets no line.
func TestAgentMemoryChecks(t *testing.T) {
	claude, _ := agents.Find("claude")
	if claude.Memory != 4*agents.GB {
		t.Fatalf("Claude Code asks for %d bytes, want 4 GB", claude.Memory)
	}
	installed := map[string]string{"claude": "/home/dev/.local/bin/claude", "shell": "/bin/bash"}
	for _, a := range agents.Table {
		if a.Memory == 0 {
			installed[a.Kind] = "/usr/bin/" + a.Kind
		}
	}
	const hint = "give the host at least 4 GB (3.7 GiB) of memory. Swap does not count, because sessions do not use it. See docs/how-to/memory.md."
	for _, c := range []struct {
		why    string
		total  int64
		ok     bool
		detail string
	}{
		{"a 4 GiB host", 4 * gib, true, "its docs ask for 4 GB of RAM: this host has 4.0 GiB, and a session may use 2.9 GiB"},
		{"a VM sold as 4 GB", 3891 * mib, true, "its docs ask for 4 GB of RAM: this host has 3.8 GiB, and a session may use 2.8 GiB"},
		{"just at 4 GB", 3815 * mib, true, "its docs ask for 4 GB of RAM: this host has 3.7 GiB, and a session may use 2.7 GiB"},
		{"just below 4 GB", 3814 * mib, false, "its docs ask for 4 GB of RAM: this host has only 3.7 GiB, and a session may use 2.7 GiB"},
		{"a 2 GiB host", 2 * gib, false, "its docs ask for 4 GB of RAM: this host has only 2.0 GiB, and a session may use 1.4 GiB"},
	} {
		// The memory comes from /proc/meminfo, as Doctor reads it.
		fakeProc(t, map[string]string{"meminfo": meminfo(c.total)})
		want := []Check{{Name: "Claude Code memory", OK: c.ok, Detail: c.detail}}
		if !c.ok {
			want[0].Fix = hint
		}
		if got := AgentMemoryChecks(installed, MemTotal(), DefaultMemoryMax()); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.why, got, want)
		}
	}

	// No agent with a value installed, or the memory not known: no lines.
	delete(installed, "claude")
	fakeProc(t, map[string]string{"meminfo": meminfo(1 * gib)})
	if got := AgentMemoryChecks(installed, MemTotal(), DefaultMemoryMax()); len(got) != 0 {
		t.Errorf("only agents whose docs name no memory: %+v", got)
	}
	if got := AgentMemoryChecks(map[string]string{"claude": "/x"}, 0, 0); len(got) != 0 {
		t.Errorf("with the memory unknown: %+v", got)
	}
}
