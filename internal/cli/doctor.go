package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keys"
)

func doctorCmd() *cobra.Command {
	var fix, yes bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine and its terminal",
		Long: "Checks this computer: ssh, the config files and the secrets' commands. With --fix it\n" +
			"offers to fix what it can: SSH configs it generated for hosts you removed, and\n" +
			"ControlMaster sockets whose master died. Each fix is shown before it runs.\n" +
			"`host doctor <name>` checks a host.",
		Args: unknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Println("This computer:")
			printChecks(os.Stdout, client.LocalChecks())
			fixes := client.LocalFixes(false, nil)
			if !fix {
				for _, f := range fixes {
					fmt.Printf("  ✕ %s\n", f.Problem)
				}
				if len(fixes) > 0 {
					fmt.Printf("\nTry: %s doctor --fix\n", brand.Name)
				}
				return nil
			}
			return offerFixes(cmd.Context(), os.Stdin, os.Stdout, fixes, yes, func(ids []string) ([]client.Fix, error) {
				return client.LocalFixes(true, ids), nil
			})
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "offer to fix the problems found, one by one")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "with --fix, apply every fix without asking")
	cmd.AddCommand(&cobra.Command{
		Use:   "terminal",
		Short: "Ask this terminal what it supports (keys, colours, synchronized output…)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return terminalProbe(cmd.Context()) },
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "keys",
		Short: "Show every key as the terminal sends it, and which keys " + brand.DisplayName + " keeps",
		Long: "Turns on the kitty keyboard protocol, then prints each key press as bytes.\n" +
			"To check the path to a host, start a session that runs the same tester there:\n" +
			"  " + brand.Name + " new <host> -- " + brand.HostBinary + " keytest",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return keyTester(cmd.Context()) },
	})
	return cmd
}

// keyTester prints each key as the terminal sends it, until q twice, or
// until ctx ends: a signal ends its wait for a key, whether or not the read
// can be cancelled, and its defers put the terminal back.
func keyTester(ctx context.Context) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return &client.UserError{Msg: "the key tester needs a terminal", Hint: "Run it from a terminal.", Code: "no_terminal"}
	}
	s := client.LoadSettings()
	dk, err := keys.Parse(s.Attach.DetachKey)
	if err != nil {
		return &client.UserError{Msg: "bad detach key: " + err.Error(), Hint: "Fix attach.detach_key in the config.", Code: "bad_config", Kind: client.KindUsage}
	}
	pk, err := keys.Parse(s.Attach.ImagePasteKey)
	if err != nil {
		return &client.UserError{Msg: "bad image paste key: " + err.Error(), Hint: "Fix attach.image_paste_key in the config.", Code: "bad_config", Kind: client.KindUsage}
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("put the terminal in raw mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, old) }()
	// Kitty keyboard flags 1|2|4|8|16: report everything, with event types.
	fmt.Print("\x1b[>31u\x1b[?2004h")
	defer fmt.Print("\x1b[<u\x1b[?2004l")
	fmt.Printf("Press keys. %s owns %s (detach) and %s (image paste). Press q twice to quit.\r\n\r\n", brand.DisplayName, dk, pk)
	in, done := inputOf(os.Stdin)
	defer done()
	sctx, cancel := stopping(ctx)
	defer cancel()
	f := keys.New(dk, pk)
	qs := 0
	err = keys.ReadUntil(sctx, in, func(b []byte) bool {
		for _, p := range f.Feed(b) {
			label := ""
			switch p.Kind {
			case keys.Match:
				label = []string{"  ← detach key", "  ← image paste key"}[p.Index]
				if p.Release {
					label += " (release)"
				}
			case keys.Paste:
				label = "  ← a paste"
			default:
				// Bytes that pass through get no label.
			}
			fmt.Printf("%s%s\r\n", strconv.Quote(string(p.Raw)), label)
			if string(p.Raw) == "q" || string(p.Raw) == "\x1b[113u" {
				qs++
				if qs >= 2 {
					return false
				}
			} else if p.Kind != keys.Match || !p.Release {
				qs = 0
			}
		}
		return true
	})
	if err != nil {
		return fmt.Errorf("read the keys: %w", err)
	}
	return nil
}
