package client

import (
	"context"
	"fmt"
	"net"
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
	run func() error
}

// LocalFixes lists the problems on this computer the doctor can fix: SSH
// configs generated for hosts that are gone, and ControlMaster sockets whose
// master has died. With apply it runs the ones named in ids.
func LocalFixes(apply bool, ids []string) []Fix {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	all := append(sshConfigFix(), controlSocketFix()...)
	out := make([]Fix, 0, len(all))
	for _, f := range all {
		if apply && want[f.ID] {
			if err := f.run(); err != nil {
				f.Error = err.Error()
			} else {
				f.Done = true
			}
		}
		out = append(out, f.Fix)
	}
	return out
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
		run: func() error { return removeFiles(orphans) }}}
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
		run: func() error { return removeFiles(stale) }}}
}

func removeFiles(list []string) error {
	var failed []string
	for _, p := range list {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return nil
}
