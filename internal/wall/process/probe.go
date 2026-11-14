// Package process probes the host features needed by the process wall.
// The complete sandbox runtime is installed by the process driver separately.
package process

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/wall"
)

type probeSystem struct {
	os       string
	kernel   string
	readFile func(string) ([]byte, error)
	landlock func() int
	smoke    func(context.Context) smokeResult
}

type smokeResult struct {
	UserNS  bool   `json:"userns"`
	Seccomp bool   `json:"seccomp"`
	Error   string `json:"error,omitempty"`
}

// Probe checks the process wall's prerequisites in a child. It reports the
// runtime as unavailable until a complete process Driver is built in.
func Probe(ctx context.Context) wall.Probe {
	return probe(ctx, localSystem())
}

func probe(ctx context.Context, sys probeSystem) wall.Probe {
	p := wall.Probe{Driver: "process", Level: wall.Process, Facts: map[string]string{
		"kernel": sys.kernel, "runtime_available": "false", "userns": "false", "seccomp": "false",
		"landlock_abi": "0", "apparmor_userns": "false", "prerequisite_ready": "false",
	}}
	p.Fix = "Use a helper release with the process sandbox runtime."
	if err := ctx.Err(); err != nil {
		p.Reason = "process wall probe was cancelled"
		return p
	}
	if sys.os != "linux" {
		p.Reason = "the process wall needs Linux"
		p.Fix = "Use a Linux host for process sandboxes."
		return p
	}
	if sys.readFile != nil {
		p.Facts["apparmor_userns"] = strconv.FormatBool(readInt(sys.readFile, "/proc/sys/kernel/apparmor_restrict_unprivileged_userns") > 0)
		p.Facts["max_user_namespaces"] = strconv.FormatInt(readInt(sys.readFile, "/proc/sys/user/max_user_namespaces"), 10)
	}
	if sys.landlock != nil {
		p.Facts["landlock_abi"] = strconv.Itoa(max(0, sys.landlock()))
	}
	if !kernelReady(sys.kernel) {
		p.Reason = "the process wall needs Linux 5.13 or later"
		p.Fix = "Update the host's Linux kernel to 5.13 or later."
		return p
	}
	if sys.smoke != nil {
		result := sys.smoke(ctx)
		p.Facts["userns"] = strconv.FormatBool(result.UserNS)
		p.Facts["seccomp"] = strconv.FormatBool(result.Seccomp)
	}
	return prerequisites(p)
}

func prerequisites(p wall.Probe) wall.Probe {
	switch {
	case p.Facts["userns"] != "true":
		p.Reason = "a child could not make user and mount namespaces and mount tmpfs"
		p.Fix = "Ask the host's owner to allow unprivileged user namespaces."
	case p.Facts["landlock_abi"] == "0":
		p.Reason = "the host has no usable Landlock ABI"
		p.Fix = "Enable Landlock in the host's Linux kernel."
	case p.Facts["seccomp"] != "true":
		p.Reason = "a child could not load a seccomp filter"
		p.Fix = "Enable seccomp filters in the host's Linux kernel."
	default:
		p.Facts["prerequisite_ready"] = "true"
		p.Reason = "the process sandbox runtime is not built into this helper"
	}
	return p
}

func readInt(readFile func(string) ([]byte, error), path string) int64 {
	b, err := readFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func kernelReady(kernel string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(kernel, "%d.%d", &major, &minor); err != nil {
		return false
	}
	return major > 5 || (major == 5 && minor >= 13)
}
