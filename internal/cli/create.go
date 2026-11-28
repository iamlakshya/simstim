package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/docker"
	"github.com/iamlakshya/simstim/internal/provider/e2b"
	"github.com/iamlakshya/simstim/internal/provider/lima"
	"github.com/iamlakshya/simstim/internal/wall"
)

// providers are the built-in providers of `host create`.
var providers = []provider.Provider{lima.Provider{}, docker.Provider{}, e2b.Provider{}}

func init() {
	for _, p := range providers {
		client.RegisterProvider(p.Name(), p.Open)
	}
}

func findProvider(name string) (provider.Provider, error) {
	var names []string
	for _, p := range providers {
		if p.Name() == name {
			return p, nil
		}
		names = append(names, p.Name())
	}
	return nil, &client.UserError{Msg: fmt.Sprintf("no provider called %q", name), Hint: "Use one of: " + strings.Join(names, ", ") + ".", Code: "provider_not_found", Kind: client.KindNotFound}
}

// powersOffWhenIdle reports whether a provider's machines stop themselves
// with the helper's idle timer (VMs), rather than with a lease kept by the
// client (sandboxes), or not at all (containers).
func powersOffWhenIdle(p string) bool { return onboard.PowersOffWhenIdle(p) }

func hostCreateCmd() *cobra.Command {
	var req provider.Request
	var floor, memStr, diskStr string
	var trusted bool
	cmd := &cobra.Command{
		Use:   "create <provider> [<name>]",
		Short: "Make a host with lima, docker or e2b, and connect it",
		Long: "Make a host and set it up in one step: a local Lima VM, a Docker container (experimental),\n" +
			"or an E2B sandbox. Creating a host counts as consent to install the helper on it;\n" +
			"what is installed, and where, is printed. The name defaults to the provider's name.",
		Example: "  " + brand.Name + " host create lima\n  " + brand.Name + " host create docker test --image debian:stable-slim\n  " +
			brand.Name + " host create e2b cloud --template " + e2b.DefaultTemplate,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := findProvider(args[0])
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("memory") {
				if req.Memory, err = parseLegacyGiBSizeFlag(cmd, "--memory", memStr, oldCreateMemoryBare); err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("disk") {
				if req.Disk, err = parseLegacyGiBSizeFlag(cmd, "--disk", diskStr, oldCreateDiskBare); err != nil {
					return err
				}
			}
			if floor != "" {
				lvl, err := wall.ParseLevel(floor)
				if err != nil {
					return &client.UserError{Msg: fmt.Sprintf("--floor: %v", err), Hint: "Pass none, process, gvisor or microvm.", Code: "bad_value", Kind: client.KindUsage}
				}
				floor = lvl.String()
			}
			req.Name = p.Name()
			if len(args) == 2 {
				req.Name = args[1]
			}
			if !lima.ValidAutostart(req.Autostart) {
				return &client.UserError{
					Msg:  fmt.Sprintf("%q is not an autostart mode", req.Autostart),
					Hint: "Use --autostart boot, login or off.",
					Code: "bad_value",
					Kind: client.KindUsage,
				}
			}
			req.AutostartExplicit = cmd.Flags().Changed("autostart")
			req.Interactive = inputOn(cmd)
			req.Out = os.Stderr
			return createHostWithOptions(cmd.Context(), p, req, floor, trusted)
		},
	}
	f := cmd.Flags()
	addCPUs(cmd, &req.CPUs, lima.DefaultCPUs, "lima: CPUs")
	addMemory(cmd, &memStr, fmt.Sprintf("%dG", lima.DefaultMemoryGiB), "lima: memory `size`, such as 4G")
	addDisk(cmd, &diskStr, fmt.Sprintf("%dG", lima.DefaultDiskGiB), "lima: disk `size`, such as 40G (sparse: it grows as it fills)")
	f.StringVar(&req.Image, "image", docker.DefaultImage, "docker: the image, which needs sh, bash and coreutils")
	f.StringVar(&req.Template, "template", e2b.DefaultTemplate, "e2b: the template, which sets CPUs and memory")
	addTokenCommand(cmd, &req.TokenCommand, "e2b: a command that prints the API key, if E2B_API_KEY is not set and the E2B CLI is not logged in")
	f.StringVar(&req.IdleStop, "idle-stop", "", "stop after this long with no client and no working session, or never (lima: 30m, e2b: 15m)")
	f.StringVar(&floor, "floor", "", "weakest sandbox level to pin: none, process, gvisor or microvm")
	f.StringVar(&req.Autostart, "autostart", "", "lima on macOS: start VM automatically: boot, login or off")
	f.BoolVar(&trusted, "trusted", false, "mark the host as trusted (you control root on it)")
	return cmd
}

func createHost(ctx context.Context, p provider.Provider, req provider.Request) error {
	return createHostWithOptions(ctx, p, req, "", false)
}

func createHostWithOptions(ctx context.Context, p provider.Provider, req provider.Request, floor string, trusted bool) error {
	if err := onboard.CheckCreate(p, req); err != nil {
		return err
	}
	unlock, err := client.LockHost(ctx, req.Name, "host create")
	if err != nil {
		return err
	}
	defer unlock()
	fmt.Fprintf(os.Stderr, "Creating %s with %s…\n", req.Name, p.Name())
	start := time.Now()
	h, err := onboard.Create(ctx, p, req)
	if err != nil {
		return err
	}
	h.WallFloor = floor
	h.Trusted = trusted
	fmt.Fprintf(os.Stderr, "  ✓ created %s %s in %s\n\n", h.Machine.Provider, h.Machine.ID, time.Since(start).Round(time.Second))
	// Making the host counts as the yes to its helper, so nothing is asked.
	if err := addHost(ctx, &prompter{ctx: ctx, out: os.Stderr}, h, true); err != nil {
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
	fmt.Fprintf(os.Stderr, "Created %s in %s.\n", h.Name, time.Since(start).Round(time.Second))
	return nil
}

// saveHost adds h to hosts.toml, or replaces the host of the same name,
// unless ctx has ended.
func saveHost(ctx context.Context, h client.Host) error { return client.SaveHost(ctx, h) }

// applyIdle installs (or removes) the helper's idle timer on a VM.
func applyIdle(ctx context.Context, h client.Host) error {
	out, err := onboard.SetIdle(ctx, h)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  ✓ idle stop: %s\n", client.CleanLine(out)) // the host's own words
	return nil
}

// machineAction runs host start, stop or status for a host that a provider
// made.
func machineAction(ctx context.Context, h client.Host, action string) error {
	lc, err := client.OpenMachine(h)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		if _, err := client.HostTransport(ctx, h, true); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s is running.\n", h.Name)
	case "stop":
		if err := lc.Stop(ctx); err != nil {
			return err
		}
		(&client.SSH{Host: h}).ExitMaster(ctx)
		fmt.Fprintf(os.Stderr, "%s is stopped. Attaching to one of its sessions starts it again.\n", h.Name)
	default:
		st, err := lc.Status(ctx)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(map[string]string{"state": st, "provider": h.Machine.Provider, "id": h.Machine.ID})
		fmt.Println(string(b))
	}
	return nil
}
