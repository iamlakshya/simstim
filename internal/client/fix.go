package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

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

// fileCheck reads a TOML file into v, and reports keys it does not know.
func fileCheck(name, path string, v any) Check {
	md, err := toml.DecodeFile(path, v)
	switch {
	case os.IsNotExist(err):
		return Check{Name: name, OK: true, Detail: "none yet, at " + path}
	case err != nil:
		return Check{Name: name, Detail: err.Error(), Fix: "fix the file " + path}
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		var ks []string
		for _, k := range keys {
			ks = append(ks, k.String())
		}
		return Check{Name: name, Detail: "unknown keys: " + strings.Join(ks, ", "), Fix: "check their spelling against the configuration reference"}
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

// LocalFixesContext lists or applies local repairs with cancellation.
// Context-bearing callers use it instead of the compatibility wrapper.
func LocalFixesContext(ctx context.Context, apply bool, ids []string) ([]Fix, error) {
	if !apply {
		ids = nil
	}
	return ApplyLocalFixes(ctx, ids)
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
	all := append(sshConfigFix(), controlSocketFix(ctx)...)
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

// sshConfigFix finds the SSH configs that the client generated and no
// longer uses: those of hosts that are gone, and those under an old file
// name. A file counts only when its first line is the client's header, so
// a file that a user made is never removed, and neither is one that a
// saved host's ssh_config names.
func sshConfigFix() []localFix {
	hosts, err := LoadHosts()
	if err != nil {
		return nil
	}
	refs := savedSSHConfigPaths(hosts)
	saved := map[string]bool{}
	for _, h := range hosts {
		saved[h.Name] = true
	}
	files, _ := filepath.Glob(filepath.Join(ConfigDir(), "ssh", "config-*"))
	var candidates []ownedSSHConfig
	var paths []string
	removed, old := 0, 0
	for _, path := range files {
		candidate, ok := snapshotGeneratedSSHConfig(path)
		if !ok || savedSSHConfigReference(path, candidate.info, refs) {
			continue
		}
		candidates = append(candidates, candidate)
		paths = append(paths, path)
		if saved[candidate.owner] {
			old++
		} else {
			removed++
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	var parts []string
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d for hosts you removed", removed))
	}
	if old > 0 {
		parts = append(parts, fmt.Sprintf("%d under an old file name", old))
	}
	return []localFix{{Fix: Fix{ID: "ssh-configs",
		Problem: fmt.Sprintf("%d generated SSH config(s) that no host uses: %s", len(candidates), strings.Join(parts, ", ")),
		Action:  "remove them; " + brand.Name + " writes a host's config again when it connects",
		Command: "rm " + strings.Join(paths, " ")},
		run: func(ctx context.Context, f *Fix) error {
			ctx = withLocalRemovalReport(ctx, func(path string) { f.Changed = append(f.Changed, "removed "+path); RemovedFile(path) })
			return removeOrphanSSHConfigs(ctx, candidates)
		}, files: len(candidates), what: "generated SSH configs"}}
}

func controlSocketFix(ctx context.Context) []localFix {
	candidates := ownedControlSockets(ctx)
	if len(candidates) == 0 {
		return nil
	}
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.path())
	}
	return []localFix{{Fix: Fix{ID: "control-sockets",
		Problem: fmt.Sprintf("%d ControlMaster socket(s) whose SSH master has died, which can make connections hang or fail", len(candidates)),
		Action:  "remove them; the next connection starts a new master",
		Command: "rm " + strings.Join(paths, " ")},
		run: func(ctx context.Context, f *Fix) error {
			ctx = withLocalRemovalReport(ctx, func(path string) {
				f.Changed = append(f.Changed, "removed "+path)
				RemovedFile(path)
			})
			return runOwnedControlFix(ctx, candidates)
		},
		files: len(candidates), what: "ControlMaster sockets"}}
}

// RemovedFile is called after each file that a local fix removes. Tests
// cancel there, between two files or two fixes; nothing else sets it.
var RemovedFile = func(string) {}
