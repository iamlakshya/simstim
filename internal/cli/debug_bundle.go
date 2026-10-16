package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/bundle"
	"github.com/iamlakshya/simstim/internal/client"
)

// bundleWait is how long a bundle may take: the fresh profiles, and the
// files of every session, over a slow link.
const bundleWait = 10 * time.Minute

func debugBundleCmd() *cobra.Command {
	var ssh bool
	cmd := &cobra.Command{
		Use:   "bundle [<host> | <host>/<session>]",
		Short: "Write one local archive of the debug files, with private values replaced, to attach to an issue",
		Long: "Without an argument, the bundle takes the laptop's side only. With a host, it adds every session\n" +
			"on that host that has debug files, and with a session, that session only. It writes\n" +
			brand.Name + "-debug-<time>.tar.gz in the current folder, with mode 0600. It replaces host names,\n" +
			"addresses, user names, home folders, session names and folders, email addresses, SSH keys and\n" +
			"the values of the host's alerts file. Nothing is uploaded. Read the files before you share them.",
		Example: "  " + brand.Name + " debug bundle my-vps/auth-fix\n" +
			"  " + brand.Name + " debug bundle my-vps --ssh",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runBundle(cmd.Context(), args, ssh)
			if err != nil {
				return err
			}
			printBundle(os.Stdout, res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&ssh, "ssh", false, "add the ssh -v log of one new connection to the host")
	return cmd
}

// runBundle writes the bundle of the laptop, a host or a session into the
// current folder.
func runBundle(ctx context.Context, args []string, ssh bool) (*bundle.Result, error) {
	o := bundle.Options{SSH: ssh, Interactive: true}
	if len(args) == 1 {
		hn, ref, slash := strings.Cut(args[0], "/")
		if hn == "" || slash && ref == "" {
			return nil, &client.UserError{Msg: fmt.Sprintf("%q is not a host or host/session", args[0]), Hint: brand.Name + " ls"}
		}
		h, err := client.FindHost(hn)
		if err != nil {
			return nil, err
		}
		o.Host, o.Session = &h, ref
	} else if ssh {
		return nil, &client.UserError{Msg: "--ssh needs a host", Hint: brand.Name + " debug bundle <host> --ssh"}
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("find the current folder: %w", err)
	}
	o.Dir = dir
	ctx, cancel := context.WithTimeout(ctx, bundleWait)
	defer cancel()
	// Ctrl+C stops the bundle, which then removes what it wrote.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	if o.Host != nil {
		fmt.Fprintf(os.Stderr, "Collecting the debug files of %s…\n", args[0])
	}
	return bundle.Write(ctx, o)
}

// printBundle says what the bundle holds: its path and size, each file
// and its size, the counts of what it replaced, and what it left out.
func printBundle(w io.Writer, r *bundle.Result) {
	fmt.Fprintf(w, "Wrote %s (%s), with %d files:\n", tildeHome(r.Path), sizeText(r.Size), len(r.Files))
	width := 0
	for _, f := range r.Files {
		width = max(width, len(f.Name))
	}
	for _, f := range r.Files {
		fmt.Fprintf(w, "  %-*s  %s\n", width, f.Name, sizeText(f.Size))
	}
	if len(r.Counts) == 0 {
		fmt.Fprintln(w, "It replaced no private value that it knows.")
	} else {
		parts := make([]string, 0, len(r.Counts))
		for _, c := range r.Counts {
			parts = append(parts, c.String())
		}
		fmt.Fprintf(w, "In the archive, it replaced %s (replaced.txt).\n", strings.Join(parts, ", "))
	}
	for _, l := range r.Left {
		name := l.Name
		if l.From != "" {
			name += " (" + tildeHome(l.From) + ")"
		}
		fmt.Fprintf(w, "Left out %s: %s.\n", name, strings.TrimSuffix(l.Why, "."))
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "Not gathered: %s.\n", strings.TrimSuffix(n, "."))
	}
	if r.Size > bundle.GitHubMax {
		big := make([]string, 0, 3)
		for _, f := range r.Largest(3) {
			big = append(big, fmt.Sprintf("%s (%s)", f.Name, sizeText(f.Size)))
		}
		fmt.Fprintf(w, "The bundle is %.1f MB, more than the 25 MB that GitHub takes for one file. Its largest files: %s.\n",
			float64(r.Size)/1e6, strings.Join(big, ", "))
		fmt.Fprintf(w, "To make it smaller, bundle one session: %s debug bundle <host>/<session>\n", brand.Name)
	}
	fmt.Fprintln(w, "Nothing was uploaded. Read the files before you attach them to an issue.")
}
