package chv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const remoteTimeout = 5 * time.Second

// VMInfo is the subset of `ch-remote info` JSON we read (spec 20.3).
type VMInfo struct {
	State string `json:"state"`
}

// PowerButton presses the virtual ACPI power button on the VM listening at
// apiSocket using the shipped ch-remote CLI (reuse.md part A, spec 20.3).
func PowerButton(ctx context.Context, chRemoteBin, apiSocket string) error {
	_, err := runCHRemote(ctx, chRemoteBin, apiSocket, "power-button")
	return err
}

// Shutdown asks the VMM to shut down the VM via ch-remote.
func Shutdown(ctx context.Context, chRemoteBin, apiSocket string) error {
	_, err := runCHRemote(ctx, chRemoteBin, apiSocket, "shutdown")
	return err
}

// Info queries VM state through `ch-remote --api-socket <sock> info`.
func Info(ctx context.Context, chRemoteBin, apiSocket string) (VMInfo, error) {
	out, err := runCHRemote(ctx, chRemoteBin, apiSocket, "info")
	if err != nil {
		return VMInfo{}, err
	}
	var info VMInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return VMInfo{}, fmt.Errorf("decode ch-remote info output: %w. Try: check ch-remote version matches Cloud Hypervisor v%s", err, PinnedVMMVersion)
	}
	return info, nil
}

func runCHRemote(ctx context.Context, chRemoteBin, apiSocket, subcmd string) ([]byte, error) {
	if chRemoteBin == "" {
		return nil, fmt.Errorf("ch-remote path is empty. Try: install the pinned Cloud Hypervisor v%s runtime", PinnedVMMVersion)
	}
	if apiSocket == "" {
		return nil, errors.New("VMM API socket path is empty. Try: start the microvm first")
	}
	tctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()

	cmd := exec.CommandContext(tctx, chRemoteBin, "--api-socket", apiSocket, subcmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return nil, fmt.Errorf("ch-remote %s (%s): %w. Try: check the session's supervisor log", subcmd, detail, err)
		}
		return nil, fmt.Errorf("ch-remote %s: %w. Try: check the session's supervisor log", subcmd, err)
	}
	return stdout.Bytes(), nil
}
