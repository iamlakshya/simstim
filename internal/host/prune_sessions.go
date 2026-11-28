package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

type unpulledArgs struct {
	Dir    string   `json:"dir"`
	Base   string   `json:"base,omitempty"`
	Pulled []string `json:"pulled,omitempty"`
}

// unpulledCount runs inside git-op (with gitSafe: hooks and fsmonitor off,
// version gate, systemd scope and timeout) to count commits in a.Dir ahead of
// a.Base and any already-pulled commit OIDs in a.Pulled.
func unpulledCount(a *unpulledArgs) (int, error) {
	if a == nil || a.Dir == "" {
		return 0, nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return 0, fmt.Errorf("git not installed: %w", err)
	}
	if err := gitVersionGate(); err != nil {
		return 0, err
	}
	if _, err := git(a.Dir, "rev-parse", "--verify", "HEAD"); err != nil {
		return 0, fmt.Errorf("no HEAD commit: %w", err)
	}
	args := []string{"rev-list", "--count", "HEAD"}
	if a.Base != "" && isValidGitOID(a.Base) {
		if _, err := git(a.Dir, "cat-file", "-e", a.Base+"^{commit}"); err == nil {
			args = append(args, "^"+a.Base)
		}
	}
	for _, oid := range a.Pulled {
		if isValidGitOID(oid) {
			if _, err := git(a.Dir, "cat-file", "-e", oid+"^{commit}"); err == nil {
				args = append(args, "^"+oid)
			}
		}
	}
	out, err := git(a.Dir, args...)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse rev-list count: %w", err)
	}
	if n < 0 {
		return 0, nil
	}
	return n, nil
}

// UnpulledCommits counts commits in session m's seeded repositories, managed
// worktree, or session work directory that have not yet been pulled to the
// laptop (spec 17.6). Every git call runs through git-op (spec 19.3).
func UnpulledCommits(m *Manifest) int {
	if m == nil {
		return 0
	}
	total := 0
	checked := map[string]bool{}
	countDir := func(dir, base string) {
		if dir == "" {
			return
		}
		clean := filepath.Clean(dir)
		if checked[clean] {
			return
		}
		checked[clean] = true
		if _, err := os.Stat(filepath.Join(clean, ".git")); err != nil {
			return
		}
		if base == "" && len(m.SeedHeads) > 0 {
			if sha := m.SeedHeads[hostToGuestPath(m, clean)]; sha != "" {
				base = sha
			} else if sha := m.SeedHeads[clean]; sha != "" {
				base = sha
			}
		}
		ans, err := gitOp(gitOpUnpulledCount, gitOpRequest{
			Unpulled: &unpulledArgs{Dir: clean, Base: base, Pulled: m.Pulled},
		})
		if err == nil && ans.Count > 0 {
			total += ans.Count
		}
	}

	for _, r := range m.SeedRepos {
		countDir(r.Dir, r.SeedHead)
	}
	if m.Git != nil && m.Git.Worktree != "" {
		countDir(m.Git.Worktree, m.Git.BaseSHA)
	}
	if m.Wall != nil && m.Wall.WorkspaceHost != "" && isInsideWorkDir(m.ID, m.Wall.WorkspaceHost) {
		countDir(m.Wall.WorkspaceHost, "")
	}
	return total
}

func isInsideWorkDir(id, p string) bool {
	work := filepath.Clean(paths.WorkDir(id))
	clean := filepath.Clean(p)
	return clean == work || strings.HasPrefix(clean, work+string(filepath.Separator))
}

// FinishSession finishes a throwaway (`new --rm`) session once its program has
// ended: removes it when no commits wait to be pulled, or stops it and reports
// the count when unpulled commits exist (isolation spec 17.6).
func FinishSession(id string) (api.SessionFinishResult, error) {
	if err := CheckCaller(); err != nil {
		return api.SessionFinishResult{}, err
	}
	if !validID(id) {
		return api.SessionFinishResult{}, UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	m, err := LoadManifest(id)
	if err != nil {
		return api.SessionFinishResult{}, UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	name := m.Name
	if name == "" {
		name = m.ID
	}
	if n := UnpulledCommits(m); n > 0 {
		if _, err := Stop(api.StopArgs{ID: id}); err != nil {
			return api.SessionFinishResult{}, err
		}
		noun := "commits"
		if n == 1 {
			noun = "commit"
		}
		msg := fmt.Sprintf("%s has %d %s that you have not pulled, so it stopped instead of being removed", name, n, noun)
		return api.SessionFinishResult{
			ID:              id,
			Name:            name,
			Action:          "stopped",
			UnpulledCommits: n,
			Message:         msg,
		}, nil
	}
	if _, err := Delete(id); err != nil {
		return api.SessionFinishResult{}, err
	}
	return api.SessionFinishResult{
		ID:     id,
		Name:   name,
		Action: "removed",
	}, nil
}

// PruneSessions lists or removes stopped sessions on this host that have no commits
// waiting to be pulled, optionally filtered by StoppedBefore (spec 17.6).
// When DryRun is false it also prunes unreferenced images and old agents disks.
func PruneSessions(a api.PruneArgs) (api.PruneResult, error) {
	if err := CheckCaller(); err != nil {
		return api.PruneResult{}, err
	}
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil && !os.IsNotExist(err) {
		return api.PruneResult{}, fmt.Errorf("read sessions: %w", err)
	}
	var candidates []*Manifest
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		m, err := LoadManifest(e.Name())
		if err != nil {
			continue
		}
		if m.SandboxOf != "" || alive(m.ID) || m.Status.State != StateStopped {
			continue
		}
		if a.StoppedBefore != nil && !m.Status.Since.Before(*a.StoppedBefore) {
			continue
		}
		candidates = append(candidates, m)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Status.Since.Before(candidates[j].Status.Since)
	})

	res := api.PruneResult{Sessions: []api.PruneSession{}}
	for _, m := range candidates {
		row := api.PruneSession{
			ID:        m.ID,
			Name:      m.Name,
			Agent:     m.Agent.Kind,
			State:     m.Status.State,
			StoppedAt: m.Status.Since,
		}
		if n := UnpulledCommits(m); n > 0 {
			noun := "commits"
			if n == 1 {
				noun = "commit"
			}
			row.UnpulledCommits = n
			row.Action = "kept"
			row.Reason = fmt.Sprintf("%d %s not pulled", n, noun)
			res.Sessions = append(res.Sessions, row)
			continue
		}
		if a.DryRun {
			row.Action = "would_remove"
			res.Sessions = append(res.Sessions, row)
			continue
		}
		if _, err := DeleteNamed(m.ID, m.Name); err != nil {
			row.Action = "error"
			row.Error = err.Error()
		} else {
			row.Action = "removed"
		}
		res.Sessions = append(res.Sessions, row)
	}
	if !a.DryRun {
		if removed, err := PruneImages(); err == nil && len(removed) > 0 {
			res.RemovedImages = removed
		}
	}
	return res, nil
}

// finishDetachedOnExit removes or stops an unwalled throwaway (`new --rm`)
// session when its program exits with no client attached (spec 17.6).
func (h *Holder) finishDetachedOnExit() {
	m, err := LoadManifest(h.id)
	if err != nil {
		return
	}
	if n := UnpulledCommits(m); n > 0 {
		noun := "commits"
		if n == 1 {
			noun = "commit"
		}
		reason := fmt.Sprintf("%s has %d %s that you have not pulled, so it stopped instead of being removed", m.Name, n, noun)
		h.mu.Lock()
		h.m.Status = Status{
			State:  StateStopped,
			Since:  m.Status.Since,
			Source: "user",
			Reason: reason,
		}
		h.m.Run.HolderPID, h.m.Run.AgentPID, h.m.Run.Unit = 0, 0, ""
		snap := *h.m
		h.mu.Unlock()
		_ = h.save(&snap, true)
		h.closeSockets()
		h.closeAlerts()
		os.Exit(0)
	}
	h.closeSockets()
	h.closeAlerts()
	if m.Git != nil {
		_, _ = removeWorktree(m.Git)
	}
	_ = removeSessionDir(paths.SessionDir(h.id))
	os.Exit(0)
}

// FinishDetachedWalledOnExit removes or stops a walled throwaway (`new --rm`)
// session from inside its supervisor when its program exits with no client
// attached (spec 17.6). It reports whether the session was removed.
func FinishDetachedWalledOnExit(id string) (removed bool, stopReason string) {
	m, err := LoadManifest(id)
	if err != nil {
		return false, ""
	}
	if n := UnpulledCommits(m); n > 0 {
		noun := "commits"
		if n == 1 {
			noun = "commit"
		}
		return false, fmt.Sprintf("%s has %d %s that you have not pulled, so it stopped instead of being removed", m.Name, n, noun)
	}
	if m.Git != nil {
		_, _ = removeWorktree(m.Git)
	}
	_ = removeSessionDir(paths.SessionDir(id))
	_ = os.RemoveAll(paths.GuestRuntimeDir(id))
	_ = os.RemoveAll(paths.PortDir(id))
	return true, ""
}
