package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/review"
)

func cpCmd() *cobra.Command {
	var force, allowFlagged, recursive bool
	cmd := &cobra.Command{
		Use:   "cp <source> <destination>",
		Short: "Copy files or folders into or out of a session over SFTP",
		Long: "Copies a file or folder between this machine and a session's workspace over SFTP.\n" +
			"Name the remote side as <host>/<session>:<path>. A relative path starts in the session's workspace.\n" +
			"Writes on this machine stay inside os.Root without following symbolic links, and files that can run code are flagged for review.",
		Example: "  " + brand.Name + " cp my-vps/auth-fix:notes.md .\n" +
			"  " + brand.Name + " cp ./data.csv my-vps/auth-fix:data.csv",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			srcHost, srcSess, srcPath, srcRemote := parseCPSpec(args[0])
			dstHost, dstSess, dstPath, dstRemote := parseCPSpec(args[1])
			if srcRemote == dstRemote {
				return &client.UserError{
					Msg:  "one side of cp must be <host>/<session>:<path> and the other a local path",
					Hint: brand.Name + " cp my-vps/auth-fix:notes.md .",
					Code: "bad_address",
					Kind: client.KindUsage,
				}
			}
			p := prompts(cmd)
			confirm := func(res review.Result) (bool, error) {
				if !p.on {
					return false, nil
				}
				fmt.Fprintf(os.Stderr, "Flagged files: %s\n", client.CleanLine(res.Summary()))
				return p.yesNo("Copy the listed files that can run code or contain hidden text?", false)
			}
			opt := client.CopyOptions{
				Force:          force,
				AllowFlagged:   allowFlagged,
				ConfirmFlagged: confirm,
			}
			if srcRemote {
				return runCP(cmd, srcHost+"/"+srcSess, func(ctx context.Context, h client.Host, s *client.Session) error {
					sc, closer, err := client.OpenSessionSFTP(ctx, h, s)
					if err != nil {
						return err
					}
					defer closer.Close()
					res, err := client.CopyFromSFTP(sc, srcPath, args[1], opt)
					if err != nil {
						return err
					}
					reportCP(cmd, "Copied", res, fmt.Sprintf("from %s on %s", client.CleanLine(s.Name), h.Name))
					return nil
				})
			}
			return runCP(cmd, dstHost+"/"+dstSess, func(ctx context.Context, h client.Host, s *client.Session) error {
				sc, closer, err := client.OpenSessionSFTP(ctx, h, s)
				if err != nil {
					return err
				}
				defer closer.Close()
				res, err := client.CopyToSFTP(sc, args[0], dstPath, opt)
				if err != nil {
					return err
				}
				reportCP(cmd, "Copied", res, fmt.Sprintf("to %s on %s", client.CleanLine(s.Name), h.Name))
				return nil
			})
		},
	}
	addForceShort(cmd, &force, "replace existing files at the destination")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "copy folders recursively (already the default)")
	cmd.Flags().BoolVar(&allowFlagged, "allow-flagged", false, "copy files that can run code or contain hidden text without asking")
	return cmd
}

func runCP(cmd *cobra.Command, target string, fn func(context.Context, client.Host, *client.Session) error) error {
	return withSession(cmd, target, func(ctx context.Context, h client.Host, conn *client.Conn, s *client.Session) error {
		if !conn.HasFeature(api.FeatureSFTP) {
			return &client.UserError{
				Msg:  "the helper on " + h.Name + " is too old for SFTP file copy",
				Hint: brand.Name + " host add " + h.Name + " (updates the helper)",
				Code: "helper_old",
			}
		}
		return fn(ctx, h, s)
	})
}

// parseCPSpec checks whether arg has the remote form <host>/<session>:<path>.
// A local path starting with ./, ../, /, or ~ never counts as remote.
func parseCPSpec(arg string) (host, session, remotePath string, ok bool) {
	if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") || strings.HasPrefix(arg, "~") {
		return "", "", "", false
	}
	left, right, hasColon := strings.Cut(arg, ":")
	if !hasColon || left == "" {
		return "", "", "", false
	}
	h, s, hasSlash := strings.Cut(left, "/")
	if !hasSlash || h == "" || s == "" || strings.ContainsRune(s, '/') {
		return "", "", "", false
	}
	return h, s, right, true
}

func reportCP(cmd *cobra.Command, verb string, res client.CopyResult, where string) {
	unit := "files"
	if res.Files == 1 {
		unit = "file"
	}
	say(cmd, "%s %d %s (%d bytes) %s.\n", verb, res.Files, unit, res.Bytes, where)
	for _, sk := range res.Skipped {
		say(cmd, "Skipped %s.\n", client.CleanLine(sk))
	}
}
