package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// The commands that only read a host: host show, and the look of host rm
// at a machine before it asks (docs/contributing/cli-spec.md 11.2 and
// 11.3). They never install or prune a helper, and still work for a
// client built with go install.

// withoutTheHelper saves box, a created host whose helper is not this
// client's: a dial that may install would probe, install and prune.
func withoutTheHelper(t *testing.T) *clienttest.Transport {
	t.Helper()
	_, tr := withFake(t, nil)
	tr.NoHelper, tr.Helper = true, "an-other-version"
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	return tr
}

// host show of a host without the helper: the host did not answer, with
// the fix `host upgrade`, and nothing reached the host but the dial.
func TestHostShowWithoutTheHelper(t *testing.T) {
	tr := withoutTheHelper(t)
	out, errOut, code := mainRun(t, "host", "show", "box")
	if code != 0 || !strings.Contains(out, "box did not answer: the helper "+client.HelperVersion()+" is not on box\n") ||
		errOut != "Try: "+brand.Name+" host upgrade box\n" {
		t.Errorf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	out, _, code = mainRun(t, "host", "show", "box", "--json")
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil || code != 0 || obj["reachable"] != false || obj["try"] != brand.Name+" host upgrade box" {
		t.Errorf("host show --json: exit %d, %v\n%s", code, err, out)
	}
	if changes := changesOnTheHost(tr.Remotes()); len(changes) != 0 {
		t.Errorf("host show changed the host: %q", changes)
	}
}

// The look of host rm at a host without the helper: the sessions could not
// be listed, and nothing reached the host but the dial.
func TestHostRmLooksWithoutInstalling(t *testing.T) {
	tr := withoutTheHelper(t)
	var out bytes.Buffer
	if err := removeHost(context.Background(), testPrompts("\n", &out), rmOpts{name: "box"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Its sessions could not be listed: the helper "+client.HelperVersion()+" is not on box.") {
		t.Errorf("host rm said %q", out.String())
	}
	if changes := changesOnTheHost(tr.Remotes()); len(changes) != 0 {
		t.Errorf("host rm changed the host: %q", changes)
	}
}

// A client built with go install learns its helper's version only when it
// loads its helpers. host show and the look of host rm load them first, so
// they find the helper that is on the host, and change nothing there.
func TestTheReadsOfAGoInstallClient(t *testing.T) {
	_, tr := withFake(t, nil)
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	tr.Helper = asGoInstall(t)
	out, errOut, code := mainRun(t, "host", "show", "box")
	if code != 0 || strings.Contains(out, "did not answer") || !strings.Contains(out, "  Helper     ") {
		t.Errorf("host show: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	var said bytes.Buffer
	if err := removeHost(context.Background(), testPrompts("\n", &said), rmOpts{name: "box"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said.String(), "It holds no sessions.") {
		t.Errorf("host rm said %q", said.String())
	}
	if changes := changesOnTheHost(tr.Remotes()); len(changes) != 0 {
		t.Errorf("the reads changed the host: %q", changes)
	}
}

// C7's optional display-copy utility cleans decoded keys/values and bounds
// them. Public JSON retains raw info; its actual boundary is checked below.
func TestHostShowJSONIsClean(t *testing.T) {
	d := clienttest.HostDetails()
	evil := "\x1bc\x1b]52;c;aGk=\x07\u202e\u200b"
	d.Info.Distro += evil + strings.Repeat("x", 500)
	d.Info.AgentVersions["claude"] += evil
	d.Info.Agents["x"+evil] = "/x" + evil
	d.Info.Checks = append(d.Info.Checks, client.Check{Name: "c" + evil, Detail: "d" + evil, Fix: "f" + evil})
	objForDisplay := hostShowObject(d, errors.New("e"+evil))
	objForDisplay["info"] = cleanInfo(valuesOf(d.Info))
	b, err := versioned(objForDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.ContainsAny(s, "\u202e\u200b\x1b\x07") || strings.Contains(s, `\u001b`) || strings.Contains(s, `\u0007`) {
		t.Errorf("host show --json has a hidden character:\n%s", s)
	}
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, val := range v {
				walk(k)
				walk(val)
			}
		case []any:
			for _, val := range v {
				walk(val)
			}
		case string:
			if utf8.RuneCountInString(v) > client.DetailMax {
				t.Errorf("a string of %d runes: %.40q…", utf8.RuneCountInString(v), v)
			}
		}
	}
	var obj any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	walk(obj.(map[string]any)["info"])
}

// A look that failed with a plain error, such as a timeout or a closed
// connection, has a Try: line all the same, in text and in JSON: host
// doctor, which says why the host did not answer (cli-spec 11.2).
func TestHostShowTriesHostDoctor(t *testing.T) {
	d := client.HostDetails{Host: client.Host{Name: "box", SSH: "box"}}
	want := brand.Name + " host doctor box"
	for _, err := range []error{context.DeadlineExceeded, client.ErrClosed} {
		var out, errOut bytes.Buffer
		showText(&out, &errOut, d, err)
		if !strings.Contains(out.String(), "box did not answer: ") || errOut.String() != "Try: "+want+"\n" {
			t.Errorf("%v: stdout %q, stderr %q", err, out.String(), errOut.String())
		}
		if obj := hostShowObject(d, err); obj["try"] != want || obj["reachable"] != false {
			t.Errorf("%v: --json %v", err, obj)
		}
	}
	var out, errOut bytes.Buffer
	if showText(&out, &errOut, d, nil); errOut.Len() != 0 {
		t.Errorf("a host that answered: stderr %q", errOut.String())
	}
}

// The tables of agents and of their versions share one name for each
// agent: with unequal keys, and two names that clean to one, a version
// shows beside the path of its own agent.
func TestAgentsAndVersionsShareTheirNames(t *testing.T) {
	info := map[string]any{
		"agents":         map[string]any{"agent": "/a", "agent\u200b": "/b"},
		"agent_versions": map[string]any{"agent\u200b": "2", "other": "3"},
	}
	got := cleanInfo(info)
	agents, versions := got["agents"].(map[string]any), got["agent_versions"].(map[string]any)
	if agents["agent"] != "/a" || agents["agent (2)"] != "/b" || len(agents) != 2 {
		t.Errorf("agents %v", agents)
	}
	if versions["agent (2)"] != "2" || versions["other"] != "3" || len(versions) != 2 {
		t.Errorf("agent_versions %v: a version shows under another agent", versions)
	}
}

// Keys that clean or cut to one string keep every entry, the same way on
// every run: in the order of the keys as the host sent them, each later
// one gets a suffix, within the bound (cli-spec 11.2).
func TestCleanedKeysThatCollide(t *testing.T) {
	long := strings.Repeat("k", 300)
	in := map[string]any{"agent": "/a", "agent\u200b": "/b", "agent\u202e": "/c", long + "1": "/d", long + "2": "/e"}
	want := map[string]any{"agent": "/a", "agent (2)": "/b", "agent (3)": "/c",
		strings.Repeat("k", client.DetailMax): "/d", strings.Repeat("k", client.DetailMax-4) + " (2)": "/e"}
	first, _ := json.Marshal(cleanJSON(in))
	for range 20 {
		got := cleanJSON(in).(map[string]any)
		if b, _ := json.Marshal(got); string(b) != string(first) {
			t.Fatalf("two runs differ:\n%s\n%s", first, b)
		}
		if len(got) != len(want) {
			t.Fatalf("got %d keys, want %d: %v", len(got), len(want), got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%.20q… = %v, want %v", k, got[k], v)
			}
		}
		for k := range got {
			if utf8.RuneCountInString(k) > client.DetailMax {
				t.Errorf("a key of %d runes", utf8.RuneCountInString(k))
			}
		}
	}
}

// Each removal route keeps observation read-only, including force and
// the second look after a typed answer. A missing helper never authorises
// a hidden installation as part of a removal.
func TestAllHostRmObservationPathsAvoidInstall(t *testing.T) {
	for _, mode := range []string{"keep", "destroy", "force", "typed"} {
		t.Run(mode, func(t *testing.T) {
			tr := withoutTheHelper(t)
			var out bytes.Buffer
			o := rmOpts{name: "box"}
			p := testPrompts("\n", &out)
			switch mode {
			case "destroy", "force":
				o = destroyOpts("box", mode == "force")
			case "typed":
				p = testPrompts("box\n", &out)
			}
			err := removeHost(context.Background(), p, o)
			if (mode == "destroy" || mode == "typed") && err == nil {
				t.Error("a missing helper did not stop deletion")
			}
			if changes := changesOnTheHost(tr.Remotes()); len(changes) != 0 {
				t.Errorf("%s installed or pruned a helper: %q", mode, changes)
			}
		})
	}
}
