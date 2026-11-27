package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The scripting guide and CHANGELOG keep the terminal-check-first rules for
// new and resume without --detach (decision 10, item 4).
func TestScriptingDocsDescribeTerminalCheckFirst(t *testing.T) {
	scripting, err := os.ReadFile(filepath.Join("..", "..", "docs", "how-to", "scripting.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Without a terminal and without `--detach`, `new` without a command fails with exit code 2 first",
		"`new` without a command and `resume` without `--detach` attach too, so they fail in the same way, before any lookup, and start or resume nothing.",
		"A command after `new --` runs through sandbox `exec` without a terminal and returns its status.",
		"`new --count` also starts without attaching.",
	} {
		if !strings.Contains(string(scripting), want) {
			t.Errorf("docs/how-to/scripting.md missing %q", want)
		}
	}
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`new` and `resume` without `--detach` check first too, so they start or resume nothing.",
		"`setup`, and `new --detach` for a folder that does not exist, without a terminal exited with 1 and now exit with 4.",
		"Without `--detach`, `new` fails with exit code 2 first.",
	} {
		if !strings.Contains(string(changelog), want) {
			t.Errorf("CHANGELOG.md missing %q", want)
		}
	}
}

// Hostile terminal sequences in a session's name, host, state, reason or
// signal never reach endMessage, stateLabel, endedError or describeSession
// (decision 10, addendum A2).
func TestEndMessageAndErrorsCleanHostileMetadata(t *testing.T) {
	const (
		rawName = "auth\x1bc\x1b]52;c;YWJj\x07\u202e\r\n\tfix"
		rawHost = "box\x1bc\x1b]52;c;YWJj\x07\u202e"
		rawSig  = "killed\x1bc\x1b]52;c;YWJj\x07\u202e\r\n\t"
	)
	checkClean := func(t *testing.T, where, s string) {
		t.Helper()
		for _, bad := range []string{"\x1bc", "\x1b]52", "\x1b", "\x07", "\u202e", "\t"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s leaked %q in %q", where, bad, s)
			}
		}
	}

	msg := endMessage(rawName, rawHost, "abcd2345", &proto.Ended{ExitCode: 137, Signal: rawSig})
	checkClean(t, "endMessage", msg)
	if !strings.Contains(msg, "The session authc]52;c;YWJj  fix on boxc]52;c;YWJj ended (killedc]52;c;YWJj  ).") {
		t.Errorf("endMessage = %q", msg)
	}
	checkClean(t, "endMessage(-1)", endMessage(rawName, rawHost, "abcd2345", &proto.Ended{ExitCode: -1}))

	code := 137
	sess := client.Session{
		ID: "abcd2345", Name: rawName, Agent: "claude\x1bc\u202e", Cwd: "/srv/app\x1bc\u202e",
		State: api.StateErrored, Signal: rawSig, ExitCode: &code, Reason: "boom\x1bc\u202e",
	}
	checkClean(t, "stateLabel(errored)", stateLabel(sess))
	lost := client.Session{ID: "abcd2345", Name: rawName, State: api.StateLost, Reason: "restarted\x1bc\x1b]52;c;YWJj\x07\u202e"}
	checkClean(t, "stateLabel(lost)", stateLabel(lost))

	for _, s := range []client.Session{sess, lost} {
		err := endedError(client.Host{Name: rawHost}, s)
		checkClean(t, "endedError", err.Error())
		var ue *client.UserError
		if errors.As(err, &ue) {
			checkClean(t, "endedError hint", ue.Hint)
		}
	}
	checkClean(t, "describeSession", describeSession(rawHost, sess))
}

// A long session name never pushes a trailing sessionCommand across note's
// cleaning limit to truncate box/abcd2345 into another session's name, in
// either plain stderr or --json mode (decision 11, item 6).
func TestDebugStoppedNoteKeepsSessionCommandWhole(t *testing.T) {
	captureStderr := func(t *testing.T, fn func()) string {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = w
		defer func() { os.Stderr = old }()
		fn()
		_ = w.Close()
		b, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	wantCmd := brand.Name + " debug start box/abcd2345"
	res := &client.ResumeResult{
		DebugStopped: true,
		Session:      client.Session{ID: "abcd2345", Name: strings.Repeat("x", 3899) + "\x1bc\u202e"},
	}

	plainOut := captureStderr(t, func() {
		cmd := &cobra.Command{Use: "resume"}
		addJSON(cmd, new(bool))
		debugStopped(cmd, client.Host{Name: "box"}, res)
	})
	if !strings.HasSuffix(strings.TrimSuffix(plainOut, "\n"), wantCmd) {
		t.Fatalf("plain debugStopped truncated command: %q", plainOut)
	}
	if strings.ContainsAny(plainOut, "\x1b\u202e") {
		t.Fatalf("plain debugStopped leaked control/bidi runes: %q", plainOut)
	}

	jsonOut := captureStderr(t, func() {
		cmd := &cobra.Command{Use: "resume"}
		var js bool
		addJSON(cmd, &js)
		_ = cmd.Flags().Set("json", "true")
		debugStopped(cmd, client.Host{Name: "box"}, res)
	})
	var obj struct {
		Warning struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"warning"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &obj); err != nil {
		t.Fatalf("unmarshal json warning %q: %v", jsonOut, err)
	}
	if obj.Warning.Code != "debug_stopped" || !strings.HasSuffix(obj.Warning.Message, wantCmd) {
		t.Fatalf("json debugStopped warning = %+v, want suffix %q", obj.Warning, wantCmd)
	}

	// Direct note() call with a >4000-rune message still preserves a trailing command whole.
	longNote := captureStderr(t, func() {
		note(nil, "debug_stopped", strings.Repeat("y", 4100)+" "+wantCmd)
	})
	if !strings.HasSuffix(strings.TrimSuffix(longNote, "\n"), wantCmd) {
		t.Fatalf("note() truncated trailing command: %q", longNote[max(0, len(longNote)-80):])
	}
}
