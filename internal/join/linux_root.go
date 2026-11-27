//go:build linux

package join

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
)

func localCapabilityProbe(ctx context.Context) (api.CapProbe, error) {
	return host.CapabilityProbe(ctx, "")
}
func (e *linuxExecutor) accountProbe(ctx context.Context, u *user.User) (api.CapProbe, error) {
	uid, parseErr := strconv.Atoi(u.Uid)
	if parseErr != nil || uid <= 0 {
		return api.CapProbe{}, codeFailure("join_probe", "the account probe has no positive native uid")
	}
	runtimeDir := filepath.Join("/run/user", strconv.Itoa(uid))
	b, err := command(ctx, nil, "/usr/sbin/runuser", "-u", e.account, "--", "/usr/bin/env", "-i", "PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME="+u.HomeDir, "USER="+e.account, "LOGNAME="+e.account, "XDG_RUNTIME_DIR="+runtimeDir, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(runtimeDir, "bus"), e.helper, "host", "_join-probe")
	var p api.CapProbe
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("read the account's sandbox probe: %w", err)
	}
	if p.User != e.account || p.UID != uid || p.UID == 0 {
		return p, codeFailure("join_probe", "the sandbox probe did not run as the joined account")
	}
	return p, nil
}
func (e *linuxExecutor) prepareSandbox(ctx context.Context, u *user.User) error {
	p, err := e.accountProbe(ctx, u)
	if err != nil {
		return err
	}
	if !p.Linger {
		return codeFailure("join_root", "the account probe did not confirm linger")
	}
	ids := client.RootStepsFor(p, api.RuntimeStatus{}, false, 0)
	var names []string
	for _, id := range ids {
		if id != client.RootLinger {
			names = append(names, string(id))
		}
	}
	if len(names) == 0 {
		return nil
	}
	if err = e.applyRoot(ctx, "sandbox", u, names); err != nil {
		return err
	}
	after, err := e.accountProbe(ctx, u)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	var selected []client.RootStepID
	for _, name := range names {
		selected = append(selected, client.RootStepID(name))
	}
	script, err := client.BuildRootScript(e.account, uid, selected, 0)
	if err != nil {
		return err
	}
	return client.VerifyRootSteps(p, after, script)
}
func rootAccountDir(account string) string {
	return filepath.Join("/var/lib", brand.Name, "root-steps", account)
}

var journalRE = regexp.MustCompile(`^[0-9]{8}$`)

func journalNames(account string) ([]string, error) {
	dir := filepath.Join(rootAccountDir(account), "journal")
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := protectedPath(dir, true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || !journalRE.MatchString(entry.Name()) {
			return nil, codeFailure("join_ledger", "the root-step journal has an unexpected entry")
		}
		if err = protectedPath(filepath.Join(dir, entry.Name()), true); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, nil
}
func (e *linuxExecutor) applyRoot(ctx context.Context, id string, u *user.User, names []string) error {
	entry := e.ledger.entry(id)
	if entry == nil {
		uid, err := strconv.Atoi(u.Uid)
		if err != nil {
			return err
		}
		var ids []client.RootStepID
		for _, name := range names {
			ids = append(ids, client.RootStepID(name))
		}
		script, err := client.BuildRootScript(e.account, uid, ids, 0)
		if err != nil {
			return err
		}
		old, err := journalNames(e.account)
		if err != nil {
			return err
		}
		// The existing producer numbers by count; refuse gaps rather than reusing
		// an old native journal name. Nothing has run under this action yet.
		for n, name := range old {
			if name != fmt.Sprintf("%08d", n+1) {
				return codeFailure("join_ledger", "the existing root journal has a gap; ask its owner to finish its undo first")
			}
		}
		var journals []journalIdentity
		next := len(old)
		for _, name := range names {
			exists := false
			for _, oldName := range old {
				b, err := os.ReadFile(filepath.Join(rootAccountDir(e.account), "journal", oldName, "id"))
				if err != nil {
					return err
				}
				if strings.TrimSpace(string(b)) == name {
					exists = true
				}
			}
			if !exists {
				next++
				journals = append(journals, journalIdentity{Name: fmt.Sprintf("%08d", next), ID: name})
			}
		}
		undo, err := selectRootUndo(script.Undo, journals)
		if err != nil {
			return err
		}
		entry, err = e.ahead(ledgerEntry{ID: id, Kind: "root", UID: uid, Script: script.Text, Undo: undo, Journals: journals})
		if err != nil {
			return err
		}
	}
	if entry.State == "applied" {
		return e.verifyJournals(entry)
	}
	if entry.State == "undone" {
		return codeFailure("join_ledger", "a root action was undone; finish host leave before a new invite")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := command(ctx, strings.NewReader(entry.Script), "/bin/sh")
	if len(b) > 0 {
		_, _ = e.out.Write(b)
	}
	captureErr := e.captureJournals(entry)
	if err != nil || captureErr != nil {
		return errors.Join(err, captureErr)
	}
	if !strings.Contains(string(b), "\n__ROOT_STEP_DONE__\n") && !strings.HasPrefix(string(b), "__ROOT_STEP_DONE__\n") {
		return codeFailure("join_root", "the root steps did not report completion")
	}
	return e.applied(entry)
}
func (e *linuxExecutor) captureJournals(entry *ledgerEntry) error {
	for n := range entry.Journals {
		j := &entry.Journals[n]
		path := filepath.Join(rootAccountDir(e.account), "journal", j.Name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := protectedPath(path, true); err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(path, "id"))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) != j.ID {
			return codeFailure("join_changed", "the root journal entry belongs to another step")
		}
		dev, ino, err := identity(path)
		if err != nil {
			return err
		}
		if j.Inode != 0 && (j.Device != dev || j.Inode != ino) {
			return codeFailure("join_changed", "the original root journal directory changed")
		}
		j.Device, j.Inode = dev, ino
	}
	return e.save()
}
func (e *linuxExecutor) verifyJournals(entry *ledgerEntry) error {
	for _, j := range entry.Journals {
		path := filepath.Join(rootAccountDir(e.account), "journal", j.Name)
		if err := protectedPath(path, true); err != nil {
			return err
		}
		dev, ino, err := identity(path)
		if err != nil {
			return err
		}
		if j.Inode == 0 || j.Device != dev || j.Inode != ino {
			return codeFailure("join_changed", "the original root journal directory changed")
		}
	}
	return nil
}

// selectRootUndo uses the actual fixed root producer's undo, with a closed
// numeric selection. It cannot accept a command or path from a code or ledger.
func selectRootUndo(original string, journals []journalIdentity) (string, error) {
	const loop = "    [ -d \"$j\" ] || continue\n    safe \"$j\"\n"
	if strings.Count(original, loop) != 1 {
		return "", codeFailure("join_root", "the root undo template changed")
	}
	var selectors []string
	for _, j := range journals {
		if !journalRE.MatchString(j.Name) {
			return "", codeFailure("join_ledger", "the root journal selector is invalid")
		}
		selectors = append(selectors, j.Name)
	}
	match := "__none__"
	if len(selectors) > 0 {
		match = strings.Join(selectors, "|")
	}
	filter := "    case \"${j##*/}\" in " + match + ") ;; *) continue;; esac\n"
	text := strings.Replace(original, loop, filter+loop, 1)
	const cleanup = "if [ \"$left\" = 0 ]; then\n    rm -f \"$d/applied.json\""
	if strings.Count(text, cleanup) != 1 {
		return "", codeFailure("join_root", "the root undo cleanup template changed")
	}
	text = strings.Replace(text, cleanup, "if [ \"$left\" = 0 ] && [ -z \"$(find \"$d/journal\" -mindepth 1 -maxdepth 1 -print -quit)\" ]; then\n    rm -f \"$d/applied.json\"", 1)
	return text, nil
}

func (e *linuxExecutor) ensureLinger(ctx context.Context, u *user.User) error {
	if e.ledger.entry("linger") == nil {
		file := filepath.Join("/var/lib/systemd/linger", e.account)
		if st, err := os.Lstat(file); err == nil {
			if !st.Mode().IsRegular() {
				return codeFailure("join_root", "the existing linger entry is not a regular file")
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if b, err := command(ctx, nil, "/usr/bin/loginctl", "show-user", e.account, "-p", "Linger", "--value"); err == nil && strings.TrimSpace(string(b)) == "yes" {
			return nil
		}
	}
	return e.applyRoot(ctx, "linger", u, []string{"linger"})
}
