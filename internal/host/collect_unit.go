package host

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// What a bundle takes fresh from a session's systemd unit (decision 0025):
// the unit's state, and the kernel's report of an out-of-memory kill in it.

// unitProps are the properties of a session's unit that a bundle shows:
// its state, its result (oom-kill after a kill), and its memory. They leave
// out the unit's command line and environment.
var unitProps = []string{
	"ActiveState", "SubState", "Result", "ExecMainCode", "ExecMainStatus", "NRestarts",
	"MemoryCurrent", "MemoryPeak", "MemoryHigh", "MemoryMax", "MemorySwapMax", "OOMPolicy",
	"ControlGroup", "ActiveEnterTimestamp", "InactiveEnterTimestamp",
}

// unitState is the state of a session's unit, from `systemctl --user show`,
// and its cgroup's memory.events.
func unitState(unit string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := systemctl(ctx, "show", unit, "-p", strings.Join(unitProps, ","))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# systemctl --user show %s\n", unit)
	b.Write(out)
	for _, line := range strings.Split(string(out), "\n") {
		cg, ok := strings.CutPrefix(line, "ControlGroup=")
		if !ok || cg == "" {
			continue
		}
		ev, err := os.ReadFile(filepath.Join(cgroupRoot, cg, "memory.events"))
		if err == nil {
			b.WriteString("\n# memory.events\n")
			b.Write(ev)
		}
	}
	return b.Bytes(), nil
}

// journalGroups are the groups whose members can read the system journal,
// which holds the kernel's messages ([journalctl]).
//
// [journalctl]: https://www.freedesktop.org/software/systemd/man/latest/journalctl.html
var journalGroups = []string{"systemd-journal", "adm", "wheel"}

// JournalAccess reports whether this user can read the system journal:
// root, or a member of one of journalGroups. Tests in other packages replace
// it, as no test runner is in those groups.
var JournalAccess = func() bool {
	if os.Getuid() == 0 {
		return true
	}
	u, err := user.Current()
	if err != nil {
		return false
	}
	gids, err := u.GroupIds()
	if err != nil {
		return false
	}
	for _, gid := range gids {
		if g, err := user.LookupGroupId(gid); err == nil && slices.Contains(journalGroups, g.Name) {
			return true
		}
	}
	return false
}

// journalMax is the most of the journal that a bundle reads.
const journalMax = 64 << 20

// journal returns the kernel's messages since a time. Tests replace it.
var journal = func(ctx context.Context, since time.Time) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "journalctl", "-k", "-q", "--no-pager", "-o", "short-iso",
		"--since", since.Local().Format("2006-01-02 15:04:05"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("run journalctl: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("run journalctl: %w", err)
	}
	var b bytes.Buffer
	_, rerr := b.ReadFrom(io.LimitReader(out, journalMax))
	if b.Len() >= journalMax {
		_ = cmd.Process.Kill() // enough: the rest would wait on a full pipe
	}
	werr := cmd.Wait()
	if rerr != nil {
		return nil, fmt.Errorf("read journalctl: %w", rerr)
	}
	if werr != nil && b.Len() == 0 {
		return nil, fmt.Errorf("journalctl: %w", werr)
	}
	return b.Bytes(), nil
}

// kernelReport keeps the lines of the kernel's messages since a session's
// start that name its unit, with the line that names the killed process
// after each oom-kill line. The kernel's report spans more lines, but only
// these name the session.
func kernelReport(unit string, since time.Time) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	all, err := journal(ctx, since)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# The kernel's messages since %s that name %s\n", since.UTC().Format(time.RFC3339), unit)
	sc := bufio.NewScanner(bytes.NewReader(all))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	after := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, unit):
			b.WriteString(line + "\n")
			after = strings.Contains(line, "oom-kill:")
		case after && strings.Contains(line, "Killed process"):
			b.WriteString(line + "\n")
			after = false
		default:
			after = false
		}
	}
	return b.Bytes(), nil
}
