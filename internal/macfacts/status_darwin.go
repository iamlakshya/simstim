//go:build darwin

package macfacts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// LocalStatusV1 is root's fixed advisory projection. It cannot select a
// command or runtime and is never returned wholesale over the SSH route.
type LocalStatusV1 struct {
	Version      int    `json:"v"`
	Account      string `json:"account"`
	UID          int    `json:"uid"`
	StartMode    string `json:"start_mode"`
	LaunchTarget string `json:"launch_target"`
}

var localAccountPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ReadForAccount consumes only the actual native account's fixed root-owned
// status. The SSH dispatcher separately restricts the literal facts-v1 action.
func ReadForAccount(ctx context.Context, account string) (Facts, error) {
	var empty Facts
	if !localAccountPattern.MatchString(account) || account == "root" {
		return empty, fmt.Errorf("the Mac facts account is invalid")
	}
	path := filepath.Join("/usr/local/libexec", brand.Name, "mac", account, "facts.json")
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return empty, err
		}
		native, ok := info.Sys().(*syscall.Stat_t)
		if !ok || native.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return empty, fmt.Errorf("the Mac facts status is not protected by root")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return empty, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, MaxBytes+1))
	decoder.DisallowUnknownFields()
	var status LocalStatusV1
	if decoder.Decode(&status) != nil || decoder.Decode(new(any)) != io.EOF || status.Version != 1 || status.Account != account || status.UID <= 0 || os.Getuid() != status.UID || os.Geteuid() != status.UID || status.StartMode != "daemon" && status.StartMode != "user-agent" {
		return empty, fmt.Errorf("the Mac facts status has no matching native account")
	}
	prefix := "system/io.lima-vm.daemon." + account + "-host"
	if status.StartMode == "user-agent" {
		parts := strings.Split(status.LaunchTarget, "/")
		if len(parts) != 3 || parts[0] != "gui" || parts[2] != "io.lima-vm.autostart."+account+"-host" {
			return empty, fmt.Errorf("the Mac facts launch target is invalid")
		}
		uid, err := strconv.Atoi(parts[1])
		if err != nil || uid <= 0 {
			return empty, fmt.Errorf("the Mac facts GUI user is invalid")
		}
		prefix = status.LaunchTarget
	}
	if status.LaunchTarget != prefix {
		return empty, fmt.Errorf("the Mac facts launch target conflicts with its fixed account")
	}
	facts := Read(ctx)
	facts.StartMode = status.StartMode
	limited, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(limited, "/bin/launchctl", "print", status.LaunchTarget)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	var output cappedOutput
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err == nil && !output.overflow {
		facts.LaunchEnabled, facts.LaunchKnown = true, true
	} else if strings.Contains(string(output.data), "Could not find service") {
		facts.LaunchKnown = true
	}
	return facts, nil
}
