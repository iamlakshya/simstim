package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// These tests run the commands themselves, so that a revert of the line
// that wires a behaviour into a command fails them.

// A secret's command gets the terminal when input is not turned off, and
// stdin and stderr are terminals, also when stdout goes to a file: at new
// and at resume. With input off it gets no terminal.
func TestSecretCommandsGetTheTerminal(t *testing.T) {
	th := newTestHost(t)
	t.Setenv(noInputEnv, "")
	where := filepath.Join(th.h.Home, "where")
	writeSettings(t, "[secrets]\nTOK = \"if [ -t 0 ]; then echo tty >> '"+where+"'; else echo none >> '"+where+"'; fi; echo value\"\n")
	term := hosttest.NewTerminal(t, 80, 10)
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	atTerminal := func(args ...string) {
		t.Helper()
		oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
		os.Stdin, os.Stdout, os.Stderr = term.TTY, out, term.TTY
		root := Root()
		root.SetArgs(args)
		err := root.ExecuteContext(withLine(context.Background(), args))
		os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}
	atTerminal("new", "box", "--detach", "--name", "one", "--secret", "TOK", "--", "sleep", "60")
	atTerminal("new", "box", "--detach", "--name", "two", "--secret", "TOK", "--no-input", "--", "sleep", "60")
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--name", "three", "--secret", "TOK", "--", "/bin/sh", "-c", "exit 0"))
	if m == nil {
		t.Fatal("new started no session")
	}
	agentEnded(t, m[1])
	atTerminal("resume", "box/"+m[1], "--detach", "--secret", "TOK")
	// Input off at the terminal: with --no-input, and with --json.
	agentEnded(t, m[1])
	atTerminal("resume", "box/"+m[1], "--detach", "--secret", "TOK", "--no-input")
	agentEnded(t, m[1])
	atTerminal("resume", "box/"+m[1], "--detach", "--secret", "TOK", "--json")
	got, _ := os.ReadFile(where)
	if string(got) != "tty\nnone\nnone\ntty\nnone\nnone\n" {
		t.Errorf("the secret's command had a terminal at new, new --no-input, new from a pipe, resume, resume --no-input "+
			"and resume --json: %q, want tty, none, none, tty, none, none", got)
	}
}

// host doctor --fix --json needs -y, which answers for every fix: with it,
// the fixes run, and the JSON holds how each ended.
func TestHostFixWithJSON(t *testing.T) {
	newTestHost(t)
	old := time.Now().Add(-2 * time.Hour)
	var dirs []string
	for _, v := range []string{"v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0"} {
		dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), brand.Name, "bin", v)
		_ = os.MkdirAll(dir, 0o700)
		_ = os.Chtimes(dir, old, old)
		dirs = append(dirs, dir)
	}
	_, errOut, code := mainRun(t, "host", "doctor", "box", "--fix", "--json")
	if code != 4 || !strings.Contains(errOut, `host doctor box --fix --json -y"`) {
		t.Errorf("host doctor --fix --json: exit %d, %q", code, errOut)
	}
	out, errOut, code := mainRun(t, "host", "doctor", "box", "--fix", "--json", "-y")
	var d struct {
		V      int
		Checks []json.RawMessage
		Fixes  []struct {
			ID   string
			Done bool
		}
	}
	if code != 0 || errOut != "" || json.Unmarshal([]byte(out), &d) != nil || d.V != 1 || len(d.Checks) == 0 {
		t.Fatalf("host doctor --fix --json -y: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	done, gone := false, 0
	for _, f := range d.Fixes {
		done = done || f.ID == "old-helpers" && f.Done
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			gone++
		}
	}
	if !done || gone == 0 {
		t.Errorf("the old helpers were not removed: %+v", d.Fixes)
	}
}

// atCancel runs the command line with a context that has ended already.
func atCancel(t *testing.T, args ...string) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var err error
	_, _ = captureOutput(t, func() {
		root := Root() //nolint:contextcheck // the command takes ctx from ExecuteContext
		root.SetArgs(args)
		err = root.ExecuteContext(withLine(ctx, args))
	})
	return err
}

// No fix runs after a cancel: not doctor's with --json and -y, not
// doctor's with -y, and not host doctor's with --json and -y. The files stay,
// and the exit is the cancel's.
func TestNoFixAfterACancel(t *testing.T) {
	hosttest.New(t)
	gone := filepath.Join(client.ConfigDir(), "ssh", "config-gone")
	_ = os.MkdirAll(filepath.Dir(gone), 0o700)
	_ = os.WriteFile(gone, []byte("# generated\n"), 0o600)
	for _, args := range [][]string{{"doctor", "--fix", "--json", "-y"}, {"doctor", "--fix", "-y"}} {
		// Nothing asked: the cancel is no cancel at a question.
		err := atCancel(t, args...)
		if !isCancel(err) || errors.Is(err, errInterrupted) || printError(io.Discard, err, nil, false, false) != 130 {
			t.Errorf("%s with the context ended: %v", args, err)
		}
		if _, err := os.Stat(gone); err != nil {
			t.Fatalf("%s with the context ended removed the file: %v", args, err)
		}
	}

	newTestHost(t)
	old := time.Now().Add(-2 * time.Hour)
	var dirs []string
	for _, v := range []string{"v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0"} {
		dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), brand.Name, "bin", v)
		_ = os.MkdirAll(dir, 0o700)
		_ = os.Chtimes(dir, old, old)
		dirs = append(dirs, dir)
	}
	h, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(context.Background(), h, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hostFixesJSON(ctx, conn, h, client.HostInfo{}); !errors.Is(err, errInterrupted) {
		t.Errorf("host doctor --fix --json -y with the context ended: %v", err)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("a host fix ran after the cancel: %v", err)
		}
	}
}

// A cancel that comes after doctor's last look for one, just before it
// applies the fixes, applies none: doctor --fix -y applies them through
// applyLocal, which looks once more, and so does the client.
func TestACancelJustBeforeTheFixesFixesNothing(t *testing.T) {
	hosttest.New(t)
	gone := writeFixWrapper(t, "gone")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := fixStage
	fixStage = func(stage string) {
		if stage == "apply" {
			cancel()
		}
	}
	defer func() { fixStage = old }()
	args := []string{"doctor", "--fix", "-y"}
	var err error
	_, _ = captureOutput(t, func() {
		root := Root() //nolint:contextcheck // the command takes ctx from ExecuteContext
		root.SetArgs(args)
		err = root.ExecuteContext(withLine(ctx, args))
	})
	if !isCancel(err) || !strings.Contains(err.Error(), "during the fixes") || printError(io.Discard, err, nil, false, false) != 130 {
		t.Errorf("doctor --fix -y with a cancel just before the fixes: %v", err)
	}
	if _, err := os.Stat(gone); err != nil {
		t.Errorf("a fix ran after the cancel: %v", err)
	}
}

// A cancel that comes once the host has listed its fixes, while the client
// gets ready to ask for them (it reads Ghostty's terminfo), sends no apply:
// host doctor --fix --json -y stops at the cancel, as every request after
// a cancel does. A fake infocmp says when the apply's turn has come.
func TestACancelBeforeTheHostsApplyAppliesNone(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.Features = []string{"doctor.fix"}
	tr.Fixes = []api.Fix{{ID: "linger", Problem: "linger is off", Action: "turn it on", Command: "loginctl enable-linger"}}
	bin := t.TempDir()
	turn := filepath.Join(bin, "apply")
	script := "#!/bin/sh\nn=$(cat '" + bin + "/calls' 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > '" + bin + "/calls'\n" +
		"if [ $n -ge 2 ]; then : > '" + turn + "'; exec sleep 30; fi\n"
	clienttest.WriteScript(t, filepath.Join(bin, "infocmp"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	conn, err := client.Dial(context.Background(), h, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- hostFixesJSON(ctx, conn, h, client.HostInfo{}) }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(turn); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the apply's turn never came")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("host doctor --fix --json -y with a cancel before the apply: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("host doctor --fix --json -y did not end at the cancel")
	}
	if _, err := conn.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(tr.Requests(), " "), "doctor.fix"); n != 1 {
		t.Errorf("%d doctor.fix requests reached the host, want the list alone: %q", n, tr.Requests())
	}
}

// A cancel during the local fixes says what was done before it, as every
// cancel does, and never that nothing changed after a change: on the
// terminal, a fix that ran by its problem, and a fix that the cancel
// stopped part way by how far it got; in the data of --json, each fix as
// it ended, with each file it removed. It speaks of the fixes, not of a
// question. The cancel comes between two whole fixes, and between two
// files of one fix.
func TestACancelDuringTheFixesSaysWhatWasFixed(t *testing.T) {
	t.Run("between two fixes", func(t *testing.T) {
		hosttest.New(t)
		short, err := os.MkdirTemp(testhome.Root(), "f")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CACHE_HOME", short) // a socket's path in it must be short
		gone := writeFixWrapper(t, "gone")
		controlHost := client.Host{Name: "control-fixture", SSH: "control-fixture.invalid"}
		if err := client.SaveHostsContext(context.Background(), []client.Host{controlHost}); err != nil {
			t.Fatal(err)
		}
		pinFor(t, controlHost.Name)
		cmd, err := (&client.SSH{Host: controlHost}).Command(context.Background(), "true")
		if err != nil {
			t.Fatal(err)
		}
		var pattern string
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "ControlPath=") {
				pattern = strings.TrimPrefix(arg, "ControlPath=")
			}
		}
		if pattern == "" || pattern == "none" || !strings.Contains(pattern, "%C") {
			t.Fatalf("canonical controller did not publish an indexed control path: %q", cmd.Args)
		}
		dead := strings.ReplaceAll(pattern, "%C", strings.Repeat("a", 40))
		l, err := net.Listen("unix", dead)
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = l.Close()
		res, err := applyLocal(cancelAtTheFirstFile(t), []string{"ssh-configs", "control-sockets"})
		if line := cancelLineOf(t, err); line != "Cancelled during the fixes. Done before that: fixed: 1 generated SSH config(s) for hosts you removed.\n" {
			t.Errorf("a cancel between two fixes: %q", line)
		}
		if fixes := cancelledFixes(t, err); len(fixes) != 2 || !fixes[0].Done || fixes[1].Done || len(fixes[1].Changed) != 0 {
			t.Errorf("the fixes in the JSON error: %+v", fixes)
		}
		if len(res) != 2 || !res[0].Done || res[1].Done {
			t.Errorf("the fixes after a cancel between them: %+v", res)
		}
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the completed first fix left its wrapper: %v", err)
		}
		if _, err := os.Stat(dead); err != nil {
			t.Errorf("the second fix ran after the cancel: %v", err)
		}
	})
	t.Run("between two files", func(t *testing.T) {
		hosttest.New(t)
		var gone []string
		for _, name := range []string{"gone1", "gone2"} {
			gone = append(gone, writeFixWrapper(t, name))
		}
		_, err := applyLocal(cancelAtTheFirstFile(t), []string{"ssh-configs"})
		if line := cancelLineOf(t, err); line != "Cancelled during the fixes. Done before that: removed 1 of 2 generated SSH configs.\n" {
			t.Errorf("a cancel after the first file: %q", line)
		}
		if fixes := cancelledFixes(t, err); len(fixes) != 1 || fixes[0].Done || !slices.Equal(fixes[0].Changed, []string{"removed " + gone[0]}) {
			t.Errorf("the fixes in the JSON error: %+v", fixes)
		}
		if _, err := os.Stat(gone[1]); err != nil {
			t.Errorf("the second file went after the cancel: %v", err)
		}
	})
}

// writeFixWrapper creates an old-name wrapper with the canonical ownership
// header. A filename or a generic generated comment does not authorise removal.
func writeFixWrapper(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(client.ConfigDir(), "ssh", "config-"+name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("# Generated by %s for host %q. Do not edit; it is rewritten on each run.\nHost %s\n", brand.Name, name, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// cancelAtTheFirstFile is a context that a cancel ends once a local fix
// has removed its first file.
func cancelAtTheFirstFile(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := client.RemovedFile
	client.RemovedFile = func(string) { cancel() }
	t.Cleanup(func() { client.RemovedFile = old; cancel() })
	return ctx
}

// cancelLineOf is the line that err prints on a terminal, and checks that
// its exit code is the cancel's.
func cancelLineOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("the cancelled fixes returned no error")
	}
	var line bytes.Buffer
	if code := printError(&line, err, nil, false, false); code != 130 {
		t.Errorf("%v exits with %d, want 130", err, code)
	}
	return line.String()
}

// cancelledFixes is the fixes in the data of err as --json prints it, and
// checks that its exit code is the cancel's.
func cancelledFixes(t *testing.T, err error) []api.Fix {
	t.Helper()
	var out bytes.Buffer
	if code := printErrorJSON(&out, err, nil, false); code != 130 {
		t.Errorf("the JSON error exits with %d, want 130: %s", code, out.Bytes())
	}
	var line struct {
		Error struct {
			Code string
			Data struct{ Fixes []api.Fix }
		}
	}
	if json.Unmarshal(out.Bytes(), &line) != nil || line.Error.Code != "cancelled" {
		t.Errorf("the JSON error: %s", out.Bytes())
	}
	return line.Error.Data.Fixes
}

// stopping gives a context that has ended already when a signal came
// before it: at once, not when its watcher of signals runs, which the test
// holds.
func TestStoppingSeesAnEarlierSignalAtOnce(t *testing.T) {
	withSignal(t)
	held := make(chan struct{})
	old := stoppingWatch
	stoppingWatch = func() { <-held }
	defer func() { close(held); stoppingWatch = old }()
	close(interrupted)
	sctx, cancel := stopping(context.Background())
	defer cancel()
	if sctx.Err() == nil {
		t.Error("stopping's context is alive after a signal")
	}
}

// A signal that came before the fixes, with the context not yet ended,
// fixes nothing: the context that the fixes get has ended at once. The
// watcher of signals is held, so that only that first look can end it.
func TestASignalBeforeTheFixesFixesNothing(t *testing.T) {
	withSignal(t)
	held := make(chan struct{})
	old := stoppingWatch
	stoppingWatch = func() { <-held }
	defer func() { close(held); stoppingWatch = old }()
	hosttest.New(t)
	gone := filepath.Join(client.ConfigDir(), "ssh", "config-gone")
	_ = os.MkdirAll(filepath.Dir(gone), 0o700)
	_ = os.WriteFile(gone, []byte("# generated\n"), 0o600)
	close(interrupted)
	_, err := applyLocal(context.Background(), []string{"ssh-configs"})
	if !isCancel(err) {
		t.Errorf("the fixes after a signal: %v", err)
	}
	if _, err := os.Stat(gone); err != nil {
		t.Errorf("a fix ran after the signal: %v", err)
	}
}

// Main writes the warnings of the end of a debug run as JSON lines, with
// --json.
func TestMainWarnsInJSON(t *testing.T) {
	hosttest.New(t)
	old := stopDebug
	defer func() { stopDebug = old }()
	stopDebug = func(w io.Writer) {
		fmt.Fprint(w, "Warning: the debug file x is not complete.\nTry: make room.\n")
	}
	out, errOut, code := mainRun(t, "version", "--json")
	var warn struct {
		V       int
		Warning map[string]string
	}
	if code != 0 || !json.Valid([]byte(out)) || json.Unmarshal([]byte(errOut), &warn) != nil || warn.Warning["code"] != "debug_incomplete" {
		t.Errorf("version --json: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if _, errOut, _ = mainRun(t, "version"); !strings.HasPrefix(errOut, "Warning: the debug file x") {
		t.Errorf("version: stderr %q", errOut)
	}
}

// host add with --no-input takes the checklist's default: it offers no
// agent, and says how to install each.
// The command runs at a terminal, stdin, stdout and stderr, so input would
// be on without the flag.
func TestHostAddChecklistNoInputCommand(t *testing.T) {
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")
	t.Setenv(noInputEnv, "")
	term, done := inTerminal(t, "host", "add", "added", "box", "--tier", "own", "--no-input")
	term.WaitFor("Agents:")
	// An Enter would answer the checklist, if the command asked it.
	term.Type("\r")
	if err := wait(t, done); err != nil {
		t.Fatalf("host add --no-input: %v\n%s", err, term.Output())
	}
	out := string(term.WaitFor("added is ready."))
	if strings.Contains(out, "Type the numbers") || !strings.Contains(out, "agents install claude added") {
		t.Errorf("host add --no-input at a terminal offered the checklist:\n%s", out)
	}
}

// host ls --json warns about its old keys also with no hosts.
func TestHostLsJSONWithNoHostsWarns(t *testing.T) {
	hosttest.New(t)
	out, errOut, code := mainRun(t, "host", "ls", "--json")
	var warn struct {
		V       int
		Warning map[string]string
	}
	if code != 0 || strings.TrimSpace(out) != "[]" || json.Unmarshal([]byte(errOut), &warn) != nil || warn.Warning["old"] == "" {
		t.Errorf("host ls --json with no hosts: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}
