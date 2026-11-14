package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// RuntimeOptions chooses delivery for one host. Probe may supply this
// connection's fresh capabilities. Progress is optional terminal output.
type RuntimeOptions struct {
	Mode     string
	Probe    *api.CapProbe
	Progress io.Writer
}

// RuntimeFetchMode reads the images fetch setting or command-line override.
// Empty means the default, auto.
func RuntimeFetchMode(mode string) (string, error) {
	switch mode {
	case "", "auto":
		return "auto", nil
	case "host", "laptop":
		return mode, nil
	default:
		return "", &UserError{Msg: fmt.Sprintf("%q is not an images fetch mode", CleanLine(mode)), Hint: "Use auto, host or laptop.", Code: "bad_images_fetch", Kind: KindUsage}
	}
}

// EnsureRuntime installs the runtime named by this client's signed-release
// manifest on this connection's host. Inventory replies never choose expected
// metadata, bytes, credentials or a different host's transport.
func EnsureRuntime(ctx context.Context, c *Conn, options RuntimeOptions) (result api.RuntimeStatus, returned error) {
	defer func() {
		if c != nil {
			returned = runtimeClientError(c.Host.Name, returned)
		}
	}()
	var empty api.RuntimeStatus
	mode, err := RuntimeFetchMode(options.Mode)
	if err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	// A helper-only development build needs no probe or runtime download.
	m, err := ReleaseManifest()
	if err != nil {
		return empty, err
	}
	hasRuntime := false
	for _, item := range m.Items {
		hasRuntime = hasRuntime || runtimecache.IsRuntime(item.Kind)
	}
	if !hasRuntime {
		return empty, nil
	}
	probe := options.Probe
	if probe == nil {
		value, err := c.CapProbe(ctx, supply.ReleaseFolder(m.Release))
		if err != nil {
			return empty, err
		}
		probe = &value
	}
	if probe.OS != "linux" {
		return empty, &UserError{Msg: "runtime delivery needs a Linux host", Hint: "Use a Linux host on amd64 or arm64.", Code: "runtime_platform"}
	}
	request, err := runtimeRequestFor(probe.Arch)
	if err != nil {
		return empty, err
	}
	status, err := runtimeStatus(ctx, c, request)
	if err != nil {
		return empty, err
	}
	if mode == "auto" {
		if probe.ReleaseReachable != nil && *probe.ReleaseReachable {
			mode = "host"
		} else {
			mode = "laptop"
		}
		if mode == "host" {
			status, err = fetchRuntimeHost(ctx, c, request, options.Progress)
			if err == nil {
				return status, nil
			}
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return empty, err
			}
			runtimeLine(options.Progress, "The host runtime download failed. Copying from this laptop once.\n")
			mode = "laptop"
		}
	}
	if mode == "host" {
		return fetchRuntimeHost(ctx, c, request, options.Progress)
	}
	status, err = copyRuntimeLaptop(ctx, c, request, status, options.Progress, filepath.Join(CacheDir(), "runtime"))
	if err != nil {
		return empty, runtimeClientError(c.Host.Name, err)
	}
	return status, nil
}

func runtimeStatus(ctx context.Context, c *Conn, request runtimecache.RuntimeRequest) (api.RuntimeStatus, error) {
	var status api.RuntimeStatus
	if err := c.Call(ctx, "runtime.status", request, &status); err != nil {
		return status, err
	}
	if status.Runtime != request.Manifest.Runtime || len(status.Items) != len(request.Manifest.Items) {
		return status, runtimeProtocolError()
	}
	expected := map[string]supply.Item{}
	sizes := map[string]int64{}
	for _, it := range request.Manifest.Items {
		expected[it.Kind+"\x00"+it.Name] = it
		sizes[it.SHA256] = it.Size
	}
	for _, file := range status.Items {
		key := file.Kind + "\x00" + file.Name
		it, ok := expected[key]
		if !ok || it.SHA256 != file.SHA256 || it.Size != file.Size {
			return status, runtimeProtocolError()
		}
		delete(expected, key)
	}
	if len(status.Transfers) > runtimecache.MaxTransfers {
		return status, runtimeProtocolError()
	}
	for _, transfer := range status.Transfers {
		size, ok := sizes[transfer.SHA256]
		if !ok || transfer.Received < 0 || transfer.Received > size {
			return status, runtimeProtocolError()
		}
	}
	// These are host claims. Verified compatibility needs the actual driver
	// registry and a separate proof of the installed runtime at admission.
	for i := range status.Drivers {
		status.Drivers[i].Verified = false
	}
	return status, nil
}

func runtimeProtocolError() error {
	return &UserError{Msg: "the host returned runtime entries outside this client's request", Hint: "Upgrade the helper and try again.", Code: "runtime_protocol"}
}
func runtimeClientError(host string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	return &UserError{Msg: "could not deliver the runtime to " + CleanLine(host) + ": " + CleanLine(err.Error()), Hint: brand.Name + " host upgrade " + ShellWord(host), Code: "runtime_install"}
}
func runtimeLine(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format, args...)
	}
}

func runtimeReady(ctx context.Context, c *Conn, request runtimecache.RuntimeRequest) (api.RuntimeStatus, error) {
	status, err := runtimeStatus(ctx, c, request)
	if err != nil {
		return status, err
	}
	for _, file := range status.Items {
		if !file.Present {
			return status, &UserError{Msg: "the host still reports a missing runtime file after installation", Hint: "Run host upgrade again and check the host's disk space.", Code: "runtime_incomplete"}
		}
	}
	return status, nil
}
