package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
)

// sessionCap is the memory cap that the session id got.
func sessionCap(t *testing.T, id string) int64 {
	t.Helper()
	list, err := host.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return s.MemoryMax
		}
	}
	t.Fatalf("no session %s", id)
	return 0
}

// capFor is the cap that a session that asked for asked bytes gets on this
// machine, where the host's limit for all sessions together lowers it.
func capFor(asked int64) int64 {
	limit := host.DefaultMemoryMax()
	if asked == 0 || (limit > 0 && asked > limit) {
		return limit
	}
	return asked
}

func writeSettings(t *testing.T, text string) {
	t.Helper()
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "config.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// `new --memory-max` and [sessions] memory_max reach the host, and a bad
// size stops the start with a hint before anything starts.
func TestNewMemoryMax(t *testing.T) {
	newTestHost(t)
	failsWithHint(t, `"100M" is below the smallest cap, 256M`, "size of 256M or more", "new", "box", "--detach", "--memory-max", "100M", "--", "sleep", "60")
	failsWithHint(t, `"4096" has no unit`, api.MemorySizes, "new", "box", "--detach", "--memory-max", "4096", "--", "sleep", "60")
	failsWithHint(t, `"infinity" is not a size: auto already`, api.MemorySizes, "new", "box", "--detach", "--memory-max", "infinity", "--", "sleep", "60")
	if list, _ := host.List(); len(list) != 0 {
		t.Fatalf("a refused start left sessions: %+v", list)
	}

	id := newSessionArgs(t, "--memory-max", "512M")
	if got := sessionCap(t, id); got != capFor(512<<20) {
		t.Errorf("new --memory-max 512M: the cap is %d, want %d", got, capFor(512<<20))
	}
	writeSettings(t, "[sessions]\nmemory_max = \"300M\"\n")
	if got := sessionCap(t, newSessionArgs(t)); got != capFor(300<<20) {
		t.Errorf("memory_max = \"300M\": the cap is %d, want %d", got, capFor(300<<20))
	}
	// The flag wins over the key, auto included.
	if got := sessionCap(t, newSessionArgs(t, "--memory-max", "auto")); got != capFor(0) {
		t.Errorf("new --memory-max auto: the cap is %d, want the default %d", got, capFor(0))
	}
	writeSettings(t, "[sessions]\nmemory_max = \"lots\"\n")
	failsWithHint(t, `memory_max in config.toml: "lots" is not a size`, "[sessions] memory_max", "new", "box", "--detach", "--", "sleep", "60")
}

// newSessionArgs starts a detached session with flags, and returns its ID.
func newSessionArgs(t *testing.T, flags ...string) string {
	t.Helper()
	args := append(append([]string{"new", "box", "--detach"}, flags...), "--", "sleep", "60")
	m := startedRE.FindStringSubmatch(ok(t, args...))
	if m == nil {
		t.Fatalf("%q started nothing", args)
	}
	return m[1]
}

// `host doctor` prints the host's line about an agent that asks for more
// memory than the host has, with its hint, and a line that fits without one.
func TestHostDoctorPrintsAgentMemory(t *testing.T) {
	installed := map[string]string{"claude": "/home/dev/.local/bin/claude", "codex": "/home/dev/.local/bin/codex"}
	var b strings.Builder
	printChecks(&b, host.AgentMemoryChecks(installed, 2<<30, 1436<<20)) // a 2 GiB host
	hint := "    Try: give the host at least 4 GB (3.7 GiB) of memory. Swap does not count, because sessions do not use it. See docs/how-to/memory.md.\n"
	want := "  ✕ Claude Code memory     its docs ask for 4 GB of RAM: this host has only 2.0 GiB, and a session may use 1.4 GiB\n" + hint +
		"  ✕ Codex memory           its docs ask for 4 GB of RAM: this host has only 2.0 GiB, and a session may use 1.4 GiB\n" + hint
	if b.String() != want {
		t.Errorf("on a 2 GiB host:\n%s\nwant:\n%s", b.String(), want)
	}
	b.Reset()
	printChecks(&b, host.AgentMemoryChecks(installed, 3891<<20, 2818<<20)) // a VM sold as 4 GB
	want = "  ✓ Claude Code memory     its docs ask for 4 GB of RAM: this host has 3.8 GiB, and a session may use 2.8 GiB\n" +
		"  ✓ Codex memory           its docs ask for 4 GB of RAM: this host has 3.8 GiB, and a session may use 2.8 GiB\n"
	if b.String() != want {
		t.Errorf("on a VM sold as 4 GB:\n%s\nwant:\n%s", b.String(), want)
	}
}
