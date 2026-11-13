package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func resumeCmd() *cobra.Command {
	var memoryMax string
	var detach, asJSON bool
	var secrets []string
	cmd := &cobra.Command{
		Use:   "resume <host>/<session>",
		Short: "Start a session's agent again after it ended, and attach to it",
		Long: "Starts the agent of a session again, in the same session, after the agent ended. It works after\n" +
			"the memory cap stopped the agent, after a crash or an exit, and after the session's process on the\n" +
			"host was lost. The agent picks up its conversation with its own resume command, where it has one.\n" +
			"Otherwise, a session that started with a command of its own runs that command again. The session\n" +
			"keeps its ID, name, folder and worktree. On a host with systemd, anything that the agent left\n" +
			"running in the session stops first. The secrets that hosts.toml gives the host are fetched again.\n" +
			"Any other secret that the session started with needs --secret.",
		Example: "  " + brand.Name + " resume my-vps/auth-fix\n" +
			"  " + brand.Name + " resume my-vps/auth-fix --memory-max 6G\n" +
			"  " + brand.Name + " resume my-vps/auth-fix --secret GITHUB_TOKEN\n" +
			"  " + brand.Name + " resume my-vps/auth-fix --detach --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON && !detach {
				return needsDetach(cmd)
			}
			a := client.ResumeArgs{}
			if cmd.Flags().Changed("memory-max") {
				n, err := client.ParseMemoryFlag(memoryMax)
				if err != nil {
					return err
				}
				a.MemoryMax = n
			}
			r, err := resumeSession(cmd.Context(), args[0], a, secrets, secretTerminal(cmd))
			if err != nil {
				return err
			}
			h, s := r.host, r.res.Session
			d := client.CleanSession(s)
			if r.res.DebugStopped {
				// Before an attach takes the terminal.
				note(cmd, "debug_stopped", fmt.Sprintf("Debug was on in %s, and stopped with the agent's old run. To record again: %s debug start %s",
					d.Name, brand.Name, client.CommandTarget(h.Name, s.ID)))
			}
			switch {
			case !detach:
				return attach(cmd.Context(), h, s.ID, s.Name)
			case asJSON:
				return printJSON(sessionObject(h.Name, s, r.list))
			}
			say(cmd, "Resumed %s/%s (%s, %s): %s.\n", h.Name, d.ID, d.Name, d.Agent, client.CleanLine(r.res.Why))
			return nil
		},
	}
	cmd.Flags().StringVar(&memoryMax, "memory-max", "", "cap the session's memory from now on: auto, or a `size` such as 4G (default: the size it had)")
	cmd.Flags().BoolVar(&detach, "detach", false, "resume it without attaching")
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "pass again a secret that the session started with and that hosts.toml does not give its host (repeatable)")
	addJSON(cmd, &asJSON)
	return cmd
}

// resumed is what resumeSession did: the host, the resume's result, and
// the host's sessions before it.
type resumed struct {
	host client.Host
	res  *client.ResumeResult
	list []client.Session
}

// resumeSession finds the session target and resumes its agent with a. It
// fetches the session's secrets again, as a start does: those that
// hosts.toml gives the host, and those that the user passed. terminal lets
// a secret's command ask on the terminal.
func resumeSession(ctx context.Context, target string, a client.ResumeArgs, passed []string, terminal bool) (resumed, error) {
	hn, ref, err := client.SplitTarget(target)
	if err != nil {
		return resumed{}, err
	}
	h, err := client.FindHost(hn)
	r := resumed{host: h}
	if err != nil {
		return r, err
	}
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(dctx, h, true)
	if err != nil {
		return r, err
	}
	defer conn.Close()
	if r.list, err = conn.Sessions(dctx); err != nil {
		return r, err
	}
	s, err := resolveIn(r.list, ref, h.Name)
	if err != nil {
		return r, err
	}
	if !client.Ended(*s) {
		return r, &client.UserError{Msg: fmt.Sprintf("the agent of %s on %s still runs", client.CleanLine(s.Name), h.Name),
			Hint: fmt.Sprintf("%s attach %s", brand.Name, client.CommandTarget(h.Name, s.ID)), Code: "agent_running"}
	}
	names, err := client.ResumeSecrets(h, *s, passed)
	if err != nil {
		return r, err
	}
	// A password manager may want a touch, so the secrets get their own time.
	a.Secrets, err = client.FetchSecrets(ctx, client.LoadSettings(), names, terminal)
	if err != nil {
		return r, err
	}
	a.ID = s.ID
	// The host stops the old holder and its unit, then waits for the start.
	rctx, rcancel := context.WithTimeout(ctx, 90*time.Second)
	defer rcancel()
	r.res, err = conn.Resume(rctx, a)
	return r, err
}
