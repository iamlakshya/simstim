package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

var qRewind = &question{cmd: "rewind", flag: "-y"}

func init() { questions = append(questions, qRewind) }

func rewindCmd() *cobra.Command {
	var list, yes, asJSON bool
	var secrets []string
	cmd := &cobra.Command{
		Use: "rewind <host>/<session> [<n>]", Short: "List disk checkpoints or return to one",
		Long: "Lists a microvm sandbox's disk checkpoints, newest first, numbered from 1. Choose a number\n" +
			"to restore its files and conversation. Rewind makes an undo checkpoint first, then starts the\n" +
			"sandbox cold. Without a number it asks at a terminal. Reflink support is required on the host.",
		Example: "  " + brand.Name + " rewind box/auth-fix --list\n  " + brand.Name + " rewind box/auth-fix 2 -y",
		Args:    cobra.RangeArgs(1, 2), ValidArgsFunction: completeSessionArg,
		RunE: func(cmd *cobra.Command, words []string) error {
			if list && len(words) > 1 {
				return &client.UserError{Code: "flags_conflict", Msg: "--list cannot select a checkpoint to restore", Hint: "Use --list alone, or remove it and pass a checkpoint number.", Kind: client.KindUsage}
			}
			number := 0
			if len(words) > 1 {
				var err error
				number, err = strconv.Atoi(words[1])
				if err != nil || number < 1 {
					return rewindNumberError()
				}
			}
			if !list {
				if _, err := client.ReadPolicySettings(); err != nil {
					return err
				}
			}
			return withSession(cmd, words[0], func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) (returned error) {
				result, err := c.Checkpoints(ctx, s.ID)
				if err != nil {
					return err
				}
				if list {
					if asJSON {
						return printJSON(struct {
							Version int    `json:"v"`
							Host    string `json:"host"`
							api.CheckpointsResult
						}{1, h.Name, result})
					}
					printCheckpoints(cmd, result)
					return nil
				}
				p := sessionPrompts(cmd, h.Name, s.ID)
				if number == 0 {
					printCheckpoints(cmd, result)
					if len(result.Checkpoints) == 0 {
						return unknownCheckpoint(1, 0)
					}
					if !p.on {
						return p.needs(qRewind, "a checkpoint number", "<n>", "-y")
					}
					number, err = askCheckpointNumber(p, len(result.Checkpoints))
					if err != nil {
						return err
					}
				}
				if number > len(result.Checkpoints) {
					return unknownCheckpoint(number, len(result.Checkpoints))
				}
				selected := result.Checkpoints[number-1]
				printRewindReview(cmd, selected, result.CurrentRefs)
				if !yes {
					answer, err := p.confirm(qRewind, "Restore this checkpoint of "+shownShort(s.Name)+" on "+shownShort(h.Name)+"?", "a yes to restore the selected checkpoint")
					if err != nil {
						return err
					}
					if !answer {
						return p.cancel()
					}
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				request := client.NewGrantRequest(false, false)
				defer func() { returned = errors.Join(returned, request.Close()) }()
				grantCtx := client.WithGrantRequest(ctx, request)
				var restored api.RewindResult
				_, err = grantQuestionsOn(cmd, p, grantCtx, func(granted context.Context) error {
					prepared, err := c.PrepareRewind(granted, api.RewindArgs{ID: s.ID, Checkpoint: selected.Name}, client.RewindOptions{PassedSecrets: secrets, Terminal: secretTerminal(cmd)})
					if err != nil {
						return err
					}
					rewindCtx, cancel := context.WithTimeout(granted, 90*time.Second)
					defer cancel()
					restored, err = prepared.Commit(rewindCtx)
					return err
				})
				if err != nil {
					return err
				}
				client.RememberSession(h.Name, restored.Session)
				if asJSON {
					return printJSON(struct {
						Version    int    `json:"v"`
						Host       string `json:"host"`
						ID         string `json:"id"`
						Checkpoint string `json:"checkpoint"`
						Session    any    `json:"session"`
					}{1, h.Name, restored.ID, restored.Checkpoint, sessionObject(h.Name, restored.Session)})
				}
				say(cmd, "Restored %s on %s to %s.\n", shownShort(s.Name), shownShort(h.Name), selected.CreatedAt.UTC().Format(time.RFC3339Nano))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "list checkpoints without restoring one")
	addYes(cmd, &yes, "restore the selected checkpoint")
	addSecret(cmd, &secrets, "pass again secrets that the sandbox needs and that hosts.toml does not give its host")
	addJSON(cmd, &asJSON)
	return cmd
}

func printCheckpoints(cmd *cobra.Command, result api.CheckpointsResult) {
	if jsonOn(cmd) {
		return
	}
	if len(result.Checkpoints) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No checkpoints.")
		return
	}
	for index, checkpoint := range result.Checkpoints {
		fmt.Fprintf(cmd.OutOrStdout(), "%d  %s\n", index+1, checkpoint.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
}

func askCheckpointNumber(p *prompter, count int) (int, error) {
	for try := 0; try < 3; try++ {
		answer, err := p.ask(fmt.Sprintf("Checkpoint to restore (1-%d): ", count))
		if err != nil {
			return 0, err
		}
		number, err := strconv.Atoi(answer)
		if err == nil && number >= 1 && number <= count {
			return number, nil
		}
	}
	return 0, p.cancel()
}

func printRewindReview(cmd *cobra.Command, selected api.CheckpointInfo, current []proto.BundleRef) {
	if jsonOn(cmd) {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Checkpoint: %s\n", selected.CreatedAt.UTC().Format(time.RFC3339Nano))
	for _, change := range checkpointChanges(selected.Refs, current) {
		fmt.Fprintln(cmd.ErrOrStderr(), change)
	}
}

func checkpointChanges(saved, current []proto.BundleRef) []string {
	before := make(map[string]string, len(saved))
	after := make(map[string]string, len(current))
	names := make(map[string]bool, len(saved)+len(current))
	for _, ref := range saved {
		before[ref.Name] = ref.OID
		names[ref.Name] = true
	}
	for _, ref := range current {
		after[ref.Name] = ref.OID
		names[ref.Name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	slices.Sort(ordered)
	var changed []string
	for _, name := range ordered {
		if before[name] == after[name] {
			continue
		}
		old, new := before[name], after[name]
		if old == "" {
			old = "(absent)"
		}
		if new == "" {
			new = "(absent)"
		}
		changed = append(changed, client.CleanLine(strings.TrimPrefix(name, "refs/heads/"))+": "+new+" -> "+old)
	}
	return changed
}

func rewindNumberError() error {
	return &client.UserError{Code: "bad_checkpoint", Msg: "a checkpoint number must be a positive integer", Hint: "List checkpoints with rewind HOST/SESSION --list, then choose a number from 1.", Kind: client.KindUsage}
}

func unknownCheckpoint(number, count int) error {
	return &client.UserError{Code: "rewind_unknown", Msg: fmt.Sprintf("the session has no checkpoint %d: it keeps %d", number, count), Hint: "List checkpoints with rewind HOST/SESSION --list.", Kind: client.KindNotFound}
}
