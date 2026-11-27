package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// The debug mode (decision 0025): the global flags --debug and
// --debug-file, and the commands that start and stop the debug of a
// running session, save its recent debug record, and write its profiles.

// addDebugFlags adds the global debug flags to root. startDebug turns on
// the client's debug before any command runs.
func addDebugFlags(root *cobra.Command) {
	f := root.PersistentFlags()
	addList(f, new([]string), "debug", "turn on the debug mode, with every category, or with the `categories` listed: "+
		strings.ReplaceAll(diag.CatList, ",", ", ")+". A list binds only in the = form, such as --debug=net")
	f.Lookup("debug").NoOptDefVal = diag.CatList
	f.String("debug-file", "", "write the client's debug records to this `file`. On its own, it turns on every category ("+diag.EnvFile+" does the same)")
	shared(f, "debug")
	shared(f, "debug-file")
	_ = root.RegisterFlagCompletionFunc("debug", completeCats(diag.AllCats))
}

// isLauncher reports whether cmd opens the TUI launcher or switcher.
func isLauncher(cmd *cobra.Command, args []string) bool {
	return !cmd.HasParent() || cmd.Name() == "flip" && len(args) == 0
}

// completeCats completes a list of categories from cats, separated by
// commas: the ones that the list does not name yet, after its last comma.
func completeCats(cats diag.Cats) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		prefix := toComplete[:strings.LastIndex(toComplete, ",")+1]
		have := diag.CatsOf(strings.Split(prefix, ","))
		var out []string
		for _, c := range cats.List() {
			if cand := prefix + c; !have.Has(c) && strings.HasPrefix(cand, toComplete) {
				out = append(out, cand)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
}

// startDebug turns on the client's debug when --debug, --debug-file or
// the variable asks for it, and says so on stderr, before an attach can
// take the terminal. The launcher says so in its header instead. --debug-file
// wins over the variable.
func startDebug(cmd *cobra.Command, args []string) error {
	if cmd.Name() == cobra.ShellCompRequestCmd || cmd.Name() == cobra.ShellCompNoDescRequestCmd {
		return nil // the shell's completion of a word: nothing to record, and nothing to print
	}
	flags := cmd.Flags()
	val := flags.Lookup("debug").Value.String()
	file, _ := flags.GetString("debug-file")
	set := flags.Changed("debug")
	if !flags.Changed("debug-file") {
		file = os.Getenv(diag.EnvFile)
	}
	if !set && file == "" {
		return nil
	}
	cats := diag.AllCats
	if set {
		c, err := diag.ParseCats(val)
		if err != nil {
			return &client.UserError{Msg: "--debug: " + err.Error(),
				Hint: "Pass --debug alone for every category, or a list in the = form, such as --debug=attach,net.", Code: "bad_value", Kind: client.KindUsage}
		}
		cats = c
	}
	path, err := client.StartDebug(client.DebugOptions{Cats: cats, File: file})
	if err != nil {
		return err
	}
	if !isLauncher(cmd, args) {
		note(cmd, "debug_on", "debug on: "+tildeHome(path))
	}
	return nil
}

// tildeHome writes a path under the home folder from ~.
func tildeHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
			return "~/" + filepath.ToSlash(rest)
		}
	}
	return p
}

func debugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Record why a session goes wrong: debug files, profiles and traces",
		Long: "The debug mode records what the client and the host helper do, into files on each side.\n" +
			"Nothing listens and nothing is uploaded. --debug turns it on for one command. These commands work\n" +
			"on a running session, from any terminal, with no restart. See docs/how-to/debug-a-session.md.",
		Example: "  " + brand.Name + " debug start my-vps/auth-fix\n" +
			"  " + brand.Name + " debug bundle my-vps/auth-fix",
	}
	cmd.AddCommand(debugStartCmd(), debugStopCmd(), debugSaveCmd(), debugProfileCmd(), debugBundleCmd())
	return cmd
}

func debugStartCmd() *cobra.Command {
	var d time.Duration
	var only []string
	cmd := &cobra.Command{
		Use:   "start <host>/<session>",
		Short: "Turn on the debug of a running session, for 30 minutes or --for",
		Long: "The session's process on the host records the host's categories, attach, memory and runtime,\n" +
			"for every attached client, until debug stop or the time limit. A second debug start sets a new end.",
		Example: "  " + brand.Name + " debug start my-vps/auth-fix\n" +
			"  " + brand.Name + " debug start my-vps/auth-fix --for 8h --only memory",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if d < time.Second || d > api.DebugForMax {
				return &client.UserError{Msg: "--for: " + d.String() + " is not between 1s and 24h", Hint: "Pass a time such as 30m or 8h.", Code: "bad_value", Kind: client.KindUsage}
			}
			cats, err := hostCats(only, listTyped(cmd.Flags(), "only"))
			if err != nil {
				return err
			}
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				st, err := conn.DebugStart(ctx, s.ID, cats, d)
				if err != nil {
					return err
				}
				until := "the end of its time"
				if st.Until != nil {
					until = clockTime(*st.Until)
				}
				fmt.Fprintf(os.Stderr, "Debug is on for %s on %s until %s (%s).\n", shownShort(nameOf(*s)), shownShort(h.Name), until, shownShort(strings.Join(st.Categories, ", ")))
				fmt.Fprintf(os.Stderr, "The host writes its records to %s.\n", shown(st.File))
				fmt.Fprintf(os.Stderr, "To trace your keys too, attach with --debug. To stop: %s\n", sessionCommand(h.Name, s.ID, "debug stop"))
				return nil
			})
		},
	}
	addFor(cmd, &d, api.DebugFor, "how long debug stays on, up to 24h")
	addList(cmd.Flags(), &only, "only", "record only these host `categories`: attach, memory or runtime (default: all three)")
	_ = cmd.RegisterFlagCompletionFunc("only", completeCats(diag.HostCats))
	return cmd
}

// hostCats reads --only: categories of the host only. When --only is not
// set, or was set to nothing, as "", it returns every one of them, as
// before. A list of no names, such as ",", is an error.
func hostCats(only []string, set bool) (diag.Cats, error) {
	if !set {
		return diag.HostCats, nil
	}
	cats, err := diag.ParseCats(strings.Join(only, ","))
	if err != nil {
		return 0, &client.UserError{Msg: "--only: " + err.Error(), Hint: "Name attach, memory or runtime.", Code: "bad_value", Kind: client.KindUsage}
	}
	if other := cats &^ diag.HostCats; other != 0 {
		return 0, &client.UserError{Msg: "--only: " + other.String() + " is not a category of the host",
			Hint: "Name attach, memory or runtime, and record net and launcher on this computer with --debug.", Code: "bad_value", Kind: client.KindUsage}
	}
	return cats, nil
}

// clockTime is t as a local time of day, with the date when it is not
// today.
func clockTime(t time.Time) string {
	t = t.Local()
	if y, m, d := t.Date(); y == time.Now().Year() && m == time.Now().Month() && d == time.Now().Day() {
		return t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

func debugStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <host>/<session>",
		Short:             "Turn off the debug of a session at once",
		Long:              "Turns off the debug of a session at once, and the host side of each traced attach.",
		Example:           "  " + brand.Name + " debug stop my-vps/auth-fix",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				st, err := conn.DebugStop(ctx, s.ID)
				if err != nil {
					return err
				}
				if !st.Stopped {
					fmt.Fprintf(os.Stderr, "Debug was not on for %s on %s.\n", shown(nameOf(*s)), shown(h.Name))
					return nil
				}
				fmt.Fprintf(os.Stderr, "Debug is off for %s on %s. Its records are in %s on the host.\n", shown(nameOf(*s)), shown(h.Name), shown(st.File))
				return nil
			})
		},
	}
}

func debugSaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save <host>/<session>",
		Short: "Save the session's recent debug record, just after a lag",
		Long: "While the session's runtime category is on, the session's process on the host keeps the newest\n" +
			"10 s or so of Go's execution trace. This saves that window to a file on the host, for go tool trace.",
		Example:           "  " + brand.Name + " debug save my-vps/auth-fix",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				f, err := conn.DebugSave(ctx, s.ID)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "Saved the recent debug record of %s: %s on %s (%s).\n", shown(nameOf(*s)), shown(f.Path), shown(h.Name), sizeText(f.Size))
				return nil
			})
		},
	}
}

func debugProfileCmd() *cobra.Command {
	var d time.Duration
	cmd := &cobra.Command{
		Use:   "profile <host>/<session> cpu|heap|goroutine",
		Short: "Write a profile of the session's process, for go tool pprof",
		Example: "  " + brand.Name + " debug profile my-vps/auth-fix cpu --for 1m\n" +
			"  " + brand.Name + " debug profile my-vps/auth-fix heap",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := args[1]
			switch kind {
			case diag.ProfileCPU, diag.ProfileHeap, diag.ProfileGoroutine:
			default:
				return &client.UserError{Msg: "no profile " + kind, Hint: "Name cpu, heap or goroutine.", Code: "bad_value", Kind: client.KindUsage}
			}
			if kind != diag.ProfileCPU && cmd.Flags().Changed("for") {
				return &client.UserError{Msg: "--for is for a cpu profile only: a " + kind + " profile is taken at once",
					Hint: "Leave out --for, or name cpu.", Code: "flags_conflict", Kind: client.KindUsage}
			}
			if d < time.Second || d > diag.ProfileMax {
				return &client.UserError{Msg: "--for: " + d.String() + " is not between 1s and 5m", Hint: "Pass a time such as 30s or 2m.", Code: "bad_value", Kind: client.KindUsage}
			}
			return withSession(cmd, args[0], func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
				wait := 30 * time.Second
				if kind == diag.ProfileCPU {
					wait += d
					defer countDown("Profiling the CPU of "+shown(nameOf(*s)), d)()
				}
				// A CPU profile may take longer than the connection's own time.
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
				defer cancel()
				f, err := conn.DebugProfile(ctx, s.ID, kind, d)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "Wrote a %s profile of %s: %s on %s (%s).\n", kind, shown(nameOf(*s)), shown(f.Path), shown(h.Name), sizeText(f.Size))
				return nil
			})
		},
	}
	addFor(cmd, &d, diag.ProfileDefault, "cpu only: how long the profile runs, up to 5m")
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 0:
			return completeSessions(cmd, args, toComplete)
		case 1:
			return completeFixed(diag.ProfileCPU, diag.ProfileHeap, diag.ProfileGoroutine)(cmd, args, toComplete)
		default:
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}
	return cmd
}

// countDown shows the time left on stderr while the client waits, and
// returns the function that clears it. Without a terminal, it says the
// time once.
func countDown(what string, d time.Duration) func() {
	if !isTerminal(os.Stderr) {
		fmt.Fprintf(os.Stderr, "%s for %s.\n", what, d)
		return func() {}
	}
	end := time.Now().Add(d)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			left := max(time.Until(end).Round(time.Second), 0)
			fmt.Fprintf(os.Stderr, "\r\x1b[K%s: %s left", what, left)
			select {
			case <-stop:
				fmt.Fprint(os.Stderr, "\r\x1b[K")
				return
			case <-t.C:
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// sizeText is a file's size for people, such as 8.1 MiB.
func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
