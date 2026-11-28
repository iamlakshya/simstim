package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/tui"
)

// show describes one session (docs/contributing/cli-spec.md 9.6).

func showCmd() *cobra.Command {
	var asJSON, screen bool
	cmd := &cobra.Command{
		Use:   "show [<host>/<session>|-]",
		Short: "Describe a session",
		Long: "Prints everything about one session, one line for each detail that it has. With --screen,\n" +
			"prints the session's last screen as plain text (or adds a screen field with --json). Without\n" +
			"a session, and at a terminal, it lets you pick one from a list. It does not wake a stopped host.",
		Example: "  " + brand.Name + " show my-vps/auth-fix\n" +
			"  " + brand.Name + " show my-vps/auth-fix --screen\n" +
			"  " + brand.Name + " show auth-fix --json   # the host is found when the name is unique\n" +
			"  " + brand.Name + " show -                 # the last session you attached to",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := sessionArg(cmd, args, false)
			if err != nil {
				return err
			}
			// Describing a session is no reason to start a machine.
			err = a.open(client.WithoutWake(cmd.Context()), cmd, func(ctx context.Context, f found) error {
				if screen {
					return showScreen(ctx, f, asJSON)
				}
				if asJSON {
					return printJSON(sessionObject(f.host.Name, *f.s))
				}
				printf("%s", describeSession(f.host.Name, *f.s))
				return nil
			})
			return stoppedHost(cmd.Context(), a.host, err)
		},
	}
	addJSON(cmd, &asJSON)
	cmd.Flags().BoolVar(&screen, "screen", false, "print the session's last screen as plain text (with --json, adds screen)")
	return cmd
}

// showScreen reads the session's last screen through the peek op and prints it
// as plain text, or adds it to the session's JSON object (cli-spec 9.6).
func showScreen(ctx context.Context, f found, asJSON bool) error {
	if !f.conn.HasFeature("peek") {
		return &client.UserError{
			Msg:  "the helper on " + f.host.Name + " is too old to read a session's screen",
			Hint: brand.Name + " host upgrade " + client.ShellQuote(f.host.Name),
			Code: "helper_old",
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, cols, rows, err := f.conn.Peek(pctx, f.s.ID, tui.PeekBytes)
	if err != nil {
		return err
	}
	lines := tui.RenderPlainScreen(data, cols, rows)
	if asJSON {
		obj := sessionObject(f.host.Name, *f.s)
		obj["screen"] = lines
		return printJSON(obj)
	}
	for _, line := range lines {
		printf("%s\n", line)
	}
	return nil
}

// detail is one line of show: a label, and the value.
type detail struct{ label, value string }

// describeSession is the text of show: a title line, then one line for
// each field of the session that has a value, in the order of the type,
// then the command that attaches, which names the session by host/ID.
// Every value from the host is cleaned of control and hidden characters.
// The name is in the title. The state line says where the state came from
// and why the agent ended, so a field that it says faithfully has no line
// of its own. The size joins the columns and the rows.
func describeSession(host string, s client.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s on %s\n", shown(nameOf(s)), shown(host))
	for _, d := range sessionDetails(s) {
		fmt.Fprintf(&b, "  %-15s %s\n", d.label, shown(d.value))
	}
	fmt.Fprintf(&b, "  %-15s %s\n", "Attach", sessionCommand(host, s.ID, "attach"))
	return b.String()
}

// sessionDetails is the lines of show for s, without its title and its
// attach line.
func sessionDetails(s client.Session) []detail {
	var ds []detail
	add := func(label, value string) {
		if value != "" {
			ds = append(ds, detail{label, value})
		}
	}
	state := showState(s)
	add("ID", s.ID)
	add("Agent", s.Agent)
	add("Folder", s.Cwd)
	add("State", state)
	add("Since", showTime(s.Since))
	add("Reason", unsaid(state, s.Reason))
	if s.ExitCode != nil && unsaid(state, fmt.Sprintf("exit code %d", *s.ExitCode)) != "" {
		add("Exit code", strconv.Itoa(*s.ExitCode))
	}
	add("Signal", unsaid(state, s.Signal))
	if s.OOM && !strings.Contains(state, "out of memory") && !strings.Contains(state, "hit the cap") {
		add("Memory cap hit", "yes")
	}
	add("Created", showTime(s.CreatedAt))
	add("Last activity", showTime(s.LastActivityAt))
	add("Last attached", showTime(s.LastAttachedAt))
	if client.Ended(s) && s.Alive {
		add("Session", "still open: resume starts the agent again, and rm removes it")
	}
	add("Unit", s.Unit)
	add("Memory cap", showSize(s.MemoryMax))
	add("Memory used", showSize(s.MemoryCurrent))
	add("Host helper", s.HostVersion)
	if s.Cols > 0 && s.Rows > 0 {
		add("Size", fmt.Sprintf("%d×%d", s.Cols, s.Rows))
	}
	if s.FullScreen {
		add("Full screen", "yes")
	}
	add("Missing secrets", strings.Join(s.MissingSecrets, ", "))
	add("Secrets", strings.Join(s.Secrets, ", "))
	add("Sandbox", showSandbox(s.Wall))
	add("Usage", showUsage(s.Usage))
	add("Models", showUsageModels(s.Usage))
	if s.DebugUntil != nil {
		add("Debug until", showTime(*s.DebugUntil))
	}
	return ds
}

// saidAs maps a reason that the host sends as a code to the words of the
// state line that say it.
var saidAs = map[string]string{"oom": "out of memory", api.ReasonChildOOM: "hit the cap"}

// unsaid is value, unless the state line already says it: then "".
func unsaid(state, value string) string {
	value = shown(value)
	words := value
	if w, ok := saidAs[value]; ok {
		words = w
	}
	if value == "" || strings.Contains(state, words) {
		return ""
	}
	return value
}

// showState is the session's state as ls prints it, with where the state
// came from: "working (from hooks)", "errored (exit code 3, from process)".
func showState(s client.Session) string {
	if s.State == "" {
		return ""
	}
	var why []string
	if c := endCause(s); c != "" {
		why = append(why, c)
	}
	if s.Source != "" {
		why = append(why, "from "+shown(s.Source))
	}
	st := strings.ReplaceAll(shown(s.State), "_", " ")
	if len(why) == 0 {
		return st
	}
	return st + " (" + strings.Join(why, ", ") + ")"
}

// showZone is the time zone of show's times: the local one. Tests set it.
var showZone = time.Local

// showTime is t in the local time, such as "10 Nov 2026 10:40", or "" for
// a zero time.
func showTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(showZone).Format("2 Jan 2006 15:04")
}

// showSize is a size in bytes as a person reads it, such as "4.0 GiB",
// or "" for none.
func showSize(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	}
	return sizeText(n)
}

func showSandbox(w *api.WallInfo) string {
	if w == nil || w.Level == "" {
		return ""
	}
	var opts []string
	if w.DowngradedFrom != "" {
		if w.DowngradedFrom == "default" || w.DowngradedFrom == api.LevelNone {
			opts = append(opts, "! downgraded")
		} else {
			opts = append(opts, "! below the floor "+w.DowngradedFrom)
		}
	}
	if w.Sudo {
		opts = append(opts, "root")
	}
	if w.Docker {
		opts = append(opts, "docker")
	}
	if w.System == api.SandboxSystemKeep {
		opts = append(opts, "keep-system")
	}
	if w.Image != "" {
		opts = append(opts, "image "+w.Image)
	}
	if len(opts) == 0 {
		return w.Level
	}
	return fmt.Sprintf("%s (%s)", w.Level, strings.Join(opts, ", "))
}

// stoppedHost is the error of show on a host that did not answer, for a
// host that its provider script starts, when the script's status says that
// the host is stopped: show does not start a stopped host, so the hint
// says what does. Any other error, such as a key that SSH refused, or a
// status that does not say stopped, stays as it is, with its own hint.
func stoppedHost(ctx context.Context, h client.Host, err error) error {
	var ue *client.UserError
	if h.Provider == nil || !errors.As(err, &ue) || !strings.HasPrefix(ue.Msg, "cannot reach") {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, serr := client.RunProvider(sctx, h, "status")
	var st client.ProviderStatus
	if serr != nil || json.Unmarshal(out, &st) != nil || st.State != "stopped" {
		return err
	}
	hint := hostStartHint(h.Name)
	if h.Provider.WakeOnAttach {
		hint += " (an attach to one of its sessions starts it too)"
	}
	return &client.UserError{Msg: h.Name + " is stopped, and show does not start it", Hint: hint, Code: "host_stopped"}
}
