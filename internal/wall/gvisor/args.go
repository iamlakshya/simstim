package gvisor

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// RunArgs builds the runsc command-line arguments for launching a sandbox
// (isolation spec 21.4). It never passes --rootless, always sets
// --network=none, --directfs=false, --ignore-cgroups and --host-uds=none, and
// maps hostFD to guest fd 3 via --pass-fd when hostFD >= 0.
func RunArgs(rootDir, bundleDir, containerID string, hostFD int) []string {
	args := []string{
		"--root", rootDir,
		"--network=none",
		"--directfs=false",
		"--ignore-cgroups",
		"--host-uds=none",
		"run",
		"--bundle", bundleDir,
	}
	if hostFD >= 0 {
		args = append(args, "--pass-fd", strconv.Itoa(hostFD)+":3")
	}
	args = append(args, containerID)
	return args
}

// KillArgs builds the runsc arguments to signal containerID inside rootDir.
func KillArgs(rootDir, containerID, sig string) []string {
	return []string{
		"--root", rootDir,
		"--ignore-cgroups",
		"kill", containerID, sig,
	}
}

// DeleteArgs builds the runsc arguments to force-remove containerID state from
// rootDir after exit.
func DeleteArgs(rootDir, containerID string) []string {
	return []string{
		"--root", rootDir,
		"--ignore-cgroups",
		"delete", "--force", containerID,
	}
}

// ValidateRunArgs verifies the security invariants of spec 21.4 on a runsc
// argument slice before execution: never --rootless, always --network=none,
// always --host-uds=none, always --directfs=false and --ignore-cgroups.
func ValidateRunArgs(args []string) error {
	var hasNetworkNone, hasHostUDSNone, hasDirectFSFalse, hasIgnoreCgroups bool
	for _, a := range args {
		if a == "run" {
			break
		}
		if a == "--rootless" || a == "-rootless" || strings.HasPrefix(a, "--rootless=") || strings.HasPrefix(a, "-rootless=") {
			return errors.New("gvisor: --rootless is forbidden by spec 21.4")
		}
		switch {
		case a == "--network=none" || a == "-network=none":
			hasNetworkNone = true
		case a == "--network" || a == "-network" || strings.HasPrefix(a, "--network=") || strings.HasPrefix(a, "-network="):
			return fmt.Errorf("gvisor: forbidden network flag %q (must be --network=none)", a)
		case a == "--host-uds=none" || a == "-host-uds=none":
			hasHostUDSNone = true
		case a == "--host-uds" || a == "-host-uds" || strings.HasPrefix(a, "--host-uds=") || strings.HasPrefix(a, "-host-uds="):
			return fmt.Errorf("gvisor: forbidden host-uds flag %q (must be --host-uds=none)", a)
		case a == "--directfs=false" || a == "-directfs=false":
			hasDirectFSFalse = true
		case a == "--directfs" || a == "-directfs" || strings.HasPrefix(a, "--directfs=") || strings.HasPrefix(a, "-directfs="):
			return fmt.Errorf("gvisor: forbidden directfs flag %q (must be --directfs=false)", a)
		case a == "--ignore-cgroups" || a == "-ignore-cgroups" || a == "--ignore-cgroups=true":
			hasIgnoreCgroups = true
		}
	}
	if !hasNetworkNone || !hasHostUDSNone || !hasDirectFSFalse || !hasIgnoreCgroups {
		return errors.New("gvisor: missing required isolation flags (--network=none --directfs=false --ignore-cgroups --host-uds=none)")
	}
	return nil
}
