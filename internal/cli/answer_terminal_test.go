package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// fakeKeys is the key tester's input: it says when its read starts, which
// never ends, and its Cancel, which never works, as a Windows console's
// may not.
type fakeKeys struct {
	started, cancelled chan struct{}
	release            chan struct{}
}

func (f *fakeKeys) Read([]byte) (int, error) {
	close(f.started)
	<-f.release
	return 0, io.EOF
}

func (f *fakeKeys) Cancel() bool {
	select {
	case <-f.cancelled:
	default:
		close(f.cancelled)
	}
	return false
}

func (*fakeKeys) Close() error { return nil }

// The key tester reads through its fake, asks it to cancel at the end of
// its context, ends although the cancel fails, and puts the terminal back.
func TestTheKeyTesterEndsWhenItsReadCannotBeCancelled(t *testing.T) {
	newTestHost(t)
	withSignal(t)
	f := &fakeKeys{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	defer close(f.release)
	old := newKeyReader
	newKeyReader = func(*os.File) (cancelreader.CancelReader, error) { return f, nil }
	defer func() { newKeyReader = old }()
	term := hosttest.NewTerminal(t, 100, 30)
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = term.TTY, term.TTY
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- keyTester(ctx) }()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the key tester never read its input")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("keyTester = %v, want the context's end", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the key tester still waits for a read that cannot be cancelled")
	}
	select {
	case <-f.cancelled:
	default:
		t.Error("the key tester did not ask its read to cancel")
	}
	term.WaitFor("\x1b[<u\x1b[?2004l")
	term.Clear()
	term.Type("cooked\n")
	term.WaitFor("cooked")
}

// A key of the configuration that does not parse is the user's to fix: a
// usage error that names the setting.
func TestTheKeyTesterSaysWhichKeyIsWrong(t *testing.T) {
	newTestHost(t)
	cfg := client.ConfigDir()
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg+"/config.toml", []byte("[attach]\ndetach_key = \"oops\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, done := inTerminal(t, "doctor", "keys")
	err := wait(t, done)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "bad_config" || ue.Hint != "Fix attach.detach_key in the config." || printError(io.Discard, err, nil, false, false) != 2 {
		t.Errorf("doctor keys with a bad detach key = %+v", err)
	}
}

// A signal just after the last yes of the fixes applies no fix.
func TestASignalAfterTheLastYesAppliesNoFix(t *testing.T) {
	withSignal(t)
	afterAnswer = func(line string) {
		if line == "y\n" {
			close(interrupted)
		}
	}
	applied := false
	p := &prompter{ctx: context.Background(), in: strings.NewReader("y\n"), out: io.Discard, on: true}
	err := offerFixes(p, qFix, sampleFixes[:1], false, func([]string) ([]api.Fix, error) {
		applied = true
		return nil, nil
	})
	if !errors.Is(err, errInterrupted) || applied {
		t.Errorf("offerFixes = %v, applied %v", err, applied)
	}
}

// A cancel just after the yes to the helper's install installs nothing.
func TestACancelAfterTheYesInstallsNothing(t *testing.T) {
	withSignal(t)
	_, tr := withFake(t, nil)
	tr.NoHelper = true
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	afterAnswer = func(string) { cancel() }
	p := &prompter{ctx: ctx, in: strings.NewReader("y\n"), out: io.Discard, on: true}
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	err := addHost(ctx, p, box, false)
	if !errors.Is(err, errInterrupted) {
		t.Errorf("host add with a cancel after the yes = %v", err)
	}
	for _, r := range tr.Remotes() {
		if strings.Contains(r, "__INSTALLED__") {
			t.Errorf("the helper was installed after the cancel: %q", r)
		}
	}
}

// The question before the helper's install looks for a cancel once more
// after its yes: a cancel that came with the yes, which the question
// itself did not see, installs nothing.
func TestTheInstallConsentLooksForACancel(t *testing.T) {
	withSignal(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		ctx  context.Context
		want bool
	}{{context.Background(), true}, {ctx, false}} {
		p := &prompter{ctx: context.Background(), in: strings.NewReader("y\n"), out: io.Discard, on: true}
		ok, err := installConsent(c.ctx, p, client.Host{Name: "box"}, false)("Install it?")
		if ok != c.want || !c.want && !errors.Is(err, errInterrupted) {
			t.Errorf("a yes with the context ended %v: %v, %v", !c.want, ok, err)
		}
	}
}

// A cancel while host add waits for its last answer from the host, the
// agents' checks, saves no host: SaveHosts refuses after a cancel. Guided
// setup connects a host through the same code.
func TestACancelDuringHostAddsLastRequestSavesNothing(t *testing.T) {
	withSignal(t)
	_, tr := withFake(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.OnRequest = func(op string) {
		if op == "agents.check" {
			cancel()
		}
	}
	p := &prompter{ctx: ctx, in: strings.NewReader(""), out: io.Discard, on: true}
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	if err := addHost(ctx, p, box, true); !errors.Is(err, context.Canceled) {
		t.Errorf("host add with a cancel during its last request = %v", err)
	}
	if !slices.Contains(tr.Requests(), "agents.check") {
		t.Fatalf("host add never asked for the agents' checks: %q", tr.Requests())
	}
	if h, err := client.FindHost("box"); err == nil {
		t.Errorf("host add saved %+v after the cancel", h)
	}
}

// A signal just after the yes to host destroy deletes nothing.
func TestASignalAfterTheYesDestroysNothing(t *testing.T) {
	withSignal(t)
	m, _ := withFake(t, nil)
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
		t.Fatal(err)
	}
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	_, _ = w.WriteString("box\n")
	_ = w.Close()
	oldIn := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = oldIn }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	afterAnswer = func(string) { cancel() }
	p := &prompter{ctx: ctx, in: in, out: io.Discard, on: true}
	if err := removeHost(ctx, p, rmOpts{name: "box", destroy: true, force: true}); !errors.Is(err, errInterrupted) {
		t.Errorf("destroyHost = %v, want a cancel", err)
	}
	for _, c := range m.Calls() {
		if c == "destroy" {
			t.Error("the machine was destroyed after the cancel")
		}
	}
}
