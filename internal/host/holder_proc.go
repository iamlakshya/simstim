package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// kill stops the agent's whole process group, then the holder.
// stop ends the agent's process group, then the holder. When system is true,
// the session is recorded as stopped with the host, not as ended, so restore
// brings it back after a restart.
func (h *Holder) stop(reason string, system bool) {
	h.closeAlerts()
	h.mu.Lock()
	exited := h.exited
	h.stopping = system
	h.mu.Unlock()
	if !exited && h.cmd != nil && h.cmd.Process != nil {
		pid := h.cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGHUP)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			h.mu.Lock()
			exited = h.exited
			h.mu.Unlock()
			if exited {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !exited {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			time.Sleep(100 * time.Millisecond)
		}
	}
	if h.vt != nil {
		_ = h.vt.Close()
	}
	h.log.Info("holder exiting", "reason", reason)
	h.endAllDebug()
	h.mu.Lock()
	switch {
	case system:
		h.m.Status.Reason, h.m.Status.Source = reason, "system"
	case h.m.Status.State != StateExited && h.m.Status.State != StateErrored:
		h.m.Status = Status{State: StateExited, Since: time.Now().UTC(), Reason: reason}
	}
	m := *h.m
	h.mu.Unlock()
	_ = h.save(&m, true)
	if h.ln != nil {
		h.ln.Close()
	}
	os.Remove(paths.Socket(h.id))
	os.Exit(0)
}

// save writes m, a copy of the manifest, unless the holder has made its
// last save. After that, Delete may remove the session's folder at any
// moment, and a late save would bring it back.
func (h *Holder) save(m *Manifest, last bool) error {
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = last
	return m.Save()
}

// ownUnit returns the systemd unit this holder runs in, when it is the unit
// that launchHolder started for session id with systemd-run, or "". Without
// a systemd user manager, a holder stays in the cgroup of whatever started
// it, such as a CI runner's service, and the processes there are not the
// session's.
func ownUnit(id string) string {
	b, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			if leaf := filepath.Base(rest); leaf == UnitName(id) {
				return leaf
			}
		}
	}
	return ""
}

// cgroupRoot is where the cgroup tree is mounted; tests point it elsewhere.
var cgroupRoot = "/sys/fs/cgroup"

// readOOMKills reads the oom_kill counter of the holder's own unit, the one
// launchHolder started for session id. Without one, it is 0: another
// program's cgroup counts kills that are not the session's.
func readOOMKills(id string) int64 {
	b, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "0::")
		if !ok || filepath.Base(rest) != UnitName(id) {
			continue
		}
		ev, err := os.ReadFile(filepath.Join(cgroupRoot, rest, "memory.events")) //nolint:gosec // G703: rest is the kernel's own cgroup path, from /proc/self/cgroup
		if err != nil {
			return 0
		}
		for _, l := range strings.Split(string(ev), "\n") {
			if v, ok := strings.CutPrefix(l, "oom_kill "); ok {
				n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				return n
			}
		}
	}
	return 0
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

// lookPath finds name in the given PATH value.
func lookPath(name, path string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is not installed on this host (not found on PATH)", name)
}

// snapshotLines is how much scrollback a new client gets in a snapshot.
const snapshotLines = 5000

// startVT starts the shadow terminal that sits next to this binary. Without it
// the holder still works: new clients get the raw ring instead of a snapshot.
func (h *Holder) startVT() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	path := filepath.Join(filepath.Dir(exe), brand.VTBinary)
	if _, err := os.Stat(path); err != nil {
		h.log.Info("no shadow terminal, new clients get the raw output", "path", path)
		return
	}
	h.ptyOut = make(chan []byte, 256)
	go func() {
		for b := range h.ptyOut {
			_, _ = h.ptmx.Write(b)
		}
	}()
	h.mu.Lock()
	cols, rows := h.m.Term.Cols, h.m.Term.Rows
	tp, tpv := envValue(h.m.Env, "TERM_PROGRAM"), envValue(h.m.Env, "TERM_PROGRAM_VERSION")
	h.mu.Unlock()
	vt, err := StartVT(VTOptions{
		Path: path, Cols: cols, Rows: rows,
		History: func() ([]byte, int64) {
			h.mu.Lock()
			defer h.mu.Unlock()
			d, _ := h.ring.ReadFrom(h.ring.Start())
			return d, h.ring.Start()
		},
		OnWrite: func(p []byte) {
			select {
			case h.ptyOut <- append([]byte(nil), p...):
			default: // the agent is not reading its input; drop the reply
			}
		},
		Log: h.log,
	})
	if err != nil {
		h.log.Warn("shadow terminal did not start", "err", err)
		return
	}
	if tp != "" {
		vt.SetProfile(VTProfile{XTVersion: strings.TrimSpace(tp + " " + tpv)})
	}
	vt.AnswerQueries(true) // no client yet
	h.mu.Lock()
	h.vt = vt
	h.mu.Unlock()
}
