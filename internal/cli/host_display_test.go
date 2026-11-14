package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

const hostileDisplay = "host\x1b]52;c;value\a\u009b\u202e\u200b\nnext"

func noUnsafeDisplay(t *testing.T, s string) {
	t.Helper()
	if strings.ContainsAny(s, "\x1b\a\u009b\u202e\u200b") {
		t.Fatalf("unsafe display bytes: %q", s)
	}
	if !strings.Contains(s, "host]52;c;value next") {
		t.Fatalf("host display missing: %q", s)
	}
}

func TestDoctorFixAndScrollbackDisplayCopies(t *testing.T) {
	h, tr := fakeBox(t, []client.Session{{ID: "s1", Name: hostileDisplay, Agent: hostileDisplay, Alive: true, FullScreen: true}})
	check := api.Check{Name: hostileDisplay, Detail: hostileDisplay, Fix: hostileDisplay}
	info := api.HostInfo{Distro: hostileDisplay, OS: hostileDisplay, Arch: hostileDisplay, Version: hostileDisplay, Checks: []api.Check{check}}
	fix := api.Fix{ID: "keep_raw", Problem: hostileDisplay, Action: hostileDisplay, Command: hostileDisplay, Error: hostileDisplay}
	tr.Replies = map[string]any{"doctor": info, "setup": api.SetupResult{Steps: []api.Check{check}}, "doctor.fix": []api.Fix{fix}}
	// Host doctor covers its header, all check fields and scrollback text.
	out, errOut, code := mainRun(t, "host", "doctor", h.Name)
	if code != 0 {
		t.Fatalf("doctor %d: %s", code, errOut)
	}
	noUnsafeDisplay(t, out+errOut)
	// connectHost prints setup steps and doctor fields before saving the host.
	var connect bytes.Buffer
	was, err := client.SavedEntry(h.Name)
	if err != nil {
		t.Fatal(err)
	}
	_, err = connectHost(t.Context(), &connect, client.HostChange{Kind: "display test", Was: was, Now: &h, Pin: true, Tier: client.TierOwn}, nil,
		func(string) (bool, error) { return true, nil }, &onboard.Log{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	noUnsafeDisplay(t, connect.String())
	// offerFixes must use raw IDs for actions but only safe display copies.
	var output bytes.Buffer
	p := &prompter{ctx: context.Background(), out: &output}
	before := fix
	manual := fix
	manual.Manual = true
	err = offerFixes(p, qHostFix, []api.Fix{fix, manual}, true, func(ids []string) ([]api.Fix, error) {
		if !reflect.DeepEqual(ids, []string{"keep_raw"}) {
			t.Fatalf("changed IDs %q", ids)
		}
		done := fix
		done.Done = true
		done.Error = ""
		return []api.Fix{fix, done}, nil
	})
	if err == nil {
		t.Fatal("a failed fix did not fail")
	}
	noUnsafeDisplay(t, output.String())
	if !reflect.DeepEqual(fix, before) || !reflect.DeepEqual(info.Checks[0], check) {
		t.Fatal("display mutated raw source")
	}
}

func TestRestoreAndResumeRejectOrDisplayRawIDsSafely(t *testing.T) {
	_, tr := fakeBox(t, []client.Session{{ID: "s1", Name: "app", Agent: "shell", State: api.StateExited}})
	tr.Features = []string{"resume"}
	tr.Replies = map[string]any{"restore": []api.RestoreResult{{ID: "s1\u200b", Name: hostileDisplay, Action: hostileDisplay, Why: hostileDisplay}}}
	out, errOut, code := mainRun(t, "host", "restore", "box")
	if code != 0 {
		t.Fatalf("restore %d: %s", code, errOut)
	}
	noUnsafeDisplay(t, out)
	if !strings.Contains(out, "<invalid>") {
		t.Fatalf("invalid ID cleaned into another ID: %s", out)
	}
	for _, id := range []string{"s1", "s1\u200b"} {
		response := api.ResumeResult{Session: client.Session{ID: id, Name: hostileDisplay, Agent: hostileDisplay}, Why: hostileDisplay}
		tr.Replies["resume"] = response
		out, errOut, code = mainRun(t, "resume", "box/s1", "--detach")
		if id != "s1" {
			if code != 1 || out != "" || !strings.Contains(errOut, "session ID") {
				t.Fatalf("bad resumed ID: %d %q %q", code, out, errOut)
			}
		} else {
			if code != 0 {
				t.Fatalf("resume %d: %s", code, errOut)
			}
			noUnsafeDisplay(t, errOut)
		}
	}
	beforeOps := len(tr.Ops())
	tr.SetSessions([]client.Session{{ID: "s1\u200b", Name: "app", State: api.StateExited}})
	_, errOut, code = mainRun(t, "resume", "box/app", "--detach")
	if code != 1 || !strings.Contains(errOut, "session ID") {
		t.Fatalf("bad request ID: %d, %s", code, errOut)
	}
	if slices.Contains(tr.Ops()[beforeOps:], "resume") {
		t.Fatal("invalid raw ID reached resume request")
	}
}

func TestGuideUsesConfiguredDetachKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := client.ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[attach]\ndetach_key = 'ctrl+]'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	f := onboard.NewFlow("box", nil, nil)
	_ = f.Choose("claude")
	a := f.Next()
	stop := errors.New("stop after instructions")
	g := &guide{ctx: context.Background(), tty: true, out: &out, session: func(client.Host, onboard.Action) (string, bool, error) { return "", false, stop }}
	if err := g.inSession(client.Host{Name: "box"}, f, a); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ctrl+] leaves it running") || strings.Contains(out.String(), "ctrl+\\ leaves") {
		t.Fatalf("wrong detach key: %q", out.String())
	}
}

func TestHostDoctorJSONKeepsRawPayload(t *testing.T) {
	_, tr := fakeBox(t, nil)
	tr.Replies = map[string]any{"doctor": api.HostInfo{Distro: hostileDisplay, Checks: []api.Check{{Name: hostileDisplay, Detail: hostileDisplay}}}}
	out, errOut, code := mainRun(t, "host", "doctor", "box", "--json")
	if code != 0 {
		t.Fatalf("%d: %s", code, errOut)
	}
	assertSafeJSON(t, out)
	var got api.HostInfo
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Distro != hostileDisplay || got.Checks[0].Detail != hostileDisplay {
		t.Fatal("doctor JSON changed values")
	}
}

func TestCancellationLogUsesSafeDisplayCopies(t *testing.T) {
	log := &onboard.Log{}
	log.Add("changed %s", hostileDisplay)
	before := log.Lines()[0]
	var out bytes.Buffer
	printError(&out, cancelled(log.Lines()...), nil, false, false)
	noUnsafeDisplay(t, out.String())
	if log.Lines()[0] != before {
		t.Fatal("display changed the log")
	}
}

func TestPullSummaryCleansHostFields(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The transport and git are local fixtures. No host is contacted.
	word := strings.ReplaceAll(hostileDisplay, "\n", "")
	ssh := "#!/bin/sh\nprintf '%s\\n' '/work/repo' '" + word + "' '1'\n"
	git := "#!/bin/sh\ncase \"$1\" in\nsymbolic-ref) echo main;;\nrev-list) printf '%s\\n' '" + word + "';;\nesac\n"
	for name, script := range map[string]string{"ssh": ssh, "git": git} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := client.Host{Name: "box", SSH: "box"}
	s := client.Session{ID: "s1", Name: hostileDisplay, Cwd: "/work/repo"}
	var err error
	_, out := captureOutput(t, func() { err = pull(context.Background(), h, &s, "review", false) })
	if err != nil {
		t.Fatal(err)
	}
	noUnsafeDisplay(t, out)
	if s.Name != hostileDisplay {
		t.Fatal("display changed session name")
	}
}
