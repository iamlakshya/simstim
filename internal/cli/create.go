package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/docker"
	"github.com/iamlakshya/simstim/internal/provider/e2b"
	"github.com/iamlakshya/simstim/internal/provider/lima"
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
	memory := sizeValue{bytes: lima.DefaultMemory, text: "4G", old: "host create --memory"}
	disk := sizeValue{bytes: lima.DefaultDisk, text: "40G", old: "host create --disk"}
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
			req.Name = p.Name()
			if len(args) == 2 {
				req.Name = args[1]
			}
			req.Out = os.Stderr
			req.Memory, req.Disk = memory.bytes, disk.bytes
			return createHost(cmd.Context(), p, req)
		},
	}
	f := cmd.Flags()
	addCPUs(cmd, &req.CPUs, lima.DefaultCPUs, "lima: CPUs")
	addMemory(cmd, &memory, "lima: memory, a `size` such as 8G")
	addSize(f, &disk, "disk", "lima: disk, a `size` such as 80G (sparse: it grows as it fills)")
	f.StringVar(&req.Image, "image", docker.DefaultImage, "docker: the image, which needs sh, bash and coreutils")
	f.StringVar(&req.Template, "template", e2b.DefaultTemplate, "e2b: the template, which sets CPUs and memory")
	addTokenCommand(cmd, &req.TokenCommand, "e2b: a command that prints the API key, if E2B_API_KEY is not set and the E2B CLI is not logged in")
	addNever(f, &req.IdleStop, "idle-stop", "stop after this `duration` with no client and no working session, or never (lima: 30m, e2b: 15m)")
	return cmd
}

func createHost(ctx context.Context, p provider.Provider, req provider.Request) error {
	if err := onboard.CheckCreate(p, req); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Creating %s with %s…\n", req.Name, p.Name())
	start := time.Now()
	h, err := onboard.Create(ctx, p, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  ✓ created %s %s in %s\n\n", h.Machine.Provider, h.Machine.ID, time.Since(start).Round(time.Second))
	// Making the host counts as the yes to its helper, so nothing is asked.
	if err := addHost(ctx, &prompter{ctx: ctx, out: os.Stderr}, h, true); err != nil {
		return &client.UserError{
			Msg:  fmt.Sprintf("%s was created, but setting it up failed: %v", h.Name, err),
			Hint: brand.Name + " host doctor " + h.Name + ", or " + brand.Name + " host destroy " + h.Name,
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

func hostDestroyCmd() *cobra.Command {
	var force, yes bool
	cmd := &cobra.Command{
		Use:   "destroy <host>",
		Short: "Delete a created host: its machine and every session on it",
		Long: "Delete the machine behind a host, then forget the host. It asks first, and refuses while a\n" +
			"session is working unless you pass --force.",
		Example: "  " + brand.Name + " host destroy lima",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return destroyHost(cmd.Context(), prompts(cmd), args[0], force, yes)
		},
	}
	addForce(cmd, &force, "destroy it even while a session is working")
	addYes(cmd, &yes, "delete the machine")
	return cmd
}

// destroyHost deletes the machine behind the host name, after a yes to p,
// and forgets the host.
func destroyHost(ctx context.Context, p *prompter, name string, force, yes bool) error {
	h, err := client.FindHost(name)
	if err != nil {
		return err
	}
	if h.Machine == nil {
		return &client.UserError{Msg: name + " was not made by `host create`, so there is nothing to destroy", Hint: brand.Name + " host rm " + name + " (forgets it)", Code: "not_created"}
	}
	lc, err := client.OpenMachine(h)
	if err != nil {
		return err
	}
	if !force {
		if working := workingSessions(ctx, h, lc); len(working) > 0 {
			return &client.UserError{
				Msg:  fmt.Sprintf("%d session(s) on %s are working: %s", len(working), name, strings.Join(working, ", ")),
				Hint: brand.Name + " host destroy " + name + " --force (stops them too)",
				Code: "sessions_working",
			}
		}
	}
	what := fmt.Sprintf("the %s machine %s", h.Machine.Provider, h.Machine.ID)
	if !yes {
		ok, err := p.confirm(qDestroy, fmt.Sprintf("Delete %s, with its disk and every session on it?", what), "a yes to delete "+what)
		if err != nil {
			return err
		}
		if !ok {
			return p.cancel()
		}
		if stopped(ctx) {
			// A signal that came with the yes: delete nothing.
			return errInterrupted
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := lc.Destroy(dctx); err != nil {
		return err
	}
	(&client.SSH{Host: h}).ExitMaster(ctx)
	if err := client.ForgetHost(ctx, name); err != nil {
		return err
	}
	fmt.Fprintf(p.out, "Deleted %s and forgot %s.\n", what, name)
	return nil
}

// workingSessions names the sessions working on h. A host that is not
// running has none.
func workingSessions(ctx context.Context, h client.Host, lc client.Lifecycle) []string {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if st, err := lc.Status(sctx); err != nil || st != client.MachineRunning {
		return nil
	}
	conn, err := client.Dial(client.WithoutWake(sctx), h, true)
	if err != nil {
		return nil
	}
	defer conn.Close()
	sessions, err := conn.Sessions(sctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range sessions {
		if s.Alive && (s.State == api.StateWorking || s.State == api.StateStarting) {
			out = append(out, client.CleanLine(s.Name)) // the host's text, only printed
		}
	}
	sort.Strings(out)
	return out
}

func hostIdleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "idle <host> <duration>|never",
		Short: "Set how long a created host may idle before it stops",
		Example: "  " + brand.Name + " host idle lima 30m" + "\n" +
			"  " + brand.Name + " host idle lima never",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			if h.Machine == nil {
				return &client.UserError{Msg: h.Name + " was not made by `host create`", Hint: "Use a host made by " + brand.Name + " host create.", Code: "not_created"}
			}
			idle, err := parseIdle(args[1])
			if err != nil {
				return &client.UserError{Msg: fmt.Sprintf("%q is not an idle time", args[1]), Hint: "Use a duration of a minute or more, such as 30m or 2h, or never.", Code: "bad_value", Kind: client.KindUsage}
			}
			h.Machine.IdleStop = idle
			if err := saveHost(cmd.Context(), h); err != nil {
				return err
			}
			switch {
			case powersOffWhenIdle(h.Machine.Provider):
				return applyIdle(cmd.Context(), h)
			case h.Machine.Provider == "e2b":
				say(cmd, "%s pauses %s after the last client goes, while no session works.\n", h.Name, h.Machine.IdleStop)
			default:
				say(cmd, "Saved, but %s hosts do not stop by themselves.\n", h.Machine.Provider)
			}
			return nil
		},
	}
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
