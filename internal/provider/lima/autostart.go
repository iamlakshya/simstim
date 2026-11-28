package lima

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// Autostart modes for a local Lima VM on macOS (via `limactl autostart`).
const (
	AutostartBoot  = "boot"
	AutostartLogin = "login"
	AutostartOff   = "off"
	// AutostartName is the host doctor check name for Lima autostart.
	AutostartName = "autostart"
)

// Test hooks for launchd plist locations and sudo availability.
var (
	daemonPlistDir = func() string { return "/Library/LaunchDaemons" }
	agentPlistDir  = func() string {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, "Library", "LaunchAgents")
	}
	sudoAvailable   = checkSudoNonInteractive
	bootWaitTimeout = 2 * time.Minute
	bootWaitPoll    = time.Second
)

// ValidAutostart reports whether mode is a valid --autostart value.
func ValidAutostart(mode string) bool {
	switch mode {
	case "", AutostartBoot, AutostartLogin, AutostartOff:
		return true
	default:
		return false
	}
}

func daemonPlistPath(inst string) string {
	return filepath.Join(daemonPlistDir(), "io.lima-vm.daemon."+inst+".plist")
}

func agentPlistPath(inst string) string {
	return filepath.Join(agentPlistDir(), "io.lima-vm.autostart."+inst+".plist")
}

// AutostartState returns the launchd autostart state for inst on macOS:
// "boot", "login", or "off".
func AutostartState(inst string) string {
	if _, err := os.Stat(daemonPlistPath(inst)); err == nil {
		return AutostartBoot
	}
	if _, err := os.Stat(agentPlistPath(inst)); err == nil {
		return AutostartLogin
	}
	return AutostartOff
}

// EnableAutostart registers inst with `limactl autostart enable`, never
// writing a launchd plist itself. It returns the enabled mode ("boot" or
// "login"). When mode is "boot" and input is off without passwordless sudo,
// an implicit default falls back to "login" and prints the boot command.
func EnableAutostart(ctx context.Context, out io.Writer, inst, mode string, explicit, interactive bool) (string, error) {
	if out == nil {
		out = io.Discard
	}
	if goos != "darwin" || mode == "" || mode == AutostartOff {
		return "", nil
	}
	bin := limaCommand()
	if mode == AutostartBoot {
		canSudo := interactive || sudoAvailable(ctx)
		if !canSudo {
			bootCmd := fmt.Sprintf("%s autostart enable --condition=boot %s", client.ShellQuote(bin), client.ShellQuote(inst))
			if explicit {
				return "", &client.UserError{
					Msg:  "enabling start at boot needs sudo, and input is off",
					Hint: fmt.Sprintf("Run %s in a terminal, or pass --autostart=login.", bootCmd),
					Code: "autostart_sudo",
				}
			}
			fmt.Fprintf(out, "  ! start at boot needs sudo; enabling start at login instead.\n")
			fmt.Fprintf(out, "    To start at boot before login, run: %s\n", bootCmd)
			mode = AutostartLogin
		}
	}

	if mode == AutostartBoot {
		// `limactl autostart enable --condition=boot` runs `launchctl bootstrap
		// system`, which starts the VM immediately under launchd. Stop the VM
		// first so the daemon starts it cleanly without racing the current hostagent.
		if _, err := provider.Exec(ctx, install, bin, "stop", inst); err != nil {
			return "", fmt.Errorf("stop %s before enabling boot autostart: %w", inst, err)
		}
		fmt.Fprintf(out, "  Enabling start at boot: %s autostart enable --condition=boot %s\n", bin, inst)
		if err := runAutostart(ctx, bin, interactive, "autostart", "enable", "--condition=boot", "--keep-alive", inst); err != nil {
			bootCmd := fmt.Sprintf("%s autostart enable --condition=boot %s", client.ShellQuote(bin), client.ShellQuote(inst))
			return "", &client.UserError{
				Msg:  fmt.Sprintf("could not enable start at boot for %s: %v", inst, err),
				Hint: fmt.Sprintf("Run %s in a terminal, or use --autostart=login.", bootCmd),
				Code: "lima_autostart",
			}
		}
		if err := waitRunning(ctx, inst); err != nil {
			return "", err
		}
		fmt.Fprintf(out, "  ✓ autostart: starts at boot (LaunchDaemon via limactl autostart)\n")
		return AutostartBoot, nil
	}

	fmt.Fprintf(out, "  Enabling start at login: %s autostart enable --condition=login %s\n", bin, inst)
	if _, err := provider.Exec(ctx, install, bin, "autostart", "enable", "--condition=login", "--keep-alive", inst); err != nil {
		return "", &client.UserError{
			Msg:  fmt.Sprintf("could not enable autostart for %s: %v", inst, err),
			Hint: fmt.Sprintf("%s autostart enable --condition=login %s", client.ShellQuote(bin), client.ShellQuote(inst)),
			Code: "lima_autostart",
		}
	}
	fmt.Fprintf(out, "  ✓ autostart: starts at login (LaunchAgent via limactl autostart)\n")
	return AutostartLogin, nil
}

// DisableAutostart unregisters any launchd autostart entry for inst before
// deleting the VM.
func DisableAutostart(ctx context.Context, inst string) error {
	if goos != "darwin" {
		return nil
	}
	st := AutostartState(inst)
	if st == AutostartOff {
		return nil
	}
	bin := limaCommand()
	interactive := st == AutostartBoot && client.InputAllowed(ctx)
	if err := runAutostart(ctx, bin, interactive, "autostart", "disable", inst); err != nil {
		return &client.UserError{
			Msg:  fmt.Sprintf("could not disable autostart for %s: %v", inst, err),
			Hint: fmt.Sprintf("Run %s autostart disable %s, then destroy the host again.", client.ShellQuote(bin), client.ShellQuote(inst)),
			Code: "lima_autostart",
		}
	}
	return nil
}

// AutostartCheck returns a host doctor check for Lima autostart on macOS when
// the host is configured for autostart or has a launchd plist registered.
func AutostartCheck(_ context.Context, h client.Host) (api.Check, bool) {
	if goos != "darwin" || h.Machine == nil {
		return api.Check{}, false
	}
	want := h.Machine.Autostart
	state := AutostartState(h.Machine.ID)
	if want == "" && state == AutostartOff {
		return api.Check{}, false
	}
	bin := client.ShellQuote(limaCommand())
	inst := client.ShellQuote(h.Machine.ID)
	c := api.Check{Name: AutostartName}
	switch state {
	case AutostartBoot:
		c.OK = true
		c.Detail = h.Name + " starts at boot (LaunchDaemon via limactl autostart)"
	case AutostartLogin:
		if want == AutostartBoot {
			c.Detail = h.Name + " starts at login, not at boot"
			c.Fix = fmt.Sprintf("%s autostart enable --condition=boot %s", bin, inst)
		} else {
			c.OK = true
			c.Detail = h.Name + " starts at login (LaunchAgent via limactl autostart)"
		}
	default:
		cond := want
		if cond == "" || cond == AutostartOff {
			cond = AutostartBoot
		}
		c.Detail = h.Name + " is not registered with limactl autostart"
		c.Fix = fmt.Sprintf("%s autostart enable --condition=%s %s", bin, cond, inst)
	}
	return c, true
}

func runAutostart(ctx context.Context, bin string, interactive bool, args ...string) error {
	if !interactive {
		_, err := provider.Exec(ctx, install, bin, args...)
		return err
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return &client.UserError{Msg: bin + " is not installed, or not on your PATH", Hint: install, Code: "tool_missing"}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s %s: %w", bin, args[0], ctx.Err())
		}
		return fmt.Errorf("%s %s: %w", bin, args[0], err)
	}
	return nil
}

func waitRunning(ctx context.Context, inst string) error {
	deadline := time.Now().Add(bootWaitTimeout)
	for {
		in, err := find(ctx, inst)
		if err == nil && in != nil && state(in.Status) == client.MachineRunning {
			return nil
		}
		if time.Now().After(deadline) {
			return &client.UserError{
				Msg:  inst + " did not come back up after enabling autostart",
				Hint: client.ShellQuote(limaCommand()) + " list",
				Code: "lima_autostart",
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for %s to start: %w", inst, ctx.Err())
		case <-time.After(bootWaitPoll):
		}
	}
}

func checkSudoNonInteractive(ctx context.Context) bool {
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := exec.CommandContext(cctx, "sudo", "-n", "true").Run()
	return err == nil && !errors.Is(cctx.Err(), context.DeadlineExceeded)
}
