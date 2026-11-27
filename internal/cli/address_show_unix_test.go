//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if !errors.As(err, &ue) || ue.Code != "no_terminal" || ue.Kind != client.KindUsage || exitCode(ue.Kind) != 2 {
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
	pinFor(t, "cloud")
	for _, c := range []struct{ state, ssh, want, try string }{
		{"stopped", "ssh: connect to host cloud port 22: Connection refused",
			"Error: cloud is stopped, and show does not start it.", "Try: " + brand.Name + " host start cloud (an attach to one of its sessions starts it too)"},
		{"running", "cloud: Permission denied (publickey).",
			"the host refused the login (Permission denied)", "Try: ssh cloud"},
	} {
		h.Script("ssh", "case \" $* \" in *\" -G \"*) echo \"hostname cloud\"; exit 0;; esac\necho \""+c.ssh+"\" >&2; exit 255\n")
		if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "cloud", SSH: "cloud", Provider: &client.Provider{Script: provider(c.state), WakeOnAttach: true}}}); err != nil {
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
