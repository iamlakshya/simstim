package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
)

// Check is one line of the host doctor.
type Check = api.Check

// HostInfo is what the host doctor reports about the host.
type HostInfo = api.HostInfo

// Doctor inspects the host.
func Doctor() HostInfo {
	hn, _ := os.Hostname()
	u, _ := user.Current()
	info := HostInfo{Hostname: hn, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(),
		MemTotal: MemTotal(), Version: version.Version, Agents: map[string]string{}, MemoryMax: DefaultMemoryMax()}
	if u != nil {
		info.User = u.Username
	}
	if b, err := exec.CommandContext(context.Background(), "uname", "-r").Output(); err == nil {
		info.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				info.Distro = strings.Trim(v, `"`)
			}
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		info.DiskFree = diskFree(h)
	}
	login, err := LoginEnv(15 * time.Second)
	if err != nil {
		login = os.Environ()
	}
	path := envValue(login, "PATH")
	for _, a := range agents.Table {
		for _, b := range a.Binaries {
			if b == "$SHELL" {
				info.Agents[a.Kind] = LoginShell()
				break
			}
			if p, err := lookPath(b, path); err == nil {
				info.Agents[a.Kind] = p
				break
			}
		}
	}
	sd := SystemdUser()
	info.Checks = append(info.Checks, Check{Name: "systemd user manager", OK: sd,
		Detail: map[bool]string{true: "sessions run as user services with memory caps", false: "sessions run as detached processes without memory caps"}[sd]})
	if sd {
		linger := lingerOn()
		info.Checks = append(info.Checks, Check{Name: "linger", OK: linger,
			Detail: map[bool]string{true: "sessions survive logout", false: "sessions stop when your last login ends"}[linger],
			Fix:    "loginctl enable-linger $USER"})
		mem := memoryDelegated()
		info.Checks = append(info.Checks, Check{Name: "memory caps", OK: mem,
			Detail: map[bool]string{true: "cgroup v2 memory controller is delegated", false: "the memory controller is not delegated to the user manager"}[mem]})
		if mem {
			info.Checks = append(info.Checks, sessionsMemoryCheck())
		}
	}
	gt := HasTerminfo("xterm-ghostty")
	info.Checks = append(info.Checks, Check{Name: "Ghostty terminfo", OK: gt,
		Detail: map[bool]string{true: "xterm-ghostty is installed", false: "xterm-ghostty is missing; sessions fall back to xterm-256color"}[gt],
		Fix:    "infocmp -x xterm-ghostty | ssh <host> tic -x -"})
	info.Checks = append(info.Checks, Check{Name: "runtime folder", OK: true, Detail: paths.RuntimeDir()})
	info.Checks = append(info.Checks, IdleDoctor()...)
	return info
}

func lingerOn() bool {
	u, err := user.Current()
	if err != nil {
		return false
	}
	out, err := exec.CommandContext(context.Background(), "loginctl", "show-user", u.Username, "-p", "Linger", "--value").Output()
	return err == nil && strings.TrimSpace(string(out)) == "yes"
}

// SetupArgs asks the host to prepare itself: linger and terminfo entries.
type SetupArgs = api.SetupArgs

// SetupResult reports each step that setup took.
type SetupResult = api.SetupResult

// Setup turns on linger and installs terminfo entries.
func Setup(a SetupArgs) SetupResult {
	var res SetupResult
	if a.Linger && SystemdUser() {
		if lingerOn() {
			res.Steps = append(res.Steps, Check{Name: "linger", OK: true, Detail: "already on"})
		} else {
			u, _ := user.Current()
			detail := "turned on"
			err := exec.CommandContext(context.Background(), "loginctl", "enable-linger", u.Username).Run()
			if err != nil {
				// sudo -n never asks for a password: it works only where sudo
				// needs none, and the step says that it was used.
				err = exec.CommandContext(context.Background(), "sudo", "-n", "loginctl", "enable-linger", u.Username).Run()
				detail = "turned on with sudo -n loginctl enable-linger " + u.Username
			}
			ok := err == nil && lingerOn()
			if !ok {
				detail = "could not turn it on"
			}
			res.Steps = append(res.Steps, Check{Name: "linger", OK: ok, Detail: detail,
				Fix: "sudo loginctl enable-linger " + u.Username})
		}
	}
	if a.Linger {
		res.Steps = append(res.Steps, InstallRestoreUnit())
	}
	for name, src := range a.Terminfo {
		if HasTerminfo(name) {
			res.Steps = append(res.Steps, Check{Name: "terminfo " + name, OK: true, Detail: "already installed"})
			continue
		}
		err := installTerminfo(src)
		ok := err == nil && HasTerminfo(name)
		d := "installed"
		if !ok {
			d = fmt.Sprintf("could not install: %v", err)
		}
		res.Steps = append(res.Steps, Check{Name: "terminfo " + name, OK: ok, Detail: d})
	}
	return res
}

// installTerminfo compiles a terminfo source into ~/.terminfo.
func installTerminfo(src string) error {
	f, err := os.CreateTemp("", "terminfo-*")
	if err != nil {
		return fmt.Errorf("write the terminfo source: %w", err)
	}
	_, _ = f.WriteString(src)
	f.Close()
	defer os.Remove(f.Name())
	home, _ := os.UserHomeDir()
	out, err := exec.CommandContext(context.Background(), "tic", "-x", "-o", filepath.Join(home, ".terminfo"), f.Name()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tic: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
