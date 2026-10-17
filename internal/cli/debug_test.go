package cli

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// startRecord reads the run's start record: the first line of a debug file.
func startRecord(t *testing.T, path string) diag.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var r diag.Record
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &r) != nil || r.Ev != diag.EvStart {
		t.Fatalf("%s does not start with a start record", path)
	}
	return r
}

// stopClientDebug ends the debug that a command turned on, as Main does.
func stopClientDebug(t *testing.T) string {
	t.Helper()
	f := client.DebugFile()
	if f == nil {
		t.Fatal("debug is off")
	}
	client.StopDebug(io.Discard)
	return f.Path()
}

// --debug alone turns on every category, and the word after it stays an
// argument. A list binds only in the = form. --debug-file names the file
// and wins over the variable, which turns on every category by itself.
func TestTheDebugFlags(t *testing.T) {
	newTestHost(t)
	ok(t, "--debug", "ls")
	path := stopClientDebug(t)
	if r := startRecord(t, path); r.Cats != diag.CatList || r.Side != "client" {
		t.Errorf("--debug alone: %+v", r)
	}
	if filepath.Dir(path) != client.DebugDir() || !strings.HasPrefix(filepath.Base(path), "client-") {
		t.Errorf("the debug file is %s", path)
	}
	ok(t, "--debug=net,attach", "ls")
	if r := startRecord(t, stopClientDebug(t)); r.Cats != "attach,net" {
		t.Errorf("--debug=net,attach records %s", r.Cats)
	}
	failsWithHint(t, "--debug: no category disk", "= form", "--debug=disk", "ls")

	env := filepath.Join(t.TempDir(), "env.jsonl")
	t.Setenv(diag.EnvFile, env)
	ok(t, "ls")
	if p := stopClientDebug(t); p != env || startRecord(t, p).Cats != diag.CatList {
		t.Errorf("the variable wrote %s", p)
	}
	file := filepath.Join(t.TempDir(), "flag.jsonl")
	ok(t, "--debug=memory", "--debug-file", file, "ls")
	if p := stopClientDebug(t); p != file || startRecord(t, p).Cats != "memory" {
		t.Errorf("--debug-file wrote %s", p)
	}
	t.Setenv(diag.EnvFile, "")
	ok(t, "ls")
	if client.DebugFile() != nil {
		t.Error("debug is on with no flag and no variable")
	}
}

// The shell completes a list of categories after its last comma.
func TestCompleteCategories(t *testing.T) {
	got, _ := completeCats(diag.HostCats)(nil, nil, "memory,")
	if strings.Join(got, " ") != "memory,attach memory,runtime" {
		t.Errorf("completions = %v", got)
	}
	got, _ = completeCats(diag.AllCats)(nil, nil, "")
	if len(got) != 5 {
		t.Errorf("completions = %v", got)
	}
}

// debug start, save, profile and stop work on a running session from
// another terminal, and refuse what they cannot do.
func TestTheDebugCommands(t *testing.T) {
	newTestHost(t)
	newSession(t, "dbg", "/bin/sh", "-c", "exec sleep 60")
	out := ok(t, "debug", "start", "box/dbg", "--for", "10m", "--only", "memory,runtime")
	for _, want := range []string{"Debug is on for dbg on box until ", "(memory, runtime)", "~/.local/state/", "debug stop box/dbg"} {
		if !strings.Contains(out, want) {
			t.Errorf("debug start lacks %q:\n%s", want, out)
		}
	}
	if ls := ok(t, "ls", "--json"); !strings.Contains(ls, `"debug_until"`) {
		t.Errorf("ls --json shows no end: %s", ls)
	}
	if out := ok(t, "debug", "save", "box/dbg"); !strings.Contains(out, "Saved the flight recorder of dbg: ~/") {
		t.Errorf("debug save = %q", out)
	}
	if out := ok(t, "debug", "profile", "box/dbg", "heap"); !strings.Contains(out, "Wrote a heap profile of dbg: ~/") {
		t.Errorf("debug profile = %q", out)
	}
	if out := ok(t, "debug", "profile", "box/dbg", "cpu", "--for", "1s"); !strings.Contains(out, "Wrote a cpu profile") {
		t.Errorf("debug profile cpu = %q", out)
	}
	if out := ok(t, "debug", "stop", "box/dbg"); !strings.HasPrefix(out, "Debug is off for dbg on box.") {
		t.Errorf("debug stop = %q", out)
	}
	if out := ok(t, "debug", "stop", "box/dbg"); out != "Debug was not on for dbg on box.\n" {
		t.Errorf("a second debug stop = %q", out)
	}
	fails(t, "--for is for a cpu profile only", "debug", "profile", "box/dbg", "heap", "--for", "1m")
	fails(t, "is not between 1s and 24h", "debug", "start", "box/dbg", "--for", "25h")
	failsWithHint(t, "net is not a category of the host", "--debug", "debug", "start", "box/dbg", "--only", "attach,net")
	fails(t, "no profile threads", "debug", "profile", "box/dbg", "threads")
	fails(t, "is not between 1s and 5m", "debug", "profile", "box/dbg", "cpu", "--for", "10m")
	failsWithHint(t, "the flight recorder of this session is off", "debug start", "debug", "save", "box/dbg")
}
