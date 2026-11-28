package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func hostRekeyCmd() *cobra.Command {
	var hostKey, confirm string
	cmd := &cobra.Command{
		Use:   "rekey <host>",
		Short: "Replace a host's pinned SSH host key",
		Long: "Reads the host's current SSH public key without logging in, shows the old and new\n" +
			"fingerprints, and replaces the pinned key after you type the host's name.\n\n" +
			"Use this only when you know why the key changed, for example after reinstalling the host.\n" +
			"Without a terminal, pass --confirm <host> (and --host-key SHA256:… to check the new key).",
		Example: "  " + brand.Name + " host rekey my-vps\n" +
			"  " + brand.Name + " host rekey my-vps --host-key SHA256:… --confirm my-vps",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rekeyHost(cmd.Context(), prompts(cmd), args[0], hostKey, confirm)
		},
	}
	cmd.Flags().StringVar(&hostKey, "host-key", "", "require the new host key to match this SHA256 fingerprint (SHA256:…)")
	addConfirm(cmd, &confirm, "type the host's name here instead of at the prompt")
	return cmd
}

func rekeyHost(ctx context.Context, p *prompter, name, hostKey, confirm string) error {
	h, err := client.FindHost(name)
	if err != nil {
		return err
	}
	if h.SSH == "" {
		return &client.UserError{
			Msg:  fmt.Sprintf("%s is reached through %s, so it has no SSH host key", h.Name, h.Target()),
			Hint: "Only hosts reached over SSH have a pinned host key.",
			Code: "needs_ssh",
		}
	}
	if confirm != "" && confirm != h.Name {
		return &client.UserError{
			Msg:  fmt.Sprintf("--confirm %q does not match the host name %q", confirm, h.Name),
			Hint: fmt.Sprintf("%s host rekey %s --confirm %s", brand.Name, h.Name, h.Name),
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	oldFPs, oldErr := client.PinnedFingerprints(h.Name)
	fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	keys, newFPs, err := client.FetchRekeyKeys(fctx, h, hostKey)
	cancel()
	if err != nil {
		return err
	}
	switch {
	case oldErr == nil && len(oldFPs) > 0:
		fmt.Fprintf(p.out, "Old host key: %s\n", strings.Join(oldFPs, ", "))
	case !client.HasPin(h.Name):
		fmt.Fprintf(p.out, "Old host key: none\n")
	default:
		fmt.Fprintf(p.out, "Old host key: unreadable (%v)\n", oldErr)
	}
	fmt.Fprintf(p.out, "New host key: %s\n", strings.Join(newFPs, ", "))

	if confirm == "" {
		if !p.on {
			return p.needs(qHostRekey, "the typed host name to replace the pinned host key of "+h.Name, "--confirm", h.Name)
		}
		ok, err := p.typedName(fmt.Sprintf("Type %s to replace its pinned host key, or press Enter to keep it:", h.Name), h.Name)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(p.out, "Kept the pinned host key of %s.\n", h.Name)
			return nil
		}
		if stopped(ctx) {
			return errInterrupted
		}
	}
	fps, err := client.ApplyRekey(ctx, h, keys)
	if err != nil {
		return err
	}
	fmt.Fprintf(p.out, "Replaced the pinned host key of %s: %s.\n", h.Name, fps[0])
	return nil
}
