package host

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// The memory cap of sessions (decision 0024). Each session may use the host's
// memory minus a reserve for the system. All sessions together may use the
// same, through their slice.

const (
	mib = 1 << 20
	gib = 1 << 30
	// reserveMargin is kept on top of the tiers, as GKE keeps one for its
	// eviction threshold.
	reserveMargin = 100 * mib
)

// reserveTiers are the shares of a node's memory that GKE keeps for the
// system, each for up to a size of memory. The last, of size 0, takes the
// rest.
// https://docs.cloud.google.com/kubernetes-engine/docs/concepts/plan-node-sizes
var reserveTiers = []struct{ size, percent int64 }{
	{4 * gib, 25}, {4 * gib, 20}, {8 * gib, 10}, {112 * gib, 6}, {0, 2},
}

// MemTotal returns the host's memory in bytes, from /proc/meminfo, or 0 when
// it cannot be read.
func MemTotal() int64 {
	f, err := os.Open(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB"))
			n, _ := strconv.ParseInt(v, 10, 64)
			return n * 1024
		}
	}
	return 0
}

// MemoryReserve is the memory that a host with total bytes keeps for the
// system. It is GKE's tiers, or 255 MiB below 1 GiB, and a margin of 100 MiB.
func MemoryReserve(total int64) int64 {
	if total < gib {
		return 255*mib + reserveMargin
	}
	r, left := int64(0), total
	for _, t := range reserveTiers {
		part := left
		if t.size > 0 {
			part = min(part, t.size)
		}
		r += part * t.percent / 100
		left -= part
	}
	return r + reserveMargin
}

// memoryMaxFor is DefaultMemoryMax on a host with total bytes of memory. It
// is total minus the reserve, but never less than half of total. It is
// rounded down to a whole MiB, and it is 0, no cap, when total is not known.
func memoryMaxFor(total int64) int64 {
	if total <= 0 {
		return 0
	}
	return max(total-MemoryReserve(total), total/2) / mib * mib
}

// DefaultMemoryMax is the default memory cap of a session, and the limit of
// all sessions together: the host's memory minus MemoryReserve. It is read
// again at each start, so a VM that was made larger gets a larger cap.
func DefaultMemoryMax() int64 { return memoryMaxFor(MemTotal()) }

// sessionMemoryMax is the cap of a session that asked for asked bytes. It is
// the host's default when the session asked for 0. It is never more than
// the limit of all sessions together, which the slice enforces anyway.
func sessionMemoryMax(asked int64) int64 {
	limit := DefaultMemoryMax()
	if asked <= 0 || (limit > 0 && asked > limit) {
		return limit
	}
	return asked
}

// systemctl runs `systemctl --user` with args. Tests replace it.
var systemctl = func(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// userManagerCgroup is the user's systemd manager in the cgroup tree.
func userManagerCgroup() string {
	uid := os.Getuid()
	return filepath.Join(cgroupRoot, "user.slice", fmt.Sprintf("user-%d.slice", uid), fmt.Sprintf("user@%d.service", uid))
}

// memoryDelegated reports whether the user's manager has the memory
// controller of cgroup v2. Without it, as on cgroup v1, no memory cap holds.
func memoryDelegated() bool {
	b, err := os.ReadFile(filepath.Join(userManagerCgroup(), "cgroup.controllers"))
	return err == nil && strings.Contains(string(b), "memory")
}

// sliceCgroup is the sessions' slice in the cgroup tree. It exists while the
// slice runs.
func sliceCgroup() string { return filepath.Join(userManagerCgroup(), brand.Slice) }

// sliceBytes reads a number of bytes from one of the running slice's files,
// such as memory.max. ok is false when the slice does not run, or when the
// file holds no number, as memory.max holds "max" for no limit.
func sliceBytes(file string) (n int64, ok bool) {
	b, err := os.ReadFile(filepath.Join(sliceCgroup(), file))
	if err != nil {
		return 0, false
	}
	n, err = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

// sliceLimitArgs are the arguments of `systemctl --user` that put limit on
// the sessions' slice. set-property applies it at once to a running slice.
// It also stores it in a drop-in under ~/.config/systemd/user.control/, so
// it holds after a reboot and for a slice that does not run yet. A slice
// unit file would need a daemon-reload of the whole user manager. systemd
// would undo a write to the cgroup's memory.max.
func sliceLimitArgs(limit int64) []string {
	return []string{"set-property", brand.Slice, "MemoryMax=" + strconv.FormatInt(limit, 10)}
}

// limitSessions puts limit on the sessions' slice, so that all sessions
// together stay below it. When the running slice already has it, it reads
// one file and stops, so it is cheap to call at each start. It does nothing
// where no memory cap holds. It never sets a limit below what the running
// sessions use together, since the kernel would then stop one of them. It
// says so instead, and a later start sets the limit once they use less.
func limitSessions(limit int64) error {
	if limit <= 0 {
		return nil
	}
	if have, ok := sliceBytes("memory.max"); ok && have == limit {
		return nil
	}
	if !memoryDelegated() {
		return nil
	}
	if used, ok := sliceBytes("memory.current"); ok && used > limit {
		return fmt.Errorf("the sessions use %s together, more than the new limit of %s, so a later start sets it", mibText(used), mibText(limit))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := systemctl(ctx, sliceLimitArgs(limit)...); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sliceLimit is the MemoryMax that systemd has for the sessions' slice, from
// its unit and drop-ins, or 0 for none.
func sliceLimit() int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := systemctl(ctx, "show", brand.Slice, "-p", "MemoryMax", "--value")
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}

// warnSession writes a warning to the log of session id, where a failed
// start and the host doctor look.
func warnSession(id, msg string, err error) {
	f, ferr := os.OpenFile(paths.HolderLog(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	newHolderLog(f).Warn(msg, "err", err)
}

// sessionsMemoryCheck is the host doctor's line about the memory of all
// sessions together: what they may use, and the reserve. The caller checks
// that the memory controller is delegated first.
func sessionsMemoryCheck() Check {
	total, want := MemTotal(), DefaultMemoryMax()
	c := Check{Name: "memory for sessions", OK: true,
		Detail: fmt.Sprintf("sessions may use %s together, and %s is kept for the system", gibText(want), gibText(total-want))}
	if want <= 0 {
		c.OK, c.Detail = false, "the host's memory is not known, so sessions have no memory cap"
		return c
	}
	if !exists(sliceCgroup()) {
		// The next start sets the limit, also after the host was resized.
		if sliceLimit() != want {
			c.Detail += ", from the next start"
		}
		return c
	}
	have, _ := sliceBytes("memory.max")
	used, _ := sliceBytes("memory.current")
	switch {
	case have == want:
	case used > want:
		c.OK = false
		c.Detail = fmt.Sprintf("the sessions use %s together, more than the %s they may use, so their limit waits", mibText(used), mibText(want))
		c.Fix = "a later start sets the limit once the sessions use less. To set it sooner, delete a session you do not need."
	default:
		c.OK = false
		c.Detail = fmt.Sprintf("sessions may use %s together, not %s", limitText(have), gibText(want))
		c.Fix = "systemctl --user set-property " + brand.Slice + " MemoryMax=" + strconv.FormatInt(want, 10)
	}
	return c
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// gibText is a size in GiB, to a tenth: "2.9 GiB".
func gibText(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/gib) }

// mibText is a size in whole MiB, for sizes that are close: "2972 MiB".
func mibText(n int64) string { return strconv.FormatInt(n/mib, 10) + " MiB" }

// limitText is a slice's limit: a size, or "all the host's memory" for none.
func limitText(n int64) string {
	if n <= 0 {
		return "all the host's memory"
	}
	return gibText(n)
}
