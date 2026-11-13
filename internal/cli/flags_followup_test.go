package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// earlyFlagCommand refuses to run any command body. These cases must end
// at version handling, argument validation or flag parsing, before hooks.
func earlyFlagCommand(t *testing.T, w io.Writer, args []string) (*cobra.Command, error) {
	t.Helper()
	root := Root()
	root.SetOut(io.Discard)
	root.SetErr(w)
	root.SetArgs(args)
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		t.Error("the early-exit fixture reached the command hook")
		return errors.New("the fixture must stop before a command runs")
	}
	return root.ExecuteContextC(withLine(context.Background(), args))
}

// The warning and error streams agree even when pflag stops before json
// or Args stops execution before the normal warning hook.
func TestEarlyOldFlagsKeepJSONStderr(t *testing.T) {
	withFake(t, nil)
	for _, args := range [][]string{
		{"new", "my-vps", "-d", "/srv", "--badflag", "--json"},
		{"new", "my-vps", "extra", "-d", "/srv", "--json"},
		{"host", "create", "lima", "--memory", "8", "--json"},
		{"agents", "install", "claude", "--force", "--json"},
		{"-v", "--json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stderr, code := mainLine(t, args...)
			if code != 2 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			warning, failure := false, false
			for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
				var value struct {
					V       int             `json:"v"`
					Warning json.RawMessage `json:"warning"`
					Error   json.RawMessage `json:"error"`
				}
				if err := json.Unmarshal([]byte(line), &value); err != nil || value.V != jsonVersion {
					t.Fatalf("non-JSON stderr line %q: %v", line, err)
				}
				warning = warning || len(value.Warning) > 0
				failure = failure || len(value.Error) > 0
			}
			if !warning || !failure {
				t.Fatalf("missing warning or failure object: %s", stderr)
			}
		})
	}
}

// Words after -- and string-flag values do not request an output format.
func TestEarlyOldWarningsDoNotReadPositionalFlags(t *testing.T) {
	withFake(t, nil)
	for _, args := range [][]string{
		{"-v", "--", "--json"},
		{"new", "my-vps", "-d", "--json", "--badflag"},
		{"new", "my-vps", "-d", "/srv", "--badflag", "--debug-file", "--json"},
	} {
		_, stderr, _ := mainLine(t, args...)
		if !strings.HasPrefix(stderr, "Warning: ") || json.Valid([]byte(strings.TrimSpace(stderr))) {
			t.Fatalf("a positional/value word changed the warning format for %q: %s", args, stderr)
		}
	}
}

// An actual terminal makes the control cases yellow. no-color must still
// suppress it before the hook, while false/value/positional forms do not.
func TestEarlyOldWarningsRespectColour(t *testing.T) {
	withFake(t, nil)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	for _, tt := range []struct {
		args   []string
		colour bool
	}{
		{[]string{"-v"}, true},
		{[]string{"-v", "--no-color"}, false},
		{[]string{"-v", "--no-color=false"}, true},
		{[]string{"-v", "--", "--no-color"}, true},
		{[]string{"new", "my-vps", "-d", "/srv", "--badflag", "--no-color"}, false},
		{[]string{"host", "alerts", "log", "my-vps", "extra", "-n", "5", "--no-color"}, false},
		{[]string{"new", "my-vps", "-d", "--no-color", "--badflag"}, true},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			term := hosttest.NewTerminal(t, 100, 10)
			cmd, _ := earlyFlagCommand(t, term.TTY, tt.args)
			warnOldFlags(cmd)
			fmt.Fprintln(term.TTY, "warning-end")
			out := string(term.WaitFor("warning-end"))
			if !strings.Contains(out, "Warning: ") || strings.Contains(out, "\x1b[33m") != tt.colour {
				t.Fatalf("warning colour = %q, want colour %v", out, tt.colour)
			}
		})
	}
}

// Explicit empty lists are errors, while absent events retain defaults
// and valid repeated/comma forms retain every name.
func TestAlertsEventsDistinguishEmptyFromAbsent(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want []string
		bad  bool
	}{
		{nil, nil, false},
		{[]string{"--events", ""}, nil, true},
		{[]string{"--events", ","}, nil, true},
		{[]string{"--events", " , , "}, nil, true},
		{[]string{"--events", "", "--events", "failed"}, []string{"failed"}, false},
		{[]string{"--events", "failed,oom", "--events", "started"}, []string{"failed", "oom", "started"}, false},
	} {
		f := alertsSetFlags{ntfy: "https://example.invalid/alerts"}
		cmd := &cobra.Command{}
		addList(cmd.Flags(), &f.events, "events", "events")
		if err := cmd.ParseFlags(tt.args); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := f.config(cmd)
		var user *client.UserError
		if tt.bad {
			if !errors.As(err, &user) || user.Code != "bad_value" || user.Kind != client.KindUsage || user.Hint == "" {
				t.Fatalf("empty changed events %q = %+v, %v", tt.args, cfg, err)
			}
		} else if err != nil || strings.Join(cfg.Events, ",") != strings.Join(tt.want, ",") || len(cfg.Destinations) != 1 {
			t.Fatalf("events %q = %+v, %v", tt.args, cfg, err)
		}
	}
	withFake(t, nil)
	for _, value := range []string{"", ",", " , , "} {
		got, code := lineErr(t, "host", "alerts", "set", "my-vps", "--events", value)
		if code != 2 || !strings.Contains(got, "--events: name at least one event") {
			t.Fatalf("empty events command exited %d: %s", code, got)
		}
	}
}
