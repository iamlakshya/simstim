package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		Long: "Starts the agent of a session again, in the same session, after it stopped or ended. It works\n" +
			"after stop, after the memory cap stopped the agent, after a crash or an exit, and after the\n" +
			"session's process on the host was lost. The agent picks up its conversation with its own resume\n" +
			"command, where it has one. Otherwise, a session that started with a command of its own runs that\n" +
			"command again. The session keeps its ID, name, folder and worktree. On a host with systemd,\n" +
			"anything that the agent left running in the session stops first. The secrets that hosts.toml\n" +
			"gives the host are fetched again. Any other secret that the session started with needs --secret.\n" +
			"While the agent still runs, resume attaches, or with --detach says that it runs.",
		Example: "  " + brand.Name + " resume my-vps/auth-fix\n" +
			"  " + brand.Name + " resume my-vps/auth-fix --memory-max 6G\n" +
			"  " + brand.Name + " resume my-vps/auth-fix --secret GITHUB_TOKEN\n" +
			"  " + brand.Name + " resume my-vps/auth-fix --detach --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := beforeAttach(cmd, asJSON, detach); err != nil {
				return err
			}
			a := client.ResumeArgs{}
			if cmd.Flags().Changed("memory-max") {
				n, err := client.ParseMemoryFlag(memoryMax)
				if err != nil {
					return err
				}
				a.MemoryMax = n
			}
			if cmd.Flags().Changed("secret") {
				var err error
				if secrets, err = parseNameList("--secret", secrets, false, "Pass one or more secret names from [secrets] in config.toml."); err != nil {
					return err
				}
			}
			r, err := resumeSession(cmd, args[0], a, secrets)
			if err != nil {
				return err
			}
			if r.res == nil {
				return resumeRunning(cmd, r, detach, asJSON)
			}
			h, s := r.host, r.res.Session
			debugStopped(cmd, h, r.res)
			switch {
			case !detach:
				return attach(cmd.Context(), h, s.ID, s.Name)
			case asJSON:
				return printJSON(sessionObject(h.Name, s))
			}
			say(cmd, "Resumed %s/%s (%s, %s): %s.\n", shown(h.Name), shown(s.ID), shown(s.Name), shown(s.Agent), shown(r.res.Why))
			return nil
		},
	}
	addMemoryMax(cmd, &memoryMax, "cap the session's memory from now on: auto, or a `size` such as 4G (default: the size it had)")
	addDetach(cmd, &detach, "resume it without attaching")
	addSecret(cmd, &secrets, "pass again a secret that the session started with and that hosts.toml does not give its host (repeatable, or comma-separated)")
	addJSON(cmd, &asJSON)
	return cmd
}

// resumed is what resumeSession did: the host, the session, and the
// resume's result. res is nil when the agent still ran, so nothing was
// resumed.
type resumed struct {
	host client.Host
	s    client.Session
	res  *client.ResumeResult
}

// resumeSession finds the session that target names, an address, and
// resumes its agent with a, unless the agent still runs (cli-spec 9.4).
// When another client starts the agent again first, between the list and
// the resume, the agent runs too. passed names the secrets that the user
// passed.
func resumeSession(cmd *cobra.Command, target string, a client.ResumeArgs, passed []string) (resumed, error) {
	var r resumed
	addr, err := resolveAddress(cmd, target, false)
	if err != nil {
		return r, err
	}
	err = addr.open(cmd.Context(), cmd, func(ctx context.Context, f found) error {
		r.host, r.s = f.host, *f.s
		if !client.Ended(*f.s) {
			return nil
		}
		res, now, rerr := resumeRacing(ctx, f, a, passed, secretTerminal(cmd))
		if now != nil {
			r.s = *now
			return nil
		}
		r.res = res
		return rerr
	})
	return r, err
}

// beforeResume runs in resumeFound just before the host's resume. Tests
// set it to make another client resume the agent first.
var beforeResume = func(found) {}

// resumeRacing resumes the ended agent of the session f found, as
// resumeFound does, when another client may start it again first: the
// host then refuses with agent_running. The session's row decides what
// follows. When it shows the agent running, running is that row, and the
// command goes on as on a running agent. When it shows the agent ended
// again, the resume tries once more, and a second refusal of that kind is
// the error of an ended agent.
func resumeRacing(ctx context.Context, f found, a client.ResumeArgs, passed []string, terminal bool) (
	res *client.ResumeResult, running *client.Session, err error,
) {
	for try := 0; ; try++ {
		res, err = resumeFound(ctx, f, a, passed, terminal)
		row, refused := refusedRow(ctx, f, err)
		switch {
		case !refused || row == nil:
			return res, nil, err
		case !client.Ended(*row):
			return nil, row, nil
		case try > 0:
			return nil, nil, endedError(f.host, *row)
		}
		f.s = row
	}
}

// refusedRow reports whether err is the host's refusal of a resume
// because the agent runs, and then reads the session's current row, or
// nil when the host does not list it.
func refusedRow(ctx context.Context, f found, err error) (*client.Session, bool) {
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "agent_running" {
		return nil, false
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	list, lerr := listSessions(lctx, f.conn)
	if lerr != nil {
		return nil, true
	}
	return byID(list, f.s.ID), true
}

// resumeFound starts again the ended agent of the session f found, with a.
// It fetches the session's secrets again, as a start does: those that
// hosts.toml gives the host, and those that the user passed. terminal lets
// a secret's command ask on the terminal.
func resumeFound(ctx context.Context, f found, a client.ResumeArgs, passed []string, terminal bool) (*client.ResumeResult, error) {
	names, err := client.ResumeSecrets(f.host, *f.s, passed)
	if err != nil {
		return nil, err
	}
	// A password manager may want a touch, so the secrets get their own time.
	a.Secrets, err = client.FetchSecrets(ctx, client.LoadSettings(), names, terminal)
	if err != nil {
		return nil, err
	}
	a.ID = f.s.ID
	beforeResume(f)
	// The host stops the old holder and its unit, then waits for the start.
	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return f.conn.Resume(rctx, a)
}

// resumeRunning is resume on a session whose agent still runs: it attaches,
// with a note, or with --detach says that the agent runs. A flag that only
// a start reads changes nothing, and resume says so (cli-spec 9.4).
func resumeRunning(cmd *cobra.Command, r resumed, detach, asJSON bool) error {
	var unused []string
	for _, f := range []string{"memory-max", "secret"} {
		if cmd.Flags().Changed(f) {
			unused = append(unused, "--"+f)
		}
	}
	name, host := shownShort(nameOf(r.s)), shownShort(r.host.Name)
	if len(unused) > 0 {
		note(cmd, "flags_unused", fmt.Sprintf("The agent of %s on %s runs, so %s %s nothing. Pass %s again after the agent ends.",
			name, host, strings.Join(unused, " and "), plural(len(unused), "changes", "change"), plural(len(unused), "it", "them")))
	}
	switch {
	case !detach:
		say(cmd, "%s on %s is running, so this attaches.\n", name, host)
		return attach(cmd.Context(), r.host, r.s.ID, r.s.Name)
	case asJSON:
		return printJSON(sessionObject(r.host.Name, r.s))
	}
	say(cmd, "%s on %s is already running.\n", name, host)
	return nil
}

// debugStopped says when a resume stopped the session's debug, before an
// attach takes the terminal.
func debugStopped(cmd *cobra.Command, h client.Host, res *client.ResumeResult) {
	if !res.DebugStopped {
		return
	}
	s := res.Session
	note(cmd, "debug_stopped", fmt.Sprintf("Debug was on in %s, and stopped with the agent's old run. To record again: %s",
		shownShort(nameOf(s)), sessionCommand(h.Name, s.ID, "debug start")))
}
