package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// JSON may contain physical formatting newlines, but no other unsafe rune.
func assertSafeJSON(t *testing.T, text string) {
	t.Helper()
	if !json.Valid([]byte(text)) {
		t.Fatalf("invalid JSON: %q", text)
	}
	for _, r := range text {
		if r != '\n' && r != '\t' && api.Unclean(r) {
			t.Errorf("JSON holds raw unsafe rune %U: %q", r, text)
		}
	}
}

func hostileError() *proto.Error {
	return &proto.Error{Code: "busy\u009b", Message: "the host\x1b]52;c;aGk=\x07 is busy\u202e", Hint: "wait\u200b a while",
		Data: json.RawMessage(`{"why\u061c":"a\u2066b","why":"different","list":["c\udb40\udc41",3],"n":12345678901234567890,"ok":true}`)}
}

// The same error writer serves every command. It must neither emit raw
// unsafe bytes nor change decoded keys, values or large JSON numbers.
func TestJSONErrorsKeepTheHostsText(t *testing.T) {
	for _, op := range []string{"agents.check", "new"} {
		t.Run(op, func(t *testing.T) {
			_, tr := fakeBox(t, nil)
			tr.SetAgents([]api.AgentCheck{claudeCheck(api.LoginNo, "")})
			want := hostileError()
			originalData := append([]byte(nil), want.Data...)
			originalMessage, originalHint := want.Message, want.Hint
			tr.Fail(op, want)
			out, errOut, code := mainRun(t, "agents", "login", "claude", "box", "--json")
			if code != 1 || out != "" || strings.Count(errOut, "\n") != 1 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
			assertSafeJSON(t, errOut)
			var got struct {
				Error errorJSON `json:"error"`
			}
			if err := json.Unmarshal([]byte(errOut), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error.Code != want.Code || got.Error.Message != shown(want.Message) || got.Error.Try != shown(want.Hint) {
				t.Errorf("error fields changed: %+v", got.Error)
			}
			var raw, decoded any
			read := func(data []byte, v *any) {
				d := json.NewDecoder(bytes.NewReader(data))
				d.UseNumber()
				if err := d.Decode(v); err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(want.Data, originalData) || want.Message != originalMessage || want.Hint != originalHint {
				t.Fatal("original host error was mutated")
			}
			read(want.Data, &raw)
			read(got.Error.Data, &decoded)
			if !reflect.DeepEqual(raw, decoded) {
				t.Errorf("data changed: %s", got.Error.Data)
			}
			// Human output cleans a copy and keeps the error's actionable code raw.
			var human bytes.Buffer
			printError(&human, &client.UserError{Msg: want.Message, Hint: want.Hint, Code: want.Code}, nil, false, false)
			if strings.ContainsAny(human.String(), "\x1b\x07\u009b\u202e\u200b") || !strings.Contains(human.String(), "Try: wait a while") {
				t.Errorf("unsafe human error: %q", human.String())
			}
		})
	}
	t.Run("connection", func(t *testing.T) {
		_, tr := fakeBox(t, nil)
		tr.BridgeStderr = "the helper crashed\x1b]52;c;aGk=\x07 at \u009b2J\u202e\nline two"
		_, errOut, code := mainRun(t, "new", "box", "--detach", "--json")
		assertSafeJSON(t, errOut)
		var got struct {
			Error errorJSON `json:"error"`
		}
		_ = json.Unmarshal([]byte(errOut), &got)
		if code != 1 || got.Error.Message != "the helper on box did not start: "+client.CleanBlock(tr.BridgeStderr) {
			t.Errorf("connection error changed: %s", errOut)
		}
	})
}

func TestNotesCleanHumanTextAndEscapeJSON(t *testing.T) {
	msg := "path\x1b]52;c;value\x07\u009b\u202e\nStarted box/owner123"
	cmd := newCmd()
	_ = cmd.Flags().Set("json", "true")
	_, errOut := captureOutput(t, func() { note(cmd, "host_note", msg) })
	assertSafeJSON(t, errOut)
	var got struct {
		Warning struct {
			Message string `json:"message"`
		} `json:"warning"`
	}
	_ = json.Unmarshal([]byte(errOut), &got)
	if got.Warning.Message != msg {
		t.Errorf("note changed: %q", got.Warning.Message)
	}
	_, human := captureOutput(t, func() { note(nil, "host_note", msg) })
	if human != shown(msg)+"\n" {
		t.Errorf("human note = %q", human)
	}
}

func TestValidJSONData(t *testing.T) {
	data := json.RawMessage(`[{"k\u202e":"v\u009b","n":1e400},"\u200b",null]`)
	if string(validJSONData(data)) != string(data) {
		t.Fatal("raw data changed")
	}
	for _, bad := range []string{"", "{", "1 2"} {
		if validJSONData(json.RawMessage(bad)) != nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// Every session JSON output keeps raw values, while ls draws only copies.
func TestSessionOutputsPreserveJSONAndCleanHumanCopies(t *testing.T) {
	row := client.Session{ID: "legacyid", Name: "name\u200b\x1b]52;c;value\a", Agent: "custom\u202e", Cwd: "/work/\u2066repo", Reason: "busy\u009b2J", State: api.StateIdle, CreatedAt: time.Now()}
	for _, args := range [][]string{{"ls", "--json"}, {"new", "box", "--detach", "--wall", "none", "--downgrade", "--json", "--name", row.Name, "--agent", row.Agent, "--dir", row.Cwd, "--", "true"}} {
		_, tr := fakeBox(t, []client.Session{row})
		tr.NewID = row.ID
		out, errOut, code := mainRun(t, args...)
		if code != 0 {
			t.Fatalf("%v: %d, %s", args, code, errOut)
		}
		assertSafeJSON(t, out)
		var obj map[string]any
		if args[0] == "ls" {
			var rows []struct {
				Sessions []map[string]any `json:"sessions"`
			}
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatal(err)
			}
			obj = rows[0].Sessions[0]
			if obj["cwd"] != row.Cwd || obj["reason"] != row.Reason {
				t.Fatalf("list fields changed: %s", out)
			}
		} else {
			if err := json.Unmarshal([]byte(out), &obj); err != nil {
				t.Fatal(err)
			}
		}
		if obj["name"] != row.Name || obj["agent"] != row.Agent || obj["id"] != row.ID {
			t.Fatalf("decoded fields changed: %s", out)
		}

	}
	fakeBox(t, []client.Session{row, {ID: "legacyid\u200b", Name: "invalid raw ID", State: api.StateIdle}})
	human, _, code := mainRun(t, "ls")
	if code != 0 || strings.ContainsAny(human, "\x1b\a\u200b\u202e\u2066\u009b") || strings.Contains(human, "invalid raw ID") {
		t.Fatalf("unsafe human ls: %d, %q", code, human)
	}
	if row.Name != "name\u200b\x1b]52;c;value\a" {
		t.Fatal("human output mutated source")
	}
}

func TestJSONPayloadRoundTripAllUnsafeClasses(t *testing.T) {
	text := "\x00\x1b\x7f\u0085\u009b\u00ad\u061c\u200b\u202e\u2028\u2029\u2066\ufe0f\U000E0041"
	payload := map[string]any{"key": text, "key\u200b": "different", "number": json.Number("12345678901234567890"), "nested": []any{text, true}}
	b, err := versioned(map[string]any{"payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	assertSafeJSON(t, string(b))
	var got map[string]any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["payload"], payload) {
		t.Fatalf("JSON values changed: %s", b)
	}
}
