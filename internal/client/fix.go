package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tomlv2 "github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// Fix is one problem a doctor found, and what fixing it does.
type Fix = api.Fix

// Check is one line of a doctor's report.
type Check = api.Check

// HostFixes asks the host's doctor for its fixes; with apply it runs the ones
// named in ids. It sends this computer's Ghostty terminfo, if it has one, for
// the terminfo fix.
func (c *Conn) HostFixes(ctx context.Context, apply bool, ids []string) ([]Fix, error) {
	if !c.HasFeature("doctor.fix") {
		return nil, &UserError{Msg: "the helper on " + c.Host.Name + " is too old to fix itself", Hint: brand.Name + " host add " + c.Host.Name + " (updates the helper)", Code: "helper_old"}
	}
	a := api.FixArgs{Apply: apply, IDs: ids}
	if src, err := exec.CommandContext(ctx, "infocmp", "-x", "xterm-ghostty").Output(); err == nil {
		a.Terminfo = map[string]string{"xterm-ghostty": string(src)}
	}
	var out []Fix
	err := c.Call(ctx, "doctor.fix", a, &out)
	return out, err
}

// LocalChecks checks this computer: ssh, the config files, and the commands
// of the secrets. It never runs a secret's command.
func LocalChecks() []Check {
	var out []Check
	if p, err := exec.LookPath("ssh"); err != nil {
		out = append(out, Check{Name: "ssh", Detail: "ssh is not on PATH", Fix: "install OpenSSH"})
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		v, _ := exec.CommandContext(ctx, p, "-V").CombinedOutput()
		cancel()
		out = append(out, Check{Name: "ssh", OK: true, Detail: strings.TrimSpace(string(v))})
	}
	out = append(out, fileCheck("config.toml", settingsPath(), &Settings{}))
	out = append(out, fileCheck("hosts.toml", hostsPath(), &hostsFile{}))
	s := LoadSettings()
	names := make([]string, 0, len(s.Secrets))
	for k := range s.Secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		prog := strings.Fields(s.Secrets[k])
		switch {
		case len(prog) == 0:
			out = append(out, Check{Name: "secret " + k, Detail: "it has no command", Fix: "give it a command in " + settingsPath()})
		case strings.ContainsAny(prog[0], "=$`'\"(") || isShellWord(prog[0]):
			out = append(out, Check{Name: "secret " + k, OK: true, Detail: "its command is a shell line: run it once yourself to check it"})
		default:
			if _, err := exec.LookPath(prog[0]); err != nil {
				out = append(out, Check{Name: "secret " + k, Detail: prog[0] + " is not on PATH", Fix: "install " + prog[0] + ", or change the command in " + settingsPath()})
			} else {
				out = append(out, Check{Name: "secret " + k, OK: true, Detail: "its command, " + prog[0] + ", is installed"})
			}
		}
	}
	return out
}

func isShellWord(w string) bool {
	switch w {
	case "if", "for", "while", "case", "{", "(", "exec", "env", "command":
		return true
	}
	return false
}

// fileCheck reads a TOML file into v with go-toml/v2 strict decoding and
// reports unknown keys or errors with a hint to run `config check`
// (docs/contributing/cli-spec.md 13.4).
func fileCheck(name, path string, v any) Check {
	b, exists, err := ReadConfigFile(path)
	if err != nil {
		return Check{Name: name, Detail: err.Error(), Fix: configCheckHint()}
	}
	if !exists {
		return Check{Name: name, OK: true, Detail: "none yet, at " + path}
	}
	strictErr, ins, err := DecodeConfigStrict(path, b, v)
	if err != nil {
		msg := err.Error()
		var de *tomlv2.DecodeError
		if errors.As(err, &de) {
			line, col := de.Position()
			msg = fmt.Sprintf("line %d, column %d: %s", line, col, strings.TrimPrefix(de.Error(), "toml: "))
		}
		return Check{Name: name, Detail: msg, Fix: configCheckHint()}
	}
	if len(ins.Aliases) > 0 {
		a := ins.Aliases[0]
		return Check{Name: name, Detail: fmt.Sprintf("line %d: %q and %q both name %q", a.Pos.Line, a.First, a.Second, a.Canonical), Fix: configCheckHint()}
	}
	if strictErr != nil && len(strictErr.Errors) > 0 {
		ks := make([]string, 0, len(strictErr.Errors))
		for _, e := range strictErr.Errors {
			ks = append(ks, strings.Join(e.Key(), "."))
		}
		return Check{Name: name, Detail: "unknown keys: " + strings.Join(ks, ", "), Fix: configCheckHint()}
	}
	return Check{Name: name, OK: true, Detail: path}
}

// localFix is a fix on this computer and the function that applies it.
type localFix struct {
	Fix
	// run applies the fix until ctx ends, and adds each change it makes to
	// the fix's Changed, as it makes it.
	run func(ctx context.Context, f *Fix) error
	// files is how many files the fix removes, and what they are, such as
	// "generated SSH configs": a fix that a cancel stopped part way is
	// named by how many of them it removed.
	files int
	what  string
}

// StoppedFixesError is the error of local fixes that a cancel stopped. Done says
// in words what was done before it: each fix that ran ("fixed: <problem>"),
// and a fix that the cancel stopped part way, by how far it got ("removed
// 1 of 2 generated SSH configs"). It is the cancel too, for errors.Is.
type StoppedFixesError struct {
	Done []string
	Err  error
}

func (e *StoppedFixesError) Error() string { return "apply the fixes: " + e.Err.Error() }

// Unwrap gives the cancel.
func (e *StoppedFixesError) Unwrap() error { return e.Err }

// LocalFixes lists the problems on this computer the doctor can fix: SSH
// configs generated for hosts that are gone, and ControlMaster sockets whose
// master has died. With apply it runs the ones named in ids.
func LocalFixes(apply bool, ids []string) []Fix {
	if !apply {
		ids = nil
	}
	out, _ := ApplyLocalFixes(context.Background(), ids)
	return out
}

// ApplyLocalFixes lists the local fixes, as LocalFixes does, and applies
// those of ids until ctx ends: it looks at ctx before each fix and each
// file, and changes nothing after its end. A fix that ctx stopped is not
// done, and keeps what it changed. The error is then a StoppedFixesError, which
// says what was done.
func ApplyLocalFixes(ctx context.Context, ids []string) ([]Fix, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	// Which sockets are dead is a fact of their masters: a cancel of ctx
	// must not make a live one look dead, so the check does not take ctx.
	all := append(sshConfigFix(), controlSocketFix()...) //nolint:contextcheck // see above
	out := make([]Fix, 0, len(all))
	var stopped error
	var done []string
	for _, f := range all {
		if want[f.ID] && stopped == nil {
			// The check before each fix: a cancel during the last one
			// keeps the next one from starting.
			stopped = ctx.Err()
		}
		if want[f.ID] && stopped == nil {
			if err := f.run(ctx, &f.Fix); err != nil {
				f.Error = err.Error()
			} else {
				f.Done = true
			}
			switch {
			case f.Done:
				done = append(done, "fixed: "+f.Problem)
			case len(f.Changed) > 0:
				done = append(done, fmt.Sprintf("removed %d of %d %s", len(f.Changed), f.files, f.what))
			}
		}
		out = append(out, f.Fix)
	}
	if stopped == nil {
		stopped = ctx.Err() // a cancel during the last fix
	}
	if stopped != nil {
		return out, &StoppedFixesError{Done: done, Err: stopped}
	}
	return out, nil
}

func sshConfigFix() []localFix {
	hosts, err := LoadHosts()
	if err != nil {
		return nil // a broken hosts.toml must not make every config look orphaned
	}
	known := map[string]bool{}
	for _, h := range hosts {
		known[h.Name] = true
	}
	files, _ := filepath.Glob(filepath.Join(ConfigDir(), "ssh", "config-*"))
	var orphans []string
	for _, f := range files {
		if !known[strings.TrimPrefix(filepath.Base(f), "config-")] {
			orphans = append(orphans, f)
		}
	}
	if len(orphans) == 0 {
		return nil
	}
	return []localFix{{Fix: Fix{ID: "ssh-configs",
		Problem: fmt.Sprintf("%d generated SSH config(s) for hosts you removed", len(orphans)),
		Action:  "remove them; " + brand.Name + " writes a host's config again when it connects",
		Command: "rm " + strings.Join(orphans, " ")},
		run:   func(ctx context.Context, f *Fix) error { return removeFiles(ctx, orphans, f) },
		files: len(orphans), what: "generated SSH configs"}}
}

func controlSocketFix() []localFix {
	entries, _ := os.ReadDir(filepath.Join(CacheDir(), "cm"))
	var stale []string
	for _, e := range entries {
		p := filepath.Join(CacheDir(), "cm", e.Name())
		if e.Type()&os.ModeSocket == 0 {
			continue
		}
		d := net.Dialer{Timeout: 300 * time.Millisecond}
		if c, err := d.DialContext(context.Background(), "unix", p); err == nil {
			c.Close()
			continue
		}
		stale = append(stale, p)
	}
	if len(stale) == 0 {
		return nil
	}
	return []localFix{{Fix: Fix{ID: "control-sockets",
		Problem: fmt.Sprintf("%d ControlMaster socket(s) whose SSH master has died, which can make connections hang or fail", len(stale)),
		Action:  "remove them; the next connection starts a new master",
		Command: "rm " + strings.Join(stale, " ")},
		run:   func(ctx context.Context, f *Fix) error { return removeFiles(ctx, stale, f) },
		files: len(stale), what: "ControlMaster sockets"}}
}

// RemovedFile is called after each file that a local fix removes. Tests
// cancel there, between two files or two fixes; nothing else sets it.
var RemovedFile = func(string) {}

// removeFiles removes the files of list, until ctx ends, and adds each
// file it removed to f's changes, at once.
func removeFiles(ctx context.Context, list []string, f *Fix) error {
	var failed []string
	for _, p := range list {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stopped before %s: %w", p, err)
		}
		switch err := os.Remove(p); {
		case err == nil:
			f.Changed = append(f.Changed, "removed "+p)
		case !os.IsNotExist(err):
			failed = append(failed, err.Error())
		}
		RemovedFile(p)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return nil
}
