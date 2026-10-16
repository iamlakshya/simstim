package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// debug: bundle writes the bundle of the selected session or host while the
// launcher goes on. One runs at a time. The header says bundling… and the
// rule's scan runs until it ends. Quitting the launcher stops it, and it
// then leaves no file.

// bundleWait is how long a bundle from the launcher may take.
const bundleWait = 10 * time.Minute

// bundleRun is a bundle being written.
type bundleRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// debugBundle starts the bundle of the selected session or host, in the
// folder where the launcher started.
func (m *Model) debugBundle() tea.Cmd {
	if m.dbg.bundling != nil {
		return m.toast(toastInfo, "A bundle is being written. Wait for it to end.")
	}
	be := m.be.(debugBackend)
	h, s := m.debugTarget()
	if !h.online() {
		return m.toastErr(offlineErr(h))
	}
	what, id := h.name(), ""
	if s != nil {
		what, id = s.Name+" on "+h.name(), s.ID
	}
	ctx, cancel := context.WithTimeout(m.ctx, bundleWait)
	run := &bundleRun{cancel: cancel, done: make(chan struct{})}
	m.dbg.bundling = run
	host := h.host
	return tea.Batch(m.toast(toastInfo, "Writing the debug bundle of "+what+"…"), func() tea.Msg {
		defer close(run.done)
		defer cancel()
		res, err := be.Bundle(ctx, host, id)
		if err != nil {
			return debugDoneMsg{err: err, bundled: true}
		}
		return debugDoneMsg{ok: "Wrote the bundle. Read it before you share it.",
			path: res.Path + " · " + sizeText(res.Size), bundled: true}
	})
}

// stopBundle stops a bundle that is being written, and waits a moment for
// it to remove its file. Close calls it as the launcher quits.
func (m *Model) stopBundle() {
	run := m.dbg.bundling
	if run == nil {
		return
	}
	run.cancel()
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
	}
}

// localPath writes a path on this computer under the home folder from ~.
func localPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return p
}

// shortPath keeps a path to w cells, so that it fits on one line under a
// notice: its end, with the file's name, after a ….
func shortPath(p string, w int) string {
	r := []rune(p)
	if w < 2 || len(r) <= w {
		return p
	}
	return "…" + string(r[len(r)-(w-1):])
}

// sizeText is a file's size for people, such as 8.1 MiB.
func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
