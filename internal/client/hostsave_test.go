package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The compare and swap of a save or a forget (docs/contributing/cli-spec.md
// 11.3, rule 13).

// ownHosts gives the test its own hosts.toml and state folder, with hosts
// saved as a hand edit saves them.
func ownHosts(t *testing.T, hosts ...client.Host) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ownState(t)
	if err := client.SaveHosts(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
}

// A change goes ahead only while the saved entry is still the one that its
// command read: every other entry, or none, is host_changed, which changes
// nothing, says what happened and how to look, and exits with 1.
func TestUpdateHostsComparesTheEntry(t *testing.T) {
	made := client.Host{Name: "box", Machine: &client.Machine{Provider: "lima", ID: "box", Created: "1"}}
	again := client.Host{Name: "box", Machine: &client.Machine{Provider: "lima", ID: "box", Created: "2"}}
	labelled := made
	labelled.Labels = map[string]string{"k": "v"}
	look, ls := brand.Name+" host show box (then run the command again)", brand.Name+" host ls (then run the command again)"
	for _, c := range []struct {
		name   string
		saved  []client.Host
		change client.HostChange
		msg    string
		hint   string
	}{
		{"a new host", nil, client.HostChange{Kind: "host add", Now: &made}, "", ""},
		{"a new host that another added", []client.Host{again}, client.HostChange{Kind: "host add", Now: &made},
			"another command added a host named box while host add ran, so this one was not saved", brand.Name + " host show box (or pick another name)"},
		{"a save of a host removed", nil, client.HostChange{Kind: "setup", Was: &made, Now: &made},
			"box was removed while setup ran, so it was not saved", ls},
		{"a save of a host made again", []client.Host{again}, client.HostChange{Kind: "the launcher", Was: &made, Now: &made},
			"box was made again while the launcher ran, so it was not saved", look},
		{"a save of a host changed", []client.Host{labelled}, client.HostChange{Kind: "host set", Was: &made, Now: &made},
			"box changed while host set ran, so it was not saved", look},
		{"a forget of a host made again", []client.Host{again}, client.HostChange{Kind: "the launcher", Was: &made},
			"box was made again while the launcher ran, so it was not forgotten", look},
		{"a forget of the same host", []client.Host{made}, client.HostChange{Kind: "host rm", Was: &made}, "", ""},
	} {
		ownHosts(t, c.saved...)
		err := client.UpdateHosts(context.Background(), c.change)
		hosts, _ := client.LoadHosts()
		if c.msg == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		code := "host_changed"
		if c.change.Was == nil {
			code = "host_exists"
		}
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != code || ue.Msg != c.msg || ue.Hint != c.hint || ue.Kind != client.KindFailed {
			t.Errorf("%s: %+v", c.name, err)
		}
		if len(hosts) != len(c.saved) || len(hosts) == 1 && !client.SameEntry(hosts[0], c.saved[0]) {
			t.Errorf("%s: the refused change changed the hosts to %+v", c.name, hosts)
		}
	}
}

// An empty table or list counts the same as none, as the save of
// hosts.toml writes neither: a command that read one saves.
func TestUpdateHostsCountsEmptyAsNone(t *testing.T) {
	read := client.Host{Name: "mine", SSH: "mine", Labels: map[string]string{}, Secrets: []string{}}
	ownHosts(t, client.Host{Name: "mine", SSH: "mine"})
	now := client.Host{Name: "mine", SSH: "me@example.com"}
	if err := client.UpdateHosts(context.Background(), client.HostChange{Kind: "host set", Was: &read, Now: &now}); err != nil {
		t.Fatal(err)
	}
	if h, _ := client.FindHost("mine"); h.SSH != "me@example.com" {
		t.Errorf("the host is %+v", h)
	}
}

// A save takes the lock of the host for its moment: while another command
// holds it, such as host rm at its question, the save fails with
// host_busy, and changes nothing. A save in the context that holds it
// goes ahead.
func TestUpdateHostsTakesTheLockOfTheHost(t *testing.T) {
	ownHosts(t, client.Host{Name: "box", SSH: "box"})
	ctx, unlock, err := client.LockHost(context.Background(), "box", "host rm")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	was := client.Host{Name: "box", SSH: "box"}
	err = client.UpdateHosts(context.Background(), client.HostChange{Kind: "the launcher", Was: &was})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_busy" || ue.Msg != "box is busy: host rm changes it" {
		t.Fatalf("a forget while host rm holds the lock: %+v", err)
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Fatal("the busy host was forgotten")
	}
	if err := client.UpdateHosts(ctx, client.HostChange{Kind: "host rm", Was: &was}); err != nil {
		t.Fatalf("the forget of the command that holds the lock: %v", err)
	}
}

// Two commands that change two hosts at once lose no change: each read,
// change and save of hosts.toml holds the lock of the file, so the second
// reads what the first saved.
func TestUpdateHostsLosesNoChange(t *testing.T) {
	box := client.Host{Name: "box", SSH: "box"}
	ownHosts(t, box)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer client.SetBeforeHostsSave(func() {
		once.Do(func() {
			close(started)
			<-release
		})
	})()
	errs := make(chan error, 2)
	other := client.Host{Name: "other", SSH: "other"}
	go func() {
		errs <- client.UpdateHosts(context.Background(), client.HostChange{Kind: "host add", Now: &other})
	}()
	<-started
	go func() {
		errs <- client.UpdateHosts(context.Background(), client.HostChange{Kind: "host rm", Was: &box})
	}()
	time.Sleep(200 * time.Millisecond) // the forget reads now, or waits for the lock
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	hosts, err := client.LoadHosts()
	if err != nil || len(hosts) != 1 || hosts[0].Name != "other" {
		t.Errorf("the hosts are %+v, %v: one change was lost", hosts, err)
	}
}

// A hosts.toml that does not decode keeps its own error, with its line and
// column: the change does not call it a change of the host.
func TestUpdateHostsKeepsADecodeError(t *testing.T) {
	ownHosts(t)
	path := filepath.Join(client.ConfigDir(), "hosts.toml")
	if err := os.WriteFile(path, []byte("[[host]]\nname = \"box\"\n[[host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	was := client.Host{Name: "box"}
	err := client.UpdateHosts(context.Background(), client.HostChange{Kind: "setup", Was: &was, Now: &was})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "bad_config" || !strings.Contains(ue.Msg, ", line 4, column 7: ") {
		t.Fatalf("err = %+v", err)
	}
}
