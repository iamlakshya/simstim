package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

// startCountSession optionally overrides session creation in `new --count` for
// tests that simulate partial refusal.
var startCountSession func(*cobra.Command, *client.Conn, client.Host, client.NewArgs, *preparedNewP114) (*client.Session, error)

type countErrorItem struct {
	Name  string    `json:"name"`
	Error errorJSON `json:"error"`
}

type parsedNewMode struct {
	countMode  bool
	nonTTYExec bool
	userArgv   []string
	wallLevel  string
	host       client.Host
}

func validateNewRequest(cmd *cobra.Command, args []string, o *newOpts) (parsedNewMode, error) {
	warnOldNewFlags(cmd, *o)
	var m parsedNewMode
	m.countMode = cmd.Flags().Changed("count")
	if m.countMode && o.count < 1 {
		return m, &client.UserError{
			Msg:  fmt.Sprintf("--count: %d must be at least 1", o.count),
			Hint: "Pass a positive number of sessions, such as --count 4.",
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	if dash := cmd.ArgsLenAtDash(); dash >= 0 && dash < len(args) {
		m.userArgv = args[dash:]
	}
	if err := checkNewAttachMode(cmd, o, &m); err != nil {
		return m, err
	}
	var err error
	if m.wallLevel, err = validateNewOpts(cmd, o); err != nil {
		return m, err
	}
	if m.host, err = client.FindHost(args[0]); err != nil {
		return m, err
	}
	return m, nil
}

func checkNewAttachMode(cmd *cobra.Command, o *newOpts, m *parsedNewMode) error {
	if m.countMode || o.detach {
		return nil
	}
	if o.asJSON {
		return needsDetach(cmd)
	}
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		return nil
	}
	if len(m.userArgv) > 0 {
		m.nonTTYExec = true
		return nil
	}
	return attachNeedsATerminal(cmd)
}

func runNew(cmd *cobra.Command, args []string, o newOpts) error {
	m, err := validateNewRequest(cmd, args, &o)
	if err != nil {
		return err
	}
	settings := client.LoadSettings()
	var prep *preparedNewP114
	if o.in == "" {
		prep, err = prepareNewP114(cmd, m.host, settings, o.dir, o.worktree, o.p114)
		if err != nil {
			return err
		}
		defer prep.cleanup()
	}
	// Fetch the secrets first: a password manager may want a touch,
	// and a failing command should not cost a connection.
	secretNames := client.GatewaySecretNames(settings, m.host, o.agent, m.wallLevel, o.secrets, o.expose)
	values, err := client.FetchSecrets(cmd.Context(), settings, secretNames, secretTerminal(cmd))
	if err != nil {
		return err
	}
	secretBinds := client.SecretBindsFor(settings, secretNames)
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	conn, err := client.Dial(ctx, m.host, true)
	if err != nil {
		cancel()
		return err
	}
	cols, rows := termSize()
	a := client.TerminalArgs(client.NewArgs{
		Agent: o.agent, Cwd: client.RemotePath(o.dir), MakeFolder: o.mkdir, Name: o.name,
		Worktree: o.worktree, Base: o.base, Inline: o.inline,
		Secrets: values, SecretBinds: secretBinds, ExposedSecrets: o.expose, Wall: m.wallLevel,
		Image: o.image, KeepSystem: o.keepSystem, Docker: o.docker, Task: o.task,
		RemoveOnExit:     o.rm,
		RemoveWaitAttach: o.rm && !o.detach && !m.countMode && !m.nonTTYExec,
	}, cols, rows)
	if len(m.userArgv) > 0 {
		a.Argv = m.userArgv
	}
	if o.in != "" {
		if ierr := applyNewIn(ctx, cmd, conn, m.host.Name, o.in, &a); ierr != nil {
			cancel()
			conn.Close()
			return ierr
		}
	}
	if o.setup || o.noSetup {
		a.Setup = setupValue(o.setup)
	}
	cancel()

	if m.countMode {
		defer conn.Close()
		return runNewCount(cmd, conn, m.host, a, prep, o)
	}
	if m.nonTTYExec {
		return runNewNonTTYExec(cmd, conn, m.host, a, prep, m.userArgv, o.rm)
	}

	s, err := startNewWithP114(cmd, conn, m.host, a, prep)
	if err != nil {
		conn.Close()
		return err
	}
	// An address without a host finds the new session at once.
	client.RememberSession(m.host.Name, *s)
	conn.Close()
	return finishNewSingle(cmd, m.host, s, o)
}

func finishNewSingle(cmd *cobra.Command, h client.Host, s *client.Session, o newOpts) error {
	if !o.detach {
		if !o.rm {
			return attach(cmd.Context(), h, s.ID, s.Name)
		}
		res, err := attachResult(cmd.Context(), h, s.ID, s.Name)
		if err != nil {
			return err
		}
		if res.Ended != nil {
			if ferr := finishThrowaway(cmd, h, s.ID); ferr != nil {
				return ferr
			}
			if res.Ended.ExitCode != 0 {
				return silentError{code: res.Ended.ExitCode}
			}
		}
		return nil
	}
	if o.asJSON {
		return printJSON(sessionObject(h.Name, *s))
	}
	say(cmd, "Started %s/%s (%s, %s).\n", shown(h.Name), shown(s.ID), shown(s.Name), shown(s.Agent))
	return nil
}

// runNewCount starts o.count sessions named <base>-1 to <base>-<n> without
// attaching (isolation spec 17.6, cli-spec 7.1).
func runNewCount(cmd *cobra.Command, conn *client.Conn, h client.Host, baseArgs client.NewArgs, prep *preparedNewP114, o newOpts) error {
	base := o.name
	if base == "" && prep != nil && prep.defaultName != "" {
		base = prep.defaultName
	}
	if base == "" {
		base = defaultCountBase(o.dir, o.agent)
	}
	type item struct {
		name    string
		session *client.Session
		err     error
	}
	results := make([]item, 0, o.count)
	started := 0
	var firstErr error
	startFn := startCountSession
	if startFn == nil {
		startFn = startNewWithP114
	}
	for i := 1; i <= o.count; i++ {
		sessName := fmt.Sprintf("%s-%d", base, i)
		a := baseArgs
		a.Name = sessName
		s, err := startFn(cmd, conn, h, a, prep)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			results = append(results, item{name: sessName, err: err})
			continue
		}
		started++
		client.RememberSession(h.Name, *s)
		results = append(results, item{name: sessName, session: s})
	}
	if started == 0 && firstErr != nil {
		return firstErr
	}
	if o.asJSON {
		out := make([]any, 0, len(results))
		for _, r := range results {
			if r.err != nil {
				out = append(out, countErrorItem{Name: r.name, Error: reportErrorJSON(r.err, cmd)})
				continue
			}
			out = append(out, sessionObject(h.Name, *r.session))
		}
		if err := printJSON(out); err != nil {
			return err
		}
		if firstErr != nil {
			return silentError{code: exitCode(describe(firstErr, cmd).kind)}
		}
		return nil
	}
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "Failed %s on %s: %s.\n", shown(r.name), shown(h.Name), shown(describe(r.err, cmd).msg))
			continue
		}
		say(cmd, "Started %s/%s (%s, %s).\n", shown(h.Name), shown(r.session.ID), shown(r.session.Name), shown(r.session.Agent))
	}
	if firstErr != nil {
		return silentError{code: exitCode(describe(firstErr, cmd).kind)}
	}
	return nil
}

// runNewNonTTYExec starts a session without attaching when `new ... -- <cmd>`
// runs without a terminal (such as in CI), executes userArgv through the
// session's exec stream, cleans up if rm is set, and exits with the command's
// exit code (isolation spec 17.6).
func runNewNonTTYExec(cmd *cobra.Command, conn *client.Conn, h client.Host, a client.NewArgs, prep *preparedNewP114, userArgv []string, rm bool) error {
	a.Argv = []string{"/bin/sh", "-c", "exec sleep 86400"}
	a.RemoveOnExit = false
	a.RemoveWaitAttach = false
	s, err := startNewWithP114(cmd, conn, h, a, prep)
	if err != nil {
		conn.Close()
		return err
	}
	client.RememberSession(h.Name, *s)
	conn.Close()

	code, execErr := client.RunSessionExec(cmd.Context(), h, s, joinShellCommand(userArgv), cmd.InOrStdin(), os.Stdout, os.Stderr)
	if rm {
		if ferr := finishThrowaway(cmd, h, s.ID); ferr != nil && execErr == nil {
			execErr = ferr
		}
	} else {
		if serr := stopNonTTYExec(cmd, h, s.ID); serr != nil && execErr == nil {
			execErr = serr
		}
	}
	if execErr != nil {
		return execErr
	}
	if code != 0 {
		return silentError{code: code}
	}
	return nil
}

// stopNonTTYExec stops the placeholder sleep session after a non-TTY
// `new -- <command>` run finishes without `--rm`, leaving the session stopped
// with its workspace and commits on disk instead of leaking a running process.
func stopNonTTYExec(cmd *cobra.Command, h client.Host, id string) error {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 30*time.Second)
	defer cancel()
	conn, err := client.Dial(sctx, h, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Stop(sctx, id)
	return err
}

// finishThrowaway calls session.finish for a `new --rm` session and prints the
// stop note when unpulled commits kept the session instead of removing it.
func finishThrowaway(cmd *cobra.Command, h client.Host, id string) error {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 30*time.Second)
	defer cancel()
	conn, err := client.Dial(fctx, h, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	res, err := conn.FinishSession(fctx, id)
	if err != nil {
		return err
	}
	if res.Action == "removed" {
		client.ForgetSession(h.Name, id)
		return nil
	}
	if res.Message != "" {
		say(cmd, "%s.\n", shown(res.Message))
	}
	return nil
}

func defaultCountBase(dir, agent string) string {
	d := strings.TrimSpace(dir)
	if d != "" && d != "~" && d != "." && d != "/" {
		base := path.Base(strings.ReplaceAll(d, "\\", "/"))
		if base != "" && base != "." && base != "/" && client.CheckSessionName(base) == nil {
			return base
		}
	}
	if agent != "" && client.CheckSessionName(agent) == nil {
		return agent
	}
	return "session"
}

func joinShellCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for i, w := range argv {
		quoted[i] = "'" + strings.ReplaceAll(w, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

func reportErrorJSON(err error, cmd *cobra.Command) errorJSON {
	r := describe(err, cmd)
	code := exitCode(r.kind)
	var data json.RawMessage
	if json.Valid(r.data) {
		data = r.data
	}
	return errorJSON{
		Code:        r.code,
		Kind:        r.kind.String(),
		Message:     r.msg,
		Try:         r.try,
		Exit:        code,
		Suggestions: r.suggestions,
		Data:        data,
	}
}
