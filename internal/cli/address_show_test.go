package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestAttachChecksTheTerminalFirst(t *testing.T) {
	th := newTestHost(t)
	newSession(t, "a", "sleep", "60")
	writeCache(t, `{}`)
	before := len(th.tr.Remotes())
	got, code := failsAs(t, "attach", "a")
	if code != 2 || !strings.Contains(got, "Error: attach needs a terminal.") || !strings.Contains(got, "Try: "+brand.Name+" send <host>/<ID> 'text'\n") {
		t.Errorf("attach a without a terminal: exit %d\n%s", code, got)
	}
	if n := len(th.tr.Remotes()); n != before {
		t.Errorf("attach without a terminal ran %d commands on the host", n-before)
	}
	_, err := run(t, "attach", "a")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "no_terminal" {
		t.Errorf("the code is %v", err)
	}
	// The attach of new does the same, before it starts anything.
	before = len(th.tr.Remotes())
	if got, code := failsAs(t, "new", "box"); code != 2 || !strings.Contains(got, "new attaches, so it needs a terminal") {
		t.Errorf("new without a terminal: exit %d\n%s", code, got)
	}
	if n := len(th.tr.Remotes()); n != before {
		t.Errorf("new without a terminal ran %d commands on the host", n-before)
	}
}

// show on a host that its provider script starts says that the host is
// stopped, and how to start it, only when the script's status says so
// (V5). Another failure, such as a key that SSH refused, keeps its own
// error and hint. The script's start never runs.
func TestShowOnAStoppedProviderHost(t *testing.T) {
	h := hosttest.New(t)
	t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
	started := filepath.Join(h.Home, "started")
	provider := func(state string) string {
		return h.Script("provider-"+state, "[ \"$1\" = start ] && touch "+started+"\n"+
			"[ \"$1\" = status ] && echo '{\"state\": \""+state+"\"}'\ntrue\n")
	}
	for _, c := range []struct{ state, ssh, want, try string }{
		{"stopped", "ssh: connect to host cloud port 22: Connection refused",
			"Error: cloud is stopped, and show does not start it.", "Try: " + brand.Name + " host start cloud (an attach to one of its sessions starts it too)"},
		{"running", "cloud: Permission denied (publickey).",
			"Permission denied (publickey)", "Try: ssh cloud"},
	} {
		h.Script("ssh", "echo \""+c.ssh+"\" >&2; exit 255\n")
		if err := client.SaveHosts(context.Background(), []client.Host{{Name: "cloud", SSH: "cloud", Provider: &client.Provider{Script: provider(c.state), WakeOnAttach: true}}}); err != nil {
			t.Fatal(err)
		}
		got, code := failsAs(t, "show", "cloud/c")
		if code != 1 || !strings.Contains(got, c.want) || !strings.Contains(got, c.try) {
			t.Errorf("show cloud/c on a %s host: exit %d\n%s", c.state, code, got)
		}
		if c.state == "running" && strings.Contains(got, "host start") {
			t.Errorf("a host that runs got the hint of a stopped one:\n%s", got)
		}
	}
	if _, err := os.Stat(started); err == nil {
		t.Error("show ran the provider script's start")
	}
}

// The text of show, from fixed data: a title, a line for each field with
// a value, in the order of the type, and the command that attaches, by
// host/ID.
func TestDescribeSession(t *testing.T) {
	old := showZone
	showZone = time.UTC
	t.Cleanup(func() { showZone = old })
	at := time.Date(2026, 11, 10, 10, 40, 0, 0, time.UTC)
	s := client.Session{ID: "abcd2345", Name: "auth-fix", Agent: "claude", Cwd: "/home/dev/src/app", State: "working", Source: "hooks",
		Since: at, CreatedAt: at, MemoryMax: 4 << 30, HostVersion: "0.2.0", Cols: 120, Rows: 36,
		Secrets: []string{"GITHUB_TOKEN", "NPM_TOKEN"}, MissingSecrets: []string{"NPM_TOKEN"}}
	got := describeSession("box", s)
	want := "auth-fix on box\n" +
		"  ID              abcd2345\n" +
		"  Agent           claude\n" +
		"  Folder          /home/dev/src/app\n" +
		"  State           working (from hooks)\n" +
		"  Since           10 Nov 2026 10:40\n" +
		"  Created         10 Nov 2026 10:40\n" +
		"  Memory cap      4.0 GiB\n" +
		"  Host helper     0.2.0\n" +
		"  Size            120×36\n" +
		"  Missing secrets NPM_TOKEN\n" +
		"  Secrets         GITHUB_TOKEN, NPM_TOKEN\n" +
		"  Attach          " + brand.Name + " attach box/abcd2345\n"
	if got != want {
		t.Errorf("show printed\n%s\nwant\n%s", got, want)
	}
	// An ended agent: the state says the exit code, so it has no line of its
	// own. The reason that the state does not say keeps its line, and so
	// does the signal.
	ended := client.Session{ID: "c3", Name: "dup", State: "errored", Source: "process", ExitCode: new(127),
		Reason: "fork/exec /no/such/agent: no such file or directory", Alive: true}
	got = describeSession("box", ended)
	if !strings.Contains(got, "State           errored (exit code 127, from process)\n") || strings.Contains(got, "Exit code") ||
		!strings.Contains(got, "Reason          fork/exec /no/such/agent: no such file or directory\n") ||
		!strings.Contains(got, "Session         still open") || !strings.HasSuffix(got, "attach box/c3\n") {
		t.Errorf("an ended agent:\n%s", got)
	}
	capped := client.Session{ID: "e5", Name: "big", State: "errored", OOM: true, Signal: "killed", ExitCode: new(137), Reason: "oom"}
	got = describeSession("box", capped)
	if !strings.Contains(got, "errored (out of memory)") || strings.Contains(got, "Reason") || !strings.Contains(got, "Signal          killed\n") ||
		!strings.Contains(got, "Exit code       137\n") {
		t.Errorf("an agent that the cap stopped:\n%s", got)
	}
	lost := client.Session{ID: "f6", Name: "gone", State: "lost", Reason: "the host restarted"}
	if got = describeSession("box", lost); strings.Contains(got, "Reason") {
		t.Errorf("a lost agent repeats its reason:\n%s", got)
	}

	withUsage := client.Session{
		ID: "u7", Name: "budgeted", Agent: "claude", State: "working",
		Usage: &api.SessionUsage{
			Total: api.TokenUsage{
				InputTokens: 100, CacheCreationTokens: 20, CacheReadTokens: 30,
				OutputTokens: 40, TotalTokens: 190, Requests: 3, EstimatedRequests: 1,
			},
			ByModel: map[string]api.TokenUsage{
				"claude-sonnet-4-6": {Model: "claude-sonnet-4-6", TotalTokens: 190, Requests: 3},
			},
			Budget: 2000000,
		},
	}
	got = describeSession("box", withUsage)
	if !strings.Contains(got, "Usage           190 of 2000000 tokens (100 in, 40 out, 30 cached, 20 cache write; 3 requests, 1 estimated)\n") {
		t.Errorf("show usage line wrong:\n%s", got)
	}
	if !strings.Contains(got, "Models          claude-sonnet-4-6: 190\n") {
		t.Errorf("show models line wrong:\n%s", got)
	}
	if strings.Contains(got, "$") {
		t.Errorf("show must not display prices (spec 17.8):\n%s", got)
	}
	obj := sessionObject("box", withUsage)
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"usage"`) || !strings.Contains(string(raw), `"total_tokens":190`) || !strings.Contains(string(raw), `"budget":2000000`) {
		t.Errorf("sessionObject JSON missing usage: %s", raw)
	}
}
