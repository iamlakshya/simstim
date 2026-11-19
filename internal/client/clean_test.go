package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/term"
)

// dirty is text that a host could send: an OSC 52 write to the clipboard,
// a bidirectional override, a zero-width space and a tag character.
const dirty = "\x1b]52;c;aGk=\x07ok\u202e\u200b\U000e0041"

// tidy is dirty, cleaned.
const tidy = "]52;c;aGk=ok"

// TestCleanSession checks every string of a session row, and that the
// cleaned row shares no list with the host's.
func TestCleanSession(t *testing.T) {
	in := Session{ID: dirty, Name: dirty, Agent: dirty, Cwd: dirty, State: dirty, Source: dirty, Reason: dirty,
		Signal: dirty, Unit: dirty, HostVersion: dirty, MissingSecrets: []string{dirty}, Secrets: []string{dirty}}
	got := CleanSession(in)
	want := Session{ID: tidy, Name: tidy, Agent: tidy, Cwd: tidy, State: tidy, Source: tidy, Reason: tidy,
		Signal: tidy, Unit: tidy, HostVersion: tidy, MissingSecrets: []string{tidy}, Secrets: []string{tidy}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CleanSession = %+v, want %+v", got, want)
	}
	if in.MissingSecrets[0] != dirty || in.Secrets[0] != dirty {
		t.Fatal("CleanSession changed the host's own row")
	}
	// Every string field: a new one must be cleaned too.
	v := reflect.ValueOf(got)
	for i := range v.NumField() {
		if f := v.Field(i); f.Kind() == reflect.String && f.String() != tidy {
			t.Errorf("CleanSession leaves %s as %q", v.Type().Field(i).Name, f.String())
		}
	}
	if CleanSessions(nil) != nil || len(CleanSessions([]Session{in})) != 1 {
		t.Error("CleanSessions lost or made rows")
	}
}

// TestCleanSessionKeepsCleanRows checks that a clean row comes back as it is.
func TestCleanSessionKeepsCleanRows(t *testing.T) {
	in := Session{ID: "s1", Name: "auth-fix", Agent: "claude", Cwd: "/home/dev/src/café", State: api.StateNeedsInput,
		Reason: api.ReasonChildOOM, MissingSecrets: []string{"GH_TOKEN"}}
	if got := CleanSession(in); !reflect.DeepEqual(got, in) {
		t.Fatalf("CleanSession changed a clean row: %+v", got)
	}
}

func TestCleanHostInfo(t *testing.T) {
	in := HostInfo{Hostname: dirty, OS: dirty, Arch: dirty, Kernel: dirty, Distro: dirty, Version: dirty, User: dirty,
		Agents:        map[string]string{"claude" + dirty: dirty},
		AgentVersions: map[string]string{"claude": dirty},
		Checks:        []api.Check{{Name: dirty, OK: true, Detail: dirty, Fix: dirty}},
		AgentChecks:   []api.AgentCheck{{Kind: dirty, Title: dirty, Support: dirty, Path: dirty, Login: dirty, LoginHow: dirty, Installed: true}},
	}
	got := CleanHostInfo(in)
	want := HostInfo{Hostname: tidy, OS: tidy, Arch: tidy, Kernel: tidy, Distro: tidy, Version: tidy, User: tidy,
		Agents:        map[string]string{"claude" + tidy: tidy},
		AgentVersions: map[string]string{"claude": tidy},
		Checks:        []api.Check{{Name: tidy, OK: true, Detail: tidy, Fix: tidy}},
		AgentChecks:   []api.AgentCheck{{Kind: tidy, Title: tidy, Support: tidy, Path: tidy, Login: tidy, LoginHow: tidy, Installed: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CleanHostInfo = %+v, want %+v", got, want)
	}
	if in.Checks[0].Name != dirty || in.Agents["claude"+dirty] != dirty {
		t.Fatal("CleanHostInfo changed the host's own report")
	}
}

// TestOfferFolders checks that a folder suggestion goes back to the host
// only when it is the folder that the user sees.
func TestOfferFolders(t *testing.T) {
	got := OfferFolders(FoldersResult{
		Matches: []string{"~/src/api/", "~/src/mi\u200cnd/", "~/a" + dirty},
		Recent:  []string{"~/src/app", "~/x\x1b[2J"},
		Repos:   []string{"~/src/caf\u00e9", "~/src/\u202eppa"},
	})
	want := FoldersResult{Matches: []string{"~/src/api/"}, Recent: []string{"~/src/app"}, Repos: []string{"~/src/caf\u00e9"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OfferFolders = %+v, want %+v", got, want)
	}
}

// TestShowPath checks how a question shows a path from the host.
func TestShowPath(t *testing.T) {
	for in, want := range map[string]string{
		"/home/dev/src/app":           "/home/dev/src/app",
		"/home/dev/src/caf\u00e9":     "/home/dev/src/caf\u00e9",
		"/srv/app\u200b":              `"/srv/app\u200b"`,
		"/srv/a\x1b]52;c;aGk=\x07pp":  `"/srv/a\x1b]52;c;aGk=\app"`,
		"/srv/\u202eppa.sh\U000e0041": `"/srv/\u202eppa.sh\U000e0041"`,
	} {
		got := ShowPath(in)
		if got != want {
			t.Errorf("ShowPath(%q) = %s, want %s", in, got, want)
		}
		for _, r := range got {
			if r < 0x20 || r >= 0x7f && in != "/home/dev/src/caf\u00e9" {
				t.Errorf("ShowPath(%q) = %q, which holds %U", in, got, r)
			}
		}
	}
}

// TestOSCText checks the text of a notification: cleaned, and with no
// field separator.
func TestOSCText(t *testing.T) {
	if got, want := OSCText("a;b"+dirty), "a b]52 c aGk=ok"; got != want {
		t.Fatalf("OSCText = %q, want %q", got, want)
	}
}

// TestValidSessions checks that a row whose ID is not a session ID is
// dropped, and that the others stay as the host sent them.
func TestValidSessions(t *testing.T) {
	in := []Session{{ID: "abc2"}, {ID: "s1"}, {ID: "ab\u200bc"}, {ID: "xyz", Name: dirty}, {ID: ""}}
	got := ValidSessions("box", in)
	if len(got) != 2 || got[0].ID != "abc2" || got[1].ID != "xyz" || got[1].Name != dirty {
		t.Fatalf("ValidSessions = %+v", got)
	}
	if got := ValidSessions("box", []Session{}); got == nil || len(got) != 0 {
		t.Fatalf("ValidSessions of an empty list = %#v, want an empty list", got)
	}
	if got := ValidSessions("box", nil); got != nil {
		t.Fatalf("ValidSessions(nil) = %#v", got)
	}
}

// TestPlatformOf checks the probe's reading of uname: known platforms by
// their Go names, and anything else cleaned and cut short.
func TestPlatformOf(t *testing.T) {
	for _, c := range []struct{ system, machine, goos, arch string }{
		{"Linux", "x86_64", "linux", "amd64"},
		{"Linux", "aarch64", "linux", "arm64"},
		{"Darwin", "arm64", "darwin", "arm64"},
		{"Linux", "riscv64", "linux", "riscv64"},
		{"Linux", "x\x1b]52;c;aGk=\x07" + strings.Repeat("y", 50), "linux", "x]52;c;aGk=" + strings.Repeat("y", 21)},
		{"Ev\u202eil", "x86_64", "evil", "amd64"},
	} {
		goos, arch := platformOf(c.system, c.machine)
		if goos != c.goos || arch != c.arch {
			t.Errorf("platformOf(%q, %q) = %q, %q, want %q, %q", c.system, c.machine, goos, arch, c.goos, c.arch)
		}
	}
}

// TestCleanCuts checks the limits: a line is shorter than a path.
func TestCleanCuts(t *testing.T) {
	long := strings.Repeat("a", 5000)
	if n := len(CleanLine(long)); n != textMax {
		t.Errorf("a line keeps %d runes, want %d", n, textMax)
	}
	if n := len(CleanPath(long)); n != pathMax {
		t.Errorf("a path keeps %d runes, want %d", n, pathMax)
	}
}

// TestHostErrorIsClean checks the error in a host's answer: its message and
// hint are cleaned to draw, and its code and data stay for the client.
func TestHostErrorIsClean(t *testing.T) {
	data := json.RawMessage(`{"repo":"x"}`)
	got := hostError(&proto.Error{Code: "setup_ask", Message: "no\x1b[2J" + dirty, Hint: dirty, Data: data})
	if got.Msg != "no[2J"+tidy || got.Hint != tidy || got.Code != "setup_ask" || string(got.Data) != string(data) {
		t.Fatalf("hostError = %+v", got)
	}
}

// TestStderrKeepsItsLines checks that the end of a helper's stderr keeps
// its last three lines, each one cleaned.
func TestStderrKeepsItsLines(t *testing.T) {
	if got, want := tailStderr("zero\none"+dirty+"\ntwo\r\n\x1b[31mthree\n"), "one"+tidy+"\ntwo \n[31mthree"; got != want {
		t.Fatalf("tailStderr = %q, want %q", got, want)
	}
}

// TestSSHErrorIsClean checks ssh's stderr in an error.
func TestSSHErrorIsClean(t *testing.T) {
	err := sshError(Host{Name: "box", SSH: "box"}, errors.New("exit status 1"), "warning\nfatal: \x1b]52;c;aGk=\x07no\u202e")
	if got := err.Error(); got != "exit status 1: warning\nfatal: ]52;c;aGk=no" {
		t.Fatalf("sshError = %q", got)
	}
}

// TestResumeSecretsErrorIsClean checks a resume's errors, which name the
// session: the host's own text.
func TestResumeSecretsErrorIsClean(t *testing.T) {
	_, err := ResumeSecrets(Host{Name: "box"}, Session{ID: "abc", Name: "app" + dirty, Secrets: []string{"GH_TOKEN\u200b"}}, nil)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07\u202e\u200b") || !strings.Contains(err.Error(), `"GH_TOKEN\u200b"`) {
		t.Fatalf("ResumeSecrets = %v", err)
	}
	_, err = ResumeSecrets(Host{Name: "box"}, Session{ID: "abc", Name: "app" + dirty, Secrets: []string{"OTHER"}}, nil)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07\u202e\u200b") {
		t.Fatalf("ResumeSecrets = %v", err)
	}
}

// TestAttachEndIsClean checks the end of an attach: the output frames
// before it reach the terminal byte for byte, and the signal in the end
// frame, which the notice after the attach shows, is cleaned.
func TestAttachEndIsClean(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := &attacher{o: AttachOptions{Out: f}, tracker: term.NewTracker()}
	// The agent's own bytes, hostile ones too, pass unchanged.
	out := []byte("ok \x1b]52;c;aGk=\x07 \xe2\x80\xae \xc2\x9b2J \x1bc done\r\n")
	if _, err := a.frame(proto.Frame{Type: proto.TOutput, Payload: proto.EncodeOutput(0, out)}); err != nil {
		t.Fatal(err)
	}
	ended, _ := json.Marshal(proto.Ended{ExitCode: 137, Signal: "killed\x1b]52;c;aGk=\x07\u202e"})
	done, err := a.frame(proto.Frame{Type: proto.TEnded, Payload: ended})
	if !done || err != nil {
		t.Fatalf("the end frame = %v, %v", done, err)
	}
	if a.res.Ended == nil || a.res.Ended.Signal != "killed]52;c;aGk=" || a.res.Ended.ExitCode != 137 {
		t.Fatalf("ended = %+v", a.res.Ended)
	}
	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, out) {
		t.Fatalf("the terminal got %q, want the output unchanged: %q", got, out)
	}
}

// TestCommandTargetIsInert checks the target of a command that the client
// prints or copies: pasted into a shell, it is one word, and runs nothing.
func TestCommandTargetIsInert(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the check runs a POSIX shell")
	}
	if got := CommandTarget("box", "andvnnie"); got != "box/andvnnie" {
		t.Errorf("a valid ID = %q, want it as it is", got)
	}
	dir := t.TempDir()
	for _, id := range []string{"$(touch owned)", "`touch owned`", "a;touch owned", "a && touch owned", "a | touch owned",
		"a\ntouch owned", "it's'; touch owned; '", "$(touch${IFS}owned)", "a\x1b]52;c;aGk=\x07"} {
		target := CommandTarget("box", id)
		cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "set -- "+target+`; printf %s "$1"`)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("sh with %s: %v", target, err)
		}
		if want := CleanLine("box/" + id); string(out) != want || strings.Contains(target, "\x1b") {
			t.Errorf("CommandTarget(%q) = %s, which the shell reads as %q, want %q", id, target, out, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "owned")); err == nil {
			t.Fatalf("CommandTarget(%q) = %s ran a command", id, target)
		}
	}
}

// TestCleanWriter checks what the user sees of a host's stderr: each line
// cleaned with its break kept, a prompt with no break at once, and a
// character that a write cut in two whole.
func TestCleanWriter(t *testing.T) {
	var b bytes.Buffer
	w := &cleanWriter{w: &b}
	for _, p := range []string{"ok \x1b", "c\x1b]52;c;aGk=\x07 bi\xe2\x80", "\xaedi z\u200bw\n", "pro\xe2", "\x86\x92mpt: "} {
		if n, err := w.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if got, want := b.String(), "ok c]52;c;aGk= bidi zw\npro→mpt: "; got != want {
		t.Fatalf("before Flush: %q, want %q, with the prompt shown at once", got, want)
	}
	if _, err := w.Write([]byte("end\xe2\x80")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if got, want := b.String(), "ok c]52;c;aGk= bidi zw\npro→mpt: end\ufffd\ufffd"; got != want {
		t.Fatalf("after Flush: %q, want %q", got, want)
	}
}

// TestParseRepoInfoEscapesHostText checks pull's reading of the host's
// answer: when it cannot read it, the error shows the answer escaped to
// ASCII, with a step to try. Go's %q would keep U+034F, which hides.
func TestParseRepoInfoEscapesHostText(t *testing.T) {
	for _, out := range []string{"/r\nfeature\u034f\nnot-a-number\u034f\n", "x\u034f\x1b]52;c;aGk=\x07"} {
		_, err := parseRepoInfo(out)
		var ue *UserError
		if !errors.As(err, &ue) || ue.Hint == "" {
			t.Fatalf("parseRepoInfo(%q) = %v, want a user error with a step", out, err)
		}
		for _, r := range ue.Msg {
			if r < 0x20 || r > 0x7e {
				t.Errorf("parseRepoInfo(%q) = %q, which holds %U", out, ue.Msg, r)
			}
		}
	}
}

// TestReconnectTitleIsClean checks the title that an attach writes while
// the link is down: it holds the session's ID, which a host sent, cleaned.
// The output frames before it still reach the terminal byte for byte.
func TestReconnectTitleIsClean(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := &attacher{o: AttachOptions{Out: f}, tracker: term.NewTracker(), key: "box/x\x07\x1b]52;c;aGk=\x07\u202e"}
	out := []byte("agent \x1b]2;its own title\x07 \x1b]52;c;aGk=\x07 \xc2\x9b2J\r\n")
	if _, err := a.frame(proto.Frame{Type: proto.TOutput, Payload: proto.EncodeOutput(0, out)}); err != nil {
		t.Fatal(err)
	}
	a.write(a.reconnectTitle())
	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, out) {
		t.Fatalf("the terminal got %q, want the output unchanged first: %q", got, out)
	}
	title := "\x1b]9;4;3\x1b\\\x1b[22;0t\x1b]2;⧗ reconnecting · box/x]52;c;aGk=\x1b\\"
	if string(got[len(out):]) != title {
		t.Fatalf("the title = %q, want %q", got[len(out):], title)
	}
}
