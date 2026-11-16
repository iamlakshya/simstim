package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
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
