package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func resumeCmd() *cobra.Command {
	var memoryMax string
	var detach bool
	var secrets []string
	cmd := &cobra.Command{
		Use:   "resume <host>/<session>",
		Short: "Start a session's agent again after it ended, and attach to it",
		Long: "Starts the agent of a session again, in the same session, after the agent ended. It works after\n" +
			"the memory cap stopped the agent, after a crash or an exit, and after its holder was lost. The agent\n" +
			"picks up its conversation with its own resume command, where it has one. The session keeps its ID,\n" +
			"name, folder and worktree. On a host with systemd, anything that the agent left running in the\n" +
			"session stops first. The secrets that hosts.toml gives the host are fetched again. Any other secret\n" +
			"that the session started with needs --secret.",
		Example: "  " + brand.Name + " resume my-vps/app\n" +
			"  " + brand.Name + " resume my-vps/app --memory-max 6G\n" +
			"  " + brand.Name + " resume my-vps/app --secret GITHUB_TOKEN",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := client.ResumeArgs{}
			if cmd.Flags().Changed("memory-max") {
				n, err := client.ParseMemoryFlag(memoryMax)
				if err != nil {
					return err
				}
				a.MemoryMax = n
			}
			h, res, err := resumeSession(cmd.Context(), args[0], a, secrets)
			if err != nil {
				return err
			}
			s := res.Session
			if res.DebugStopped {
				// Before an attach takes the terminal.
				fmt.Fprintf(os.Stderr, "Debug was on in %s, and stopped with the agent's old run. To record again: %s debug start %s/%s\n",
					s.Name, brand.Name, h.Name, s.Name)
			}
			if detach {
				fmt.Printf("Resumed %s/%s (%s, %s): %s.\n", h.Name, s.ID, s.Name, s.Agent, res.Why)
				return nil
			}
			return attach(cmd.Context(), h, s.ID, s.Name)
		},
	}
	cmd.Flags().StringVar(&memoryMax, "memory-max", "", "cap the session's memory from now on: auto, or a `size` such as 4G (default: the size it had)")
	cmd.Flags().BoolVar(&detach, "detach", false, "resume it without attaching")
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "pass again a secret that the session started with and that hosts.toml does not give its host (repeatable)")
	return cmd
}

// resumeSession finds the session target and resumes its agent with a. It
// fetches the session's secrets again, as a start does: those that
// hosts.toml gives the host, and those that the user passed.
func resumeSession(ctx context.Context, target string, a client.ResumeArgs, passed []string) (client.Host, *client.ResumeResult, error) {
	hn, ref, err := client.SplitTarget(target)
	if err != nil {
		return client.Host{}, nil, err
	}
	h, err := client.FindHost(hn)
	if err != nil {
		return h, nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(dctx, h, true)
	if err != nil {
		return h, nil, err
	}
	defer conn.Close()
	s, err := resolve(dctx, conn, ref)
	if err != nil {
		return h, nil, err
	}
	if !client.Ended(*s) {
		return h, nil, &client.UserError{Msg: fmt.Sprintf("the agent of %s on %s still runs", s.Name, h.Name),
			Hint: fmt.Sprintf("%s attach %s/%s", brand.Name, h.Name, s.ID)}
	}
	names, err := client.ResumeSecrets(h, *s, passed)
	if err != nil {
		return h, nil, err
	}
	// A password manager may want a touch, so the secrets get their own time.
	a.Secrets, err = client.FetchSecrets(ctx, client.LoadSettings(), names, isTerminal(os.Stdin))
	if err != nil {
		return h, nil, err
	}
	a.ID = s.ID
	// The host stops the old holder and its unit, then waits for the start.
	rctx, rcancel := context.WithTimeout(ctx, 90*time.Second)
	defer rcancel()
	res, err := conn.Resume(rctx, a)
	return h, res, err
}
