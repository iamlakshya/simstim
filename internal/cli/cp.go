package cli

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/filecopy"
)

var qCopyFlagged = &question{cmd: "cp", flag: "--allow-flagged"}

func init() { questions = append(questions, qCopyFlagged) }

func cpCmd() *cobra.Command {
	var force, allowFlagged, asJSON bool
	cmd := &cobra.Command{
		Use: "cp <source> <destination>", Short: "Copy files between this computer and a running sandbox",
		Long: "Copies an ordinary file or folder between this computer and a running sandbox.\n" +
			"Use host/session:/path or host/session:~/path for the sandbox side.\n" +
			"Review files that affect code execution before using --allow-flagged.",
		Example: "  " + brand.Name + " cp box/work:~/notes.md .\n  " + brand.Name + " cp ./data box/work:/work/project/data",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, fromPath, fromRemote, err := copyAddress(args[0])
			if err != nil {
				return err
			}
			to, toPath, toRemote, err := copyAddress(args[1])
			if err != nil {
				return err
			}
			if fromRemote == toRemote {
				return &client.UserError{Code: "cp_pair", Msg: "a copy needs one sandbox address and one local path", Hint: "Use cp HOST/SESSION:/path LOCAL or cp LOCAL HOST/SESSION:/path.", Kind: client.KindUsage}
			}
			if toRemote && allowFlagged {
				return &client.UserError{Code: "flags_conflict", Msg: "--allow-flagged applies only to files copied out of a sandbox", Hint: "Remove --allow-flagged for an incoming copy.", Kind: client.KindUsage}
			}
			selected := from
			if toRemote {
				selected = to
			}
			address, err := sessionArg(cmd, []string{selected}, false)
			if err != nil {
				return err
			}
			original := cmd.Context()
			defer cmd.SetContext(original)
			ctx := client.WithoutWake(original)
			if fromRemote {
				ctx = client.WithGrantRequest(ctx, client.NewReviewGrantRequest())
			} else {
				ctx = client.WithGrantRequest(ctx, client.NewGrantRequest(false, false))
			}
			cmd.SetContext(ctx)
			return address.open(ctx, cmd, func(ctx context.Context, found found) (returned error) {
				if found.s.Wall == nil || found.s.Wall.Level == "none" {
					return &client.UserError{Code: "cp_unwalled", Msg: "the selected session runs without a wall", Hint: "Use scp for that host's own files.", Kind: client.KindRefused}
				}
				options := client.CopyOptions{Force: force, AllowFlagged: allowFlagged, Terminal: secretTerminal(cmd)}
				var result client.CopyResult
				if fromRemote {
					var op *client.CopyOut
					for {
						op, err = found.conn.PrepareCopyOut(cmd.Context(), found.s.ID, fromPath, args[1], options)
						if err == nil {
							break
						}
						var question *client.ConfirmError
						if !errors.As(err, &question) {
							return err
						}
						if answerErr := answerGrant(cmd, err, question); answerErr != nil {
							return answerErr
						}
					}
					defer func() { returned = errors.Join(returned, op.Close()) }()
					preview := op.Preview()
					if !asJSON {
						showCopyReview(cmd, preview)
					}
					for {
						err = op.Download(cmd.Context())
						var review *client.CopyReviewError
						if !errors.As(err, &review) {
							if err != nil {
								return err
							}
							break
						}
						if !prompts(cmd).on {
							return err
						}
						for _, finding := range review.Result.Findings {
							if finding.Refused {
								return err
							}
						}
						yes, askErr := prompts(cmd).confirm(qCopyFlagged, "Copy these reviewed files?", "a yes after reviewing the listed files")
						if askErr != nil {
							return askErr
						}
						if !yes {
							return prompts(cmd).cancel()
						}
						op.AllowFlagged()
					}
					for {
						result, err = op.Publish(cmd.Context())
						if err == nil {
							break
						}
						var grant *client.ConfirmError
						if errors.As(err, &grant) {
							if answerErr := answerGrant(cmd, err, grant); answerErr != nil {
								return answerErr
							}
							continue
						}
						var review *client.CopyReviewError
						if !errors.As(err, &review) || !prompts(cmd).on {
							return err
						}
						if !asJSON {
							showCopyReview(cmd, review.Result)
						}
						for _, finding := range review.Result.Findings {
							if finding.Refused {
								return err
							}
						}
						yes, askErr := prompts(cmd).confirm(qCopyFlagged, "Copy these reviewed files?", "a yes after reviewing the listed files")
						if askErr != nil {
							return askErr
						}
						if !yes {
							return prompts(cmd).cancel()
						}
						op.AllowFlagged()
					}
				} else {
					op, err := found.conn.PrepareCopyIn(ctx, found.s.ID, args[0], toPath, options)
					if err != nil {
						return err
					}
					defer func() { returned = errors.Join(returned, op.Close()) }()
					result, err = op.Publish(cmd.Context())
					if err != nil {
						return err
					}
				}
				if asJSON {
					return printJSON(result)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Copied %d files and %d folders (%d bytes).\n", result.Files, result.Directories, result.Bytes)
				return nil
			})
		},
	}
	addForce(cmd, &force, "replace existing copy destinations")
	cmd.Flags().Lookup("force").Shorthand = "f"
	cmd.Flags().BoolVar(&allowFlagged, "allow-flagged", false, "copy reviewed files that can run code or contain session data")
	addJSON(cmd, &asJSON)
	return cmd
}

func copyAddress(value string) (address, guest string, remote bool, err error) {
	colon := strings.IndexByte(value, ':')
	if colon < 0 {
		return "", "", false, nil
	}
	if colon == 1 && len(value) > 2 && (value[2] == '\\' || value[2] == '/') {
		return "", "", false, nil
	}
	prefix := value[:colon]
	if strings.HasPrefix(prefix, ".") || strings.HasPrefix(prefix, "/") || strings.Count(prefix, "/") != 1 {
		return "", "", false, nil
	}
	host, session, _ := strings.Cut(prefix, "/")
	if !client.ValidHostName(host) || session == "" {
		return "", "", false, nil
	}
	selected := value[colon+1:]
	trailing := strings.HasSuffix(selected, "/")
	if selected == "~/" {
		selected = "/home/agent"
	} else if len(selected) > 1 {
		selected = strings.TrimSuffix(selected, "/")
	}
	top, relative, err := filecopy.GuestPath(selected)
	if err != nil {
		var core *filecopy.Error
		if errors.As(err, &core) {
			err = &client.UserError{Code: core.Code, Msg: core.Message, Hint: core.Hint, Kind: client.KindRefused}
		}
		return "", "", false, err
	}
	guest = pathpkg.Join(top, relative)
	if trailing {
		guest += "/"
	}
	return prefix, guest, true, nil
}

func showCopyReview(cmd *cobra.Command, result client.CopyResult) {
	for _, name := range result.Skipped {
		fmt.Fprintf(cmd.ErrOrStderr(), "Skipped link or special file: %s\n", client.CleanLine(name))
	}
	for _, finding := range result.Findings {
		fmt.Fprintf(cmd.ErrOrStderr(), "Review %s: %s (%s)\n", client.CleanLine(finding.Path), client.CleanLine(finding.Reason), client.CleanLine(finding.Category))
	}
}
