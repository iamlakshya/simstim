package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// The acceptance line of C7: `host show box --json` matches the launcher's
// details. Its text has each line of client.HostDetails, which the
// launcher's details show too (TestDetailsShowTheLinesOfHostShow, from the
// same fixture), and its JSON holds the same data: the details made from
// it are the fixture's (cli-spec 11.6).
func TestHostShowMatchesTheDetails(t *testing.T) {
	d := clienttest.HostDetails()
	var text bytes.Buffer
	printHost(&text, d, nil)
	if !strings.HasPrefix(text.String(), "box\n  Route      dev@203.0.113.7  with ~/src/infra/ssh/config\n") {
		t.Errorf("host show begins %q", text.String())
	}
	for _, l := range d.Lines() {
		if want := "  " + l.Label + strings.Repeat(" ", 11-len(l.Label)) + l.Text() + "\n"; !strings.Contains(text.String(), want) {
			t.Errorf("host show lacks %q:\n%s", want, text.String())
		}
	}
	b, err := versioned(hostShowObject(d, nil))
	if err != nil {
		t.Fatal(err)
	}
	checkJSON(t, "host show", string(b), false)
	var got struct {
		V         int
		Name, SSH string
		SSHConfig string `json:"ssh_config"`
		Tier      string
		IDChanged bool `json:"host_id_changed"`
		Labels    map[string]string
		Machine   struct {
			Provider, ID string
			IdleStop     string `json:"idle_stop"`
		} `json:"machine"`
		Secrets      []string
		Reachable    bool
		Info         client.HostInfo
		SessionCount int `json:"session_count"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	tier, _ := client.ParseTier(got.Tier)
	back := client.HostDetails{
		Host: client.Host{Name: got.Name, SSH: got.SSH, SSHConfig: got.SSHConfig, Labels: got.Labels, Secrets: got.Secrets,
			Machine: &client.Machine{Provider: got.Machine.Provider, ID: got.Machine.ID, IdleStop: got.Machine.IdleStop}},
		Tier: tier, IDChanged: got.IDChanged,
		Info: &got.Info, Sessions: got.SessionCount,
	}
	if !got.Reachable || got.V != 1 || !reflect.DeepEqual(back.Lines(), d.Lines()) {
		t.Errorf("the JSON gives other details:\n%+v\nwant\n%+v\n%s", back.Lines(), d.Lines(), b)
	}
	if strings.Contains(string(b), `"Name"`) {
		t.Errorf("host show --json has the Go keys of host ls:\n%s", b)
	}
}

// host show never writes a host's text to the terminal as it is: no
// control character, no ESC c, no OSC 52 and no bidirectional control, in
// the facts or in the reason why the host did not answer.
func TestHostShowIsClean(t *testing.T) {
	d := clienttest.HostDetails()
	evil := "\x1bc\x1b]52;c;aGk=\x07\u202e"
	d.Info.Distro += evil
	d.Info.Version += evil
	d.Info.AgentVersions["claude"] += evil
	d.Info.Agents["x"+evil] = "/x"
	var text bytes.Buffer
	printHost(&text, d, errors.New("timed out"+evil))
	if s := text.String(); strings.ContainsAny(s, "\x1b\x07\u202e") || !strings.Contains(s, "Debian 13c]52;c;aGk=") {
		t.Errorf("host show wrote %q", s)
	}
}

// host show does not wake a stopped machine: it says that it is stopped.
func TestHostShowDoesNotWake(t *testing.T) {
	m, _ := withFake(t, nil)
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	m.SetState(client.MachineStopped)
	out := ok(t, "host", "show", "box")
	if !strings.Contains(out, "  Machine    fake fake-box\n") || !strings.Contains(out, "box did not answer: box is stopped") {
		t.Errorf("host show = %q", out)
	}
	for _, c := range m.Calls() {
		if c == "start" {
			t.Error("host show started the machine")
		}
	}
}
