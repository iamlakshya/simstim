package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Idle stop, for VMs that a provider made: a systemd user timer runs
// `<helper> idle check` every few minutes. When no client has been connected
// and no session has been working for the idle time, the check powers the VM
// off with `sudo -n systemctl poweroff`. No daemon is added, and no
// credential is kept on the host. See decision 0013.

// IdleState is what the check remembers between runs.
type IdleState struct {
	// BootID is the boot the state belongs to; a new boot starts the count
	// again.
	BootID string `json:"boot_id"`
	// BusyAt is the last time a client was connected or a session worked.
	BusyAt time.Time `json:"busy_at"`
}

// IdleDecide is the check's rule. From the time, the last state, this boot
// and whether anything is busy now, it returns the new state and whether to
// power off.
func IdleDecide(now time.Time, st IdleState, boot string, busy bool, after time.Duration) (IdleState, bool) {
	if busy || st.BootID != boot || st.BusyAt.IsZero() || now.Before(st.BusyAt) {
		return IdleState{BootID: boot, BusyAt: now}, false
	}
	return st, now.Sub(st.BusyAt) >= after
}

// ParseIdleStop parses a sandbox idle_stop setting: empty or "never" disables
// idle stop (returning 0, nil); otherwise it must be a duration of at least
// one minute (such as "30m" or "2h").
func ParseIdleStop(raw string) (time.Duration, error) {
	v := strings.TrimSpace(raw)
	if v == "" || v == "never" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("%q is not a valid idle_stop duration (use a duration of 1m or more such as 30m or 2h, or never)", raw)
	}
	return d, nil
}

// SessionIdleDecide applies IdleDecide to one walled session (isolation spec
// 6.6, P3.6). When after <= 0 (the default until P3.8, question 9), it never
// triggers a stop.
func SessionIdleDecide(now, busyAt time.Time, busy bool, after time.Duration) (nextBusyAt time.Time, stop bool) {
	if after <= 0 {
		return now, false
	}
	st, shouldStop := IdleDecide(now, IdleState{BootID: "session", BusyAt: busyAt}, "session", busy, after)
	return st.BusyAt, shouldStop
}

// IdleInterval is how often the timer runs the check: a fifth of the idle
// time, between 1 and 5 minutes.
func IdleInterval(after time.Duration) time.Duration {
	return min(max(after/5, time.Minute), 5*time.Minute)
}

func idleUnit(kind string) string { return brand.Name + "-idle." + kind }

func userUnitDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

func idleStatePath() string { return filepath.Join(paths.StateDir(), "idle.json") }

// IdleUnits returns the text of the service and the timer for an idle time.
func IdleUnits(helper string, after time.Duration) (service, timer string) {
	service = fmt.Sprintf(`[Unit]
Description=Power off when %[1]s has been idle for %[2]s

[Service]
Type=oneshot
ExecStart=%[3]s idle check --after %[2]s
`, brand.DisplayName, after, helper)
	iv := IdleInterval(after)
	timer = fmt.Sprintf(`[Unit]
Description=Check every %[2]s whether %[1]s is idle

[Timer]
OnBootSec=%[2]s
OnUnitActiveSec=%[2]s
AccuracySec=15s

[Install]
WantedBy=timers.target
`, brand.DisplayName, iv)
	return service, timer
}

// IdleSetup turns idle stop on with an idle time such as "30m", or off with
// "never".
func IdleSetup(after string) (string, error) {
	if after == "never" {
		_ = exec.CommandContext(context.Background(), "systemctl", "--user", "disable", "--now", idleUnit("timer")).Run()
		_ = os.Remove(filepath.Join(userUnitDir(), idleUnit("timer")))
		_ = os.Remove(filepath.Join(userUnitDir(), idleUnit("service")))
		_ = exec.CommandContext(context.Background(), "systemctl", "--user", "daemon-reload").Run()
		return "idle stop is off", nil
	}
	d, err := time.ParseDuration(after)
	if err != nil || d < time.Minute {
		return "", fmt.Errorf("%q is not an idle time: use a duration of a minute or more, such as 30m, or never", after)
	}
	if !SystemdUser() {
		return "", errors.New("idle stop needs a systemd user manager, and this host has none")
	}
	service, timer := IdleUnits(currentHelper(), d)
	if err := os.MkdirAll(userUnitDir(), 0o700); err != nil {
		return "", fmt.Errorf("make the folder for the idle units: %w", err)
	}
	for name, text := range map[string]string{idleUnit("service"): service, idleUnit("timer"): timer} {
		//nolint:gosec // G306: systemd warns about a unit file that others cannot read; its text is public through systemd anyway
		if err := os.WriteFile(filepath.Join(userUnitDir(), name), []byte(text), 0o644); err != nil {
			return "", fmt.Errorf("write the idle unit %s: %w", name, err)
		}
	}
	_ = os.Remove(idleStatePath()) // start counting now
	if out, err := exec.CommandContext(context.Background(), "systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return "", fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(context.Background(), "systemctl", "--user", "enable", "--now", idleUnit("timer")).CombinedOutput(); err != nil {
		return "", fmt.Errorf("enable the idle timer: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if !canPowerOff() {
		return fmt.Sprintf("idle stop is set to %s, but it cannot power off without passwordless sudo", d), nil
	}
	return fmt.Sprintf("powers off after %s with no client connected and no session working", d), nil
}

// idleAfter reads the idle time from the installed service, or 0 when idle
// stop is off.
func idleAfter() time.Duration {
	b, err := os.ReadFile(filepath.Join(userUnitDir(), idleUnit("service")))
	if err != nil {
		return 0
	}
	_, rest, ok := strings.Cut(string(b), "--after ")
	if !ok {
		return 0
	}
	d, _ := time.ParseDuration(strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0]))
	return d
}

func canPowerOff() bool {
	return exec.CommandContext(context.Background(), "sudo", "-n", "true").Run() == nil
}

// IdleDoctor is the doctor's line about idle stop, or nil when it is off.
func IdleDoctor() []Check {
	d := idleAfter()
	if d == 0 {
		return nil
	}
	if !canPowerOff() {
		return []Check{{Name: "idle stop", Detail: fmt.Sprintf("set to %s, but there is no passwordless sudo, so the host never powers off", d),
			Fix: "allow your user `sudo -n systemctl poweroff`, or turn it off: " + brand.Name + " host idle <host> never"}}
	}
	return []Check{{Name: "idle stop", OK: true, Detail: fmt.Sprintf("powers off after %s with no client and no working session", d)}}
}

// IdleCheck runs the check once, at now.
func IdleCheck(after time.Duration, now time.Time) (string, error) {
	var st IdleState
	if b, err := os.ReadFile(idleStatePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	clients := clientsConnected()
	working := anySessionWorking()
	next, off := IdleDecide(now, st, BootID(), clients > 0 || working, after)
	if err := paths.EnsurePrivateDir(paths.StateDir()); err != nil {
		return "", err
	}
	if b, err := json.Marshal(next); err == nil {
		_ = os.WriteFile(idleStatePath(), b, 0o600)
	}
	if !off {
		return fmt.Sprintf("busy %s ago (clients %d, working %v); powers off after %s", now.Sub(next.BusyAt).Round(time.Second), clients, working, after), nil
	}
	out, err := exec.CommandContext(context.Background(), "sudo", "-n", "systemctl", "poweroff").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("idle for %s, but could not power off: %w: %s", after, err, strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("idle for %s: powering off", after), nil
}

// anySessionWorking reports whether a live session is starting or working.
// When the sessions cannot be read, it says yes, to be safe.
func anySessionWorking() bool {
	list, err := List()
	if err != nil {
		return true
	}
	for _, s := range list {
		if s.Alive && (s.State == api.StateWorking || s.State == api.StateStarting) {
			return true
		}
	}
	return false
}

// clientsConnected counts this user's running bridges and raw attaches:
// each is one connected client.
func clientsConnected() int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if isClientCmdline(b) {
			n++
		}
	}
	return n
}

// isClientCmdline reports whether a /proc cmdline is the helper's bridge or
// raw attach.
func isClientCmdline(b []byte) bool {
	argv := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	return len(argv) >= 2 && filepath.Base(argv[0]) == brand.HostBinary && (argv[1] == "bridge" || argv[1] == "attach")
}

// RunIdle runs `idle check --after D`, `idle setup D|never` or `idle status`.
func RunIdle(args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("usage: idle check --after <duration> | setup <duration|never> | status")
	}
	switch args[0] {
	case "check":
		after := idleAfter()
		//nolint:gosec // G602: len(args) == 3 is checked first, which gosec does not follow
		if len(args) == 3 && args[1] == "--after" {
			d, err := time.ParseDuration(args[2])
			if err != nil {
				return "", fmt.Errorf("read the idle time: %w", err)
			}
			after = d
		}
		if after <= 0 {
			return "idle stop is off", nil
		}
		return IdleCheck(after, time.Now())
	case "setup":
		if len(args) != 2 {
			return "", errors.New("usage: idle setup <duration|never>")
		}
		return IdleSetup(args[1])
	case "status":
		if d := idleAfter(); d > 0 {
			return fmt.Sprintf("idle stop after %s; passwordless sudo: %v", d, canPowerOff()), nil
		}
		return "idle stop is off", nil
	}
	return "", fmt.Errorf("unknown idle command %q", args[0])
}
