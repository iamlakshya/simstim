package guest

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// On Linux the guest init of a sandbox is a child subreaper: a process of
// the sandbox whose parent dies becomes the guest init's child, wherever
// it was in the tree. So the guest init ends every process of the sandbox
// with signals to its own children only. A child's PID, and the group
// that it leads, stay its own until its parent reaps it, and only the
// guest init reaps them: os.Process reaps the holder, and the guest init
// reaps the rest, under reapMu.

// becomeSubreaper makes the guest init a child subreaper. It reports
// whether that worked.
func becomeSubreaper(log *slog.Logger) bool {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		log.Warn("the guest init is not a child subreaper", "err", err)
		return false
	}
	return true
}

// children lists the children of this process: from each thread's
// /proc/self/task/<tid>/children, or, on a kernel without those files
// (no CONFIG_PROC_CHILDREN, as Docker Desktop's), from each process in
// /proc whose parent, in its stat, is this one. Either way it lists only
// this process's own children, never a process that a file names: a
// child stays this process's, and its PID names no other process, until
// this process reaps it, and only the reapers here reap, under reapMu. So
// a signal to a listed child that is not reaped yet meets rule B
// (docs/explanation/architecture.md, 2.5).
func children() []int {
	files, _ := filepath.Glob("/proc/self/task/*/children")
	var out []int
	read := false
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		read = true
		for _, w := range bytes.Fields(b) {
			if pid, err := strconv.Atoi(string(w)); err == nil {
				out = append(out, pid)
			}
		}
	}
	if read {
		return out
	}
	return statChildren()
}

// statChildren lists the processes in /proc whose parent, in stat, is this
// process.
func statChildren() []int {
	var out []int
	self := []byte(strconv.Itoa(os.Getpid()))
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		i := bytes.LastIndexByte(b, ')')
		if err != nil || i < 0 {
			continue
		}
		if f := bytes.Fields(b[i+1:]); len(f) > 1 && bytes.Equal(f[1], self) {
			out = append(out, pid)
		}
	}
	return out
}

// reapMu lets one reaper run at a time.
var reapMu sync.Mutex

// reapLoop reaps, once a second, the children that have exited, except
// the holder, until the end begins.
func (g *Init) reapLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-g.done:
			return
		}
		g.mu.Lock()
		hp, ended := g.holder, g.ended
		g.mu.Unlock()
		if ended {
			return
		}
		owned := make(map[int]bool)
		g.mu.Lock()
		for run := range g.owned {
			run.mu.Lock()
			if run.cmd != nil {
				owned[run.cmd.Process.Pid] = true
			}
			run.mu.Unlock()
		}
		holder := 0
		if hp != nil {
			holder = hp.cmd.Process.Pid
		}
		for _, hp := range g.members {
			owned[hp.cmd.Process.Pid] = true
		}
		reapMu.Lock()
		for _, pid := range children() {
			if pid != holder && !owned[pid] {
				var ws syscall.WaitStatus
				_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
			}
		}
		reapMu.Unlock()
		g.mu.Unlock()
	}
}

// reapOrphans ends every child that the guest init still has, with the
// process group that each leads, and reaps them, until none is left or 5 s
// have passed. The holder is reaped already.
func (g *Init) reapOrphans() bool {
	reapMu.Lock()
	defer reapMu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		kids := children()
		for _, pid := range kids {
			if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		switch {
		case errors.Is(err, syscall.ECHILD):
			return true
		case pid > 0:
			continue
		case time.Now().After(deadline):
			g.log.Warn("children outlived the guest init's end", "pids", kids)
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
