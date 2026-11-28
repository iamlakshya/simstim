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
	"github.com/iamlakshya/simstim/internal/keyread"
	"github.com/iamlakshya/simstim/internal/keys"
)

func doctorCmd() *cobra.Command {
	var fix, yes, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine and its terminal",
		Long: "Checks this computer: ssh, the config files and the secrets' commands. With --fix it\n" +
			"offers to fix what it can: SSH configs it generated for hosts you removed, and\n" +
			"ControlMaster sockets whose master died. Each fix is shown before it runs.\n" +
			"`host doctor <host>` checks a host.",
		Example: "  " + brand.Name + " doctor\n  " + brand.Name + " doctor --fix\n  " + brand.Name + " doctor --json",
		Args:    unknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON && fix && !yes {
				// With --json nothing is asked, so -y must answer for each fix.
				return prompts(cmd).needs(qFix, "a yes for each fix")
			}
			checks, fixes := client.LocalChecks(), client.LocalFixes(false, nil)
			if asJSON && fix {
				res, err := applyAll(fixes, func(ids []string) ([]client.Fix, error) {
					return applyLocal(cmd.Context(), ids)
				})
				if err != nil {
					return err
				}
				fixes = res
			}
			if asJSON {
				if fixes == nil {
					fixes = []client.Fix{}
				}
				return printJSON(map[string]any{"checks": checks, "fixes": fixes})
			}
			printf("This computer:\n")
			printChecks(os.Stdout, os.Stderr, checks)
			if !fix {
				for _, f := range fixes {
					printf("  ✕ %s\n", f.Problem)
				}
				if len(fixes) > 0 {
					say(cmd, "\nTry: %s doctor --fix\n", brand.Name)
				}
				return nil
			}
			return offerFixes(prompts(cmd), qFix, fixes, yes, func(ids []string) ([]client.Fix, error) {
				return applyLocal(cmd.Context(), ids)
			})
		},
	}
	addJSON(cmd, &asJSON)
	addFix(cmd, &fix, "offer to fix the problems found, one by one")
	addYes(cmd, &yes, "with --fix, apply every fix without asking")
	cmd.AddCommand(&cobra.Command{
		Use:     "terminal",
		Short:   "Ask this terminal what it supports: keys, colours and more",
		Example: "  " + brand.Name + " doctor terminal",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return terminalProbe(cmd.Context()) },
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "keys",
		Short: "Show each key as the terminal sends it, and the client's own keys",
		Long: "Turns on the kitty keyboard protocol, then prints each key press as bytes.\n" +
			"To check the path to a host, start a session that runs the same tester there:\n" +
			"  " + brand.Name + " new <host> -- " + brand.HostBinary + " keytest",
		Example: "  " + brand.Name + " doctor keys",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return keyTester(cmd.Context()) },
	})
	return cmd
}

// keyTester prints each key as the terminal sends it, until q twice, or
// until ctx ends: a signal ends its wait for a key, whether or not the read
// can be cancelled, and its defers put the terminal back.
func keyTester(ctx context.Context) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return &client.UserError{Msg: "the key tester needs a terminal", Hint: "Run it from a terminal.", Code: "no_terminal", Kind: client.KindUsage}
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
	if err := keyread.CheckStuck(os.Stdin); err != nil {
		return terminalReadError(err)
	}
	original, err := term.GetState(fd)
	if err != nil {
		return fmt.Errorf("read the terminal state: %w", err)
	}
	// A Windows reader can change console modes in its factory. Restore the
	// state from before that factory, after the reader's own cleanup.
	defer func() { _ = term.Restore(fd, original) }()
	in, done, err := inputOf(os.Stdin)
	if err != nil {
		return err
	}
	if _, err := term.MakeRaw(fd); err != nil {
		done()
		return fmt.Errorf("put the terminal in raw mode: %w", err)
	}
	// Kitty keyboard flags 1|2|4|8|16: report everything, with event types.
	fmt.Print("\x1b[>31u\x1b[?2004h")
	defer fmt.Print("\x1b[<u\x1b[?2004l")
	defer done()
	fmt.Printf("Press keys. %s owns %s (detach) and %s (image paste). Press q twice to quit.\r\n\r\n", brand.DisplayName, dk, pk)
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
