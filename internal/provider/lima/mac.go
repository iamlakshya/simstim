package lima

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// DefaultMacName is the default host name when adding this Mac with --local.
const DefaultMacName = "mac"

// MinMacOSMajor is the oldest macOS release supported for a Mac host (spec 18.4).
const MinMacOSMajor = 15

// Test hooks for Mac hardware and OS version probes.
var (
	macOSVersionFunc = readMacOSVersion
	macHardwareFunc  = readMacHardware
)

// CheckMacHost verifies that this computer can run as a local Mac host:
// Apple silicon with macOS 15 or later (spec 18.4).
func CheckMacHost(ctx context.Context) error {
	if goos != "darwin" {
		return &client.UserError{
			Msg:  "`host add --local` runs only on macOS",
			Hint: fmt.Sprintf("Use %s host create lima <name> for a local VM, or %s host add <name> <ssh-target> for an SSH host.", brand.Name, brand.Name),
			Code: "not_mac",
			Kind: client.KindUsage,
		}
	}
	if goarch != "arm64" {
		return &client.UserError{
			Msg:  "this Mac cannot be a host: it needs Apple silicon and macOS 15 or later",
			Hint: "Use a Linux host: " + brand.Name + " host add",
			Code: "mac_unsupported",
		}
	}
	ver, err := macOSVersionFunc(ctx)
	if err != nil {
		return fmt.Errorf("read macOS version: %w", err)
	}
	major, ok := parseMajorVersion(ver)
	if !ok || major < MinMacOSMajor {
		return &client.UserError{
			Msg:  fmt.Sprintf("this Mac cannot be a host: macOS %s is older than macOS %d", strings.TrimSpace(ver), MinMacOSMajor),
			Hint: "Update macOS to 15 or later, or use a Linux host: " + brand.Name + " host add",
			Code: "mac_unsupported",
		}
	}
	return nil
}

// MacResources returns default CPU and memory (GiB) sizes for a Mac host VM:
// strictly less than half of this Mac's CPUs and memory, because Apple's
// Virtualization framework does not return all freed memory to macOS (spec 18.4).
func MacResources(ctx context.Context) (cpus, memGiB int) {
	totalCPUs, totalBytes, err := macHardwareFunc(ctx)
	if err != nil || totalCPUs <= 0 || totalBytes <= 0 {
		return DefaultCPUs, DefaultMemoryGiB
	}
	cpus = (totalCPUs - 1) / 2
	if cpus < 2 {
		cpus = max(1, totalCPUs/2)
	}
	totalGiB := int(totalBytes >> 30)
	memGiB = (totalGiB - 1) / 2
	if memGiB < 2 {
		memGiB = max(2, totalGiB/2)
	}
	return cpus, memGiB
}

func parseMajorVersion(ver string) (int, bool) {
	head, _, _ := strings.Cut(strings.TrimSpace(ver), ".")
	n, err := strconv.Atoi(head)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func readMacOSVersion(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
	if err != nil {
		return "", fmt.Errorf("sw_vers -productVersion: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func readMacHardware(ctx context.Context) (cpus int, memBytes int64, err error) {
	out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.ncpu", "hw.memsize").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("sysctl hw.ncpu hw.memsize: %w", err)
	}
	lines := strings.Fields(string(out))
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("unexpected sysctl output: %q", string(out))
	}
	cpus, err = strconv.Atoi(lines[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse hw.ncpu: %w", err)
	}
	memBytes, err = strconv.ParseInt(lines[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse hw.memsize: %w", err)
	}
	return cpus, memBytes, nil
}
