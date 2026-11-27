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

func TestTheDebugFlags(t *testing.T) {
	newTestHome(t)
	fakeBox(t, nil)
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
