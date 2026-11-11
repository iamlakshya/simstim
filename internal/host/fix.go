package host

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// The host doctor's fixes (roadmap R7). Each one is found first and shown to
// the user with what it does and the command it runs; it runs only when the
// user agrees. Nothing here uses sudo: a fix that needs root is printed for
// the user to run.

// keepHelpers is how many of the newest helper versions Prune and the
// old-helpers fix always keep, besides the ones in use.
const keepHelpers = 3

// selfExe is this helper's own path; tests replace it.
var selfExe = os.Executable

// fixItem is one fix and the function that applies it.
type fixItem struct {
	api.Fix
	run func() error
}

// Fixes lists the problems the doctor can fix on this host. With a.Apply it
// runs the fixes named in a.IDs, and reports how each went.
func Fixes(a api.FixArgs) []api.Fix {
	items := findFixes(a)
	out := make([]api.Fix, 0, len(items))
	want := map[string]bool{}
	for _, id := range a.IDs {
		want[id] = true
	}
	for _, it := range items {
		if a.Apply && want[it.ID] && !it.Manual {
			if err := it.run(); err != nil {
				it.Error = err.Error()
			} else {
				it.Done = true
			}
		}
		out = append(out, it.Fix)
	}
	return out
}

func findFixes(a api.FixArgs) []fixItem {
	var out []fixItem
	if SystemdUser() {
		out = append(out, lingerFix()...)
		out = append(out, restoreUnitFix()...)
	}
	out = append(out, terminfoFix(a.Terminfo["xterm-ghostty"])...)
	out = append(out, currentLinkFix()...)
	out = append(out, staleSocketsFix()...)
	out = append(out, oldHelpersFix()...)
	return out
}

func lingerFix() []fixItem {
	if lingerOn() {
		return nil
	}
	u, err := user.Current()
	if err != nil {
		return nil
	}
	cmd := "loginctl enable-linger " + u.Username
	return []fixItem{{Fix: api.Fix{ID: "linger", Problem: "linger is off, so sessions stop when your last login on the host ends",
		Action: "turn on linger for " + u.Username + " with loginctl, without sudo; if the host needs root for it, the fix says so", Command: cmd},
		run: func() error {
			out, err := exec.CommandContext(context.Background(), "loginctl", "enable-linger", u.Username).CombinedOutput()
			if err == nil && lingerOn() {
				return nil
			}
			return fmt.Errorf("loginctl refused (%s). It needs root on this host: run sudo %s", strings.TrimSpace(string(out)), cmd)
		}}}
}

func restoreUnitFix() []fixItem {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".config", "systemd", "user", restoreUnit())
	problem := ""
	if b, err := os.ReadFile(path); err != nil {
		problem = "the unit that restores sessions after a restart is missing"
	} else if string(b) != restoreUnitText() {
		problem = "the unit that restores sessions after a restart is out of date"
	} else if out, _ := exec.CommandContext(context.Background(), "systemctl", "--user", "is-enabled", restoreUnit()).Output(); strings.TrimSpace(string(out)) != "enabled" {
		problem = "the unit that restores sessions after a restart is not enabled"
	}
	if problem == "" {
		return nil
	}
	return []fixItem{{Fix: api.Fix{ID: "restore-unit", Problem: problem,
		Action: "write " + homeRel(path, home) + " and enable it", Command: "systemctl --user enable " + restoreUnit()},
		run: func() error {
			if c := InstallRestoreUnit(); !c.OK {
				return fmt.Errorf("%s", c.Detail)
			}
			return nil
		}}}
}

func terminfoFix(src string) []fixItem {
	const name = "xterm-ghostty"
	if HasTerminfo(name) {
		return nil
	}
	f := api.Fix{ID: "terminfo", Problem: "Ghostty's terminfo (" + name + ") is missing, so sessions use xterm-256color and lose some of Ghostty's features"}
	if src == "" {
		f.Manual = true
		f.Action = "this computer has no " + name + " entry to send; run the fix from Ghostty, or install it from a computer that has it"
		f.Command = "infocmp -x " + name + " | ssh <host> tic -x -"
		return []fixItem{{Fix: f}}
	}
	f.Action = "compile this computer's " + name + " entry into ~/.terminfo on the host"
	f.Command = "tic -x -o ~/.terminfo <the entry>"
	return []fixItem{{Fix: f, run: func() error { return installTerminfo(src) }}}
}

// helperDirs lists the installed helper versions, newest first.
func helperDirs() []os.DirEntry {
	entries, _ := os.ReadDir(paths.BinDir())
	var out []os.DirEntry
	for _, e := range entries {
		if e.IsDir() && e.Name() != "current" && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return modTime(out[i]).After(modTime(out[j])) })
	return out
}

func modTime(e os.DirEntry) time.Time {
	if fi, err := e.Info(); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// ownVersion is the folder of the running helper, if it is an installed version.
func ownVersion() string {
	exe, err := selfExe()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	if filepath.Dir(dir) != paths.BinDir() {
		return ""
	}
	return filepath.Base(dir)
}

func currentLinkFix() []fixItem {
	link := filepath.Join(paths.BinDir(), "current")
	target, err := os.Readlink(link)
	if err == nil {
		if _, err := os.Stat(filepath.Join(paths.BinDir(), target, brand.HostBinary)); err == nil {
			return nil
		}
	}
	ver := ownVersion()
	if ver == "" {
		return nil
	}
	problem := "the current helper link is missing, so hooks and the restore unit cannot find the helper"
	if target != "" {
		problem = "the current helper link points to " + target + ", which is gone, so hooks and the restore unit cannot find the helper"
	}
	return []fixItem{{Fix: api.Fix{ID: "current-link", Problem: problem,
		Action: "point it at this helper, " + ver, Command: "ln -sfn " + ver + " " + homeRel(link, "")},
		run: func() error {
			tmp := link + ".new"
			_ = os.Remove(tmp)
			if err := os.Symlink(ver, tmp); err != nil {
				return fmt.Errorf("make the link: %w", err)
			}
			if err := os.Rename(tmp, link); err != nil {
				return fmt.Errorf("replace the link: %w", err)
			}
			return nil
		}}}
}

// staleSocketsFix finds the session sockets (s/) and hook sockets (h/) that
// nothing answers on.
func staleSocketsFix() []fixItem {
	dir := paths.RuntimeDir()
	var stale []string
	for _, sub := range []string{"s", "h"} {
		entries, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range entries {
			p := filepath.Join(dir, sub, e.Name())
			if e.Type()&os.ModeSocket == 0 || !strings.HasSuffix(e.Name(), ".sock") {
				continue
			}
			d := net.Dialer{Timeout: 300 * time.Millisecond}
			if c, err := d.DialContext(context.Background(), "unix", p); err == nil {
				c.Close()
				continue
			}
			stale = append(stale, p)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return []fixItem{{Fix: api.Fix{ID: "sockets", Problem: plural(len(stale), "stale session socket", "stale session sockets") + " with no session behind them, in " + homeRel(dir, ""),
		Action: "remove them; no running session uses them", Command: "rm " + strings.Join(stale, " ")},
		run: func() error { return removeAll(stale, os.Remove) }}}
}

func oldHelpersFix() []fixItem {
	old := oldHelpers()
	if len(old) == 0 {
		return nil
	}
	var size int64
	for _, p := range old {
		size += dirSize(p)
	}
	return []fixItem{{Fix: api.Fix{ID: "old-helpers",
		Problem: fmt.Sprintf("%s that nothing uses take %.1f MB", plural(len(old), "old helper version", "old helper versions"), float64(size)/1e6),
		Action:  fmt.Sprintf("remove them, and keep the newest %d and every version in use", keepHelpers),
		Command: "rm -r " + strings.Join(old, " ")},
		run: func() error { return removeAll(old, os.RemoveAll) }}}
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

func removeAll(list []string, rm func(string) error) error {
	var failed []string
	for _, p := range list {
		if err := rm(p); err != nil && !os.IsNotExist(err) {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return nil
}

// homeRel writes a path under home as ~/…, for the user.
func homeRel(p, home string) string {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if rest, ok := strings.CutPrefix(p, home+"/"); ok && home != "" {
		return "~/" + rest
	}
	return p
}
