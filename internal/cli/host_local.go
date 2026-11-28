package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/lima"
)

type localMacOpts struct {
	local     bool
	cpus      int
	memoryGiB int
	diskGiB   int
	autostart string
	idleStop  string
}

// Test hooks for `host add --local`.
var (
	checkLocalMac    = lima.CheckMacHost
	localMacSizing   = lima.MacResources
	ensurePinnedLima = lima.EnsurePinned
	localMacProvider = func() provider.Provider { return lima.Provider{} }
)

func addLocalMacHost(cmd *cobra.Command, args []string, opts localMacOpts, labels map[string]string, floor string, trusted, yes bool) error {
	if len(args) > 1 {
		return &client.UserError{
			Msg:  "`host add --local` takes at most one name and no SSH target",
			Hint: brand.Name + " host add --local [<name>]",
			Code: "bad_args",
			Kind: client.KindUsage,
		}
	}
	ctx := cmd.Context()
	if err := checkLocalMac(ctx); err != nil {
		return err
	}
	if !lima.ValidAutostart(opts.autostart) {
		return &client.UserError{
			Msg:  fmt.Sprintf("%q is not an autostart mode", opts.autostart),
			Hint: "Use --autostart boot, login or off.",
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	name := lima.DefaultMacName
	if len(args) == 1 {
		name = args[0]
	}
	cpus, mem := localMacSizing(ctx)
	if opts.cpus > 0 {
		cpus = opts.cpus
	}
	if opts.memoryGiB > 0 {
		mem = opts.memoryGiB
	}
	disk := opts.diskGiB
	if disk <= 0 {
		disk = lima.DefaultDiskGiB
	}
	autostart := opts.autostart
	if autostart == "" {
		autostart = lima.AutostartBoot
	}
	idleStop := opts.idleStop
	if idleStop == "" {
		idleStop = "never"
	}
	p := localMacProvider()
	req := provider.Request{
		Name:              name,
		CPUs:              cpus,
		MemoryGiB:         mem,
		DiskGiB:           disk,
		IdleStop:          idleStop,
		Autostart:         autostart,
		AutostartExplicit: cmd.Flags().Changed("autostart"),
		Interactive:       inputOn(cmd),
		Out:               os.Stderr,
	}
	if err := onboard.CheckCreate(p, req); err != nil {
		return err
	}
	unlock, err := client.LockHost(ctx, req.Name, "host add")
	if err != nil {
		return err
	}
	defer unlock()
	fmt.Fprintf(os.Stderr, "Setting up this Mac as %s (Lima %s, %d CPUs, %d GiB memory, %d GiB disk)…\n",
		req.Name, lima.PinnedVersion, req.CPUs, req.MemoryGiB, req.DiskGiB)
	start := time.Now()
	if _, err := ensurePinnedLima(ctx, os.Stderr); err != nil {
		return err
	}
	h, err := onboard.Create(ctx, p, req)
	if err != nil {
		return err
	}
	if len(labels) > 0 {
		h.Labels = labels
	}
	h.WallFloor = floor
	h.Trusted = trusted
	fmt.Fprintf(os.Stderr, "  ✓ created %s %s in %s\n\n", h.Machine.Provider, h.Machine.ID, time.Since(start).Round(time.Second))
	// Setting up this Mac's VM counts as consent to install the helper inside it.
	if err := addHostWithKey(ctx, prompts(cmd), h, "", true, yes); err != nil {
		if isCancel(err) || ctx.Err() != nil {
			return err
		}
		return &client.UserError{
			Msg:  fmt.Sprintf("%s was created, but setting it up failed: %v", h.Name, err),
			Hint: brand.Name + " host doctor " + h.Name + ", or " + brand.Name + " host rm " + h.Name + " --destroy",
			Code: "setup_failed",
		}
	}
	if powersOffWhenIdle(h.Machine.Provider) {
		if err := applyIdle(ctx, h); err != nil {
			fmt.Fprintf(os.Stderr, "  ✕ idle stop: %v\n", err)
		}
	}
	fmt.Fprintf(os.Stderr, "Added this Mac as %s in %s.\n", h.Name, time.Since(start).Round(time.Second))
	return nil
}

// Keep io imported if tests inspect ensurePinnedLima signature directly.
var _ func(context.Context, io.Writer) (string, error) = ensurePinnedLima
