package host

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestParseHexAddr(t *testing.T) {
	cases := map[string]struct {
		addr string
		port int
	}{
		"0100007F:0BB8":                         {"127.0.0.1", 3000},
		"00000000:1F90":                         {"0.0.0.0", 8080},
		"00000000000000000000000000000000:1538": {"::", 5432},
		"00000000000000000000000001000000:0050": {"::1", 80},
	}
	for in, want := range cases {
		addr, port := parseHexAddr(in)
		if addr != want.addr || port != want.port {
			t.Errorf("parseHexAddr(%q) = %s:%d, want %s:%d", in, addr, port, want.addr, want.port)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Fix auth bug!": "fix-auth-bug", "": "task", "  --x--  ": "x"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// A holder has a systemd unit of its own only when systemd-run started it
// for its session. Any other unit it runs in, such as a CI runner's service,
// holds processes that are not the session's: the holder must not take it
// for its own, or the session's ports would list theirs too.
func TestHolderTakesOnlyItsOwnUnit(t *testing.T) {
	secretHome(t)
	for _, own := range []bool{false, true} {
		h := secretHolder(t, "/bin/sh", "-c", "exec sleep 30")
		cgroup := "0::/system.slice/ci-runner.service\n"
		want := ""
		if own {
			want = UnitName(h.id)
			cgroup = "0::/user.slice/" + brand.Slice + "/" + want + "\n"
		}
		fakeProc(t, map[string]string{"self/cgroup": cgroup})
		h.takeUnit()
		if err := h.start(); err != nil {
			t.Fatal(err)
		}
		pid := h.cmd.Process.Pid
		t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL); h.ptmx.Close() })
		if h.m.Run.Unit != want {
			t.Errorf("in the cgroup %q the holder's unit is %q, want %q", strings.TrimSpace(cgroup), h.m.Run.Unit, want)
		}
	}
}

// The OOM counter comes from the session's own unit only: a holder left in
// another program's cgroup must not count that program's kills.
func TestOOMKillsCountOnlyTheOwnUnit(t *testing.T) {
	const id = "oomtest1"
	root := t.TempDir()
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = "/sys/fs/cgroup" })
	for _, c := range []struct {
		path string
		want int64
	}{
		{"/system.slice/ci-runner.service", 0},
		{"/user.slice/" + brand.Slice + "/" + UnitName(id), 3},
	} {
		dir := filepath.Join(root, c.path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "memory.events"), []byte("low 0\nhigh 0\nmax 1\noom 3\noom_kill 3\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fakeProc(t, map[string]string{"self/cgroup": "0::" + c.path + "\n"})
		if got := readOOMKills(id); got != c.want {
			t.Errorf("in %s: %d OOM kills, want %d", c.path, got, c.want)
		}
	}
}
