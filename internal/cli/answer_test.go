package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// keptAnswers is every answer about a setup script that the client keeps
// for host: a yes as true, and a no as false.
func keptAnswers(host string) map[string]bool {
	a := client.LoadSetupAnswers(host)
	kept := map[string]bool{}
	for repo := range a.Refused {
		kept[repo] = false
	}
	for repo := range a.Trusted {
		kept[repo] = true
	}
	return kept
}

// withSignal runs fn with a fresh interrupted channel, the once that
// closes it, and the beforeChoice and afterAnswer hooks, and puts them
// back after it.
func withSignal(t *testing.T) {
	t.Helper()
	oldInt, oldChoice, oldAfter := interrupted, beforeChoice, afterAnswer
	interrupted, beforeChoice, afterAnswer = make(chan struct{}), func() {}, func(string) {}
	interruptedOnce = sync.Once{}
	t.Cleanup(func() {
		interrupted, beforeChoice, afterAnswer = oldInt, oldChoice, oldAfter
		interruptedOnce = sync.Once{}
	})
}

// waiting is input that never comes, until the test ends, and that no
// cancel stops. reading closes when the first read of it starts, so that a
// test can cancel while the read waits, and not before it.
type waiting struct {
	reading chan struct{}
	once    sync.Once
	release chan struct{}
}

func newWaiting(t *testing.T) *waiting {
	t.Helper()
	w := &waiting{reading: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(w.release) })
	return w
}

func (w *waiting) Read([]byte) (int, error) {
	w.once.Do(func() { close(w.reading) })
	<-w.release
	return 0, io.EOF
}

// readStarts is a terminal's reader that closes reading when its first
// read starts.
type readStarts struct {
	cancelreader.CancelReader
	reading chan struct{}
	once    sync.Once
}

func (r *readStarts) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.reading) })
	return r.CancelReader.Read(p)
}

// within fails the test unless fn returns within five seconds.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s still waits", what)
	}
}

// An answer takes its line and nothing after it, which an attach that
// follows needs.
func TestAnswerTakesNothingAfterTheNewline(t *testing.T) {
	withSignal(t)
	in := strings.NewReader("yes\nnext")
	if got, err := answer(context.Background(), in); got != "yes\n" || err != nil {
		t.Fatalf("answer = %q, %v", got, err)
	}
	if rest, _ := io.ReadAll(in); string(rest) != "next" {
		t.Errorf("answer left %q, want \"next\"", rest)
	}
	if got, err := answer(context.Background(), strings.NewReader("no newline")); got != "no newline" || err != nil {
		t.Errorf("answer of a closed input = %q, %v", got, err)
	}
	if got, err := readAnswer(bufio.NewReader(strings.NewReader("y\n"))); got != "y\n" || err != nil {
		t.Errorf("readAnswer = %q, %v", got, err)
	}
}

// A cancel wins: the end of the context or a signal, before the read, while
// the input is still blocked, and when it comes with the line.
func TestAnswerGivesUpAtACancel(t *testing.T) {
	withSignal(t)
	ctx, cancel := context.WithCancel(context.Background())
	in := newWaiting(t)
	go func() {
		<-in.reading
		cancel()
	}()
	within(t, "answer after the context ended, with the input blocked,", func() {
		if _, err := answer(ctx, in); !errors.Is(err, errInterrupted) {
			t.Errorf("answer = %v", err)
		}
	})
	signal := interrupted
	in = newWaiting(t)
	go func() {
		<-in.reading
		close(signal)
	}()
	within(t, "readAnswer after a signal, with the input blocked,", func() {
		if _, err := readAnswer(bufio.NewReader(in)); !errors.Is(err, errInterrupted) {
			t.Errorf("readAnswer = %v", err)
		}
	})

	// A signal before the question: no read starts, and the input waits.
	buffered := bufio.NewReader(strings.NewReader("yes\n"))
	if _, err := ask(buffered, io.Discard, "Fix it?"); !errors.Is(err, errInterrupted) || buffered.Buffered() != 0 {
		t.Errorf("ask after a signal = %v", err)
	}
	if rest, _ := io.ReadAll(buffered); string(rest) != "yes\n" {
		t.Errorf("the answer after a signal was read: %q left", rest)
	}

	// A cancel that comes with the line.
	withSignal(t)
	beforeChoice = func() { close(interrupted) }
	if got, err := answer(context.Background(), strings.NewReader("y\n")); got != "" || !errors.Is(err, errInterrupted) {
		t.Errorf("an answer with a signal = %q, %v", got, err)
	}
	withSignal(t)
	ctx, cancel = context.WithCancel(context.Background())
	beforeChoice = cancel
	if got, err := answer(ctx, strings.NewReader("y\n")); got != "" || !errors.Is(err, errInterrupted) {
		t.Errorf("an answer as the context ends = %q, %v", got, err)
	}
}

// A cancel at the question of a setup script keeps no answer: one that
// comes with the yes, and one that comes while the input is blocked.
func TestAnAnswerThatMeetsASignalKeepsNothing(t *testing.T) {
	hosttest.New(t)
	withSignal(t)
	_, tr := withFake(t, nil)
	if err := client.SaveHosts([]client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(api.SetupQuestion{Repo: "/home/me/app", Script: "/home/me/app/.x/setup"})
	tr.Fail("new", &proto.Error{Code: api.CodeSetupTrust, Message: "the repository has a setup script", Hint: "Pass --setup.", Data: q})
	h, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(context.Background(), h, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	term := hosttest.NewTerminal(t, 100, 30)
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = term.TTY, term.TTY
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	term.Type("y\n")
	beforeChoice = func() { close(interrupted) }
	if _, err := startSession(context.Background(), conn, h, client.NewArgs{}); !errors.Is(err, errInterrupted) {
		t.Fatalf("a yes with a signal = %v, want a cancel", err)
	}
	withSignal(t)
	term.Type("y\n")
	ctx, cancel := context.WithCancel(context.Background())
	beforeChoice = cancel
	if _, err := startSession(ctx, conn, h, client.NewArgs{}); !errors.Is(err, errInterrupted) {
		t.Fatalf("a yes as the context ends = %v, want a cancel", err)
	}
	withSignal(t)
	term.Type("y\n")
	ctx, cancel = context.WithCancel(context.Background())
	afterAnswer = func(string) { cancel() }
	if _, err := startSession(ctx, conn, h, client.NewArgs{}); !errors.Is(err, errInterrupted) {
		t.Fatalf("a cancel after the yes = %v, want a cancel", err)
	}
	// The context ends while the read of the terminal waits.
	withSignal(t)
	reading := make(chan struct{})
	open := newKeyReader
	newKeyReader = func(f *os.File) (cancelreader.CancelReader, error) {
		cr, err := open(f)
		if err != nil {
			return nil, err
		}
		return &readStarts{CancelReader: cr, reading: reading}, nil
	}
	defer func() { newKeyReader = open }()
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		<-reading
		cancel()
	}()
	within(t, "the question after the context ended, with the input blocked,", func() {
		if _, err := startSession(ctx, conn, h, client.NewArgs{}); !errors.Is(err, errInterrupted) {
			t.Errorf("a cancel with no answer = %v, want a cancel", err)
		}
	})
	if kept := keptAnswers("box"); len(kept) != 0 {
		t.Errorf("the client kept %v", kept)
	}
}

// lockedBuffer is a buffer that a guide writes while a test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The guided setup stops at a cancel while its input is blocked, a read
// that cannot be cancelled: at the end of its context, and at a signal. It
// changes nothing, and exits with 130.
func TestTheGuidedSetupStopsWithItsInputBlocked(t *testing.T) {
	for _, bySignal := range []bool{false, true} {
		withSignal(t)
		_, tr := withFake(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		l := &guideLocal{}
		g, _, _ := testGuide(t, ctx, guideOpts{}, l, tr, "", true)
		out := &lockedBuffer{}
		in := newWaiting(t)
		g.in, g.out = in, out
		signal := interrupted
		go func() {
			<-in.reading
			if bySignal {
				close(signal)
			} else {
				cancel()
			}
		}()
		var err error
		within(t, "the setup", func() { err = g.run() })
		cancel()
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Msg != "setup stopped; nothing was changed" || printError(io.Discard, err, nil, false, false) != 130 {
			t.Errorf("signal %v: %v\n%s", bySignal, err, out.String())
		}
		if hosts, _ := client.LoadHosts(); len(hosts) != 0 || len(l.added) != 0 {
			t.Errorf("signal %v: the setup saved %v and added %q", bySignal, hosts, l.added)
		}
	}
}

// lastByteCancels gives its text a byte at a time, and ends the setup's
// context as it gives the last byte: Ctrl+C as the newline of an answer
// arrives.
type lastByteCancels struct {
	text   string
	at     int
	cancel context.CancelFunc
}

func (c *lastByteCancels) Read(p []byte) (int, error) {
	if c.at >= len(c.text) {
		return 0, io.EOF
	}
	p[0] = c.text[c.at]
	c.at++
	if c.at == len(c.text) {
		c.cancel()
	}
	return 1, nil
}

// A Ctrl+C that comes with the newline of the yes to the SSH config lines
// writes no line.
func TestACancelWithTheYesWritesNoSSHConfig(t *testing.T) {
	withSignal(t)
	withFake(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &guideLocal{key: "~/.ssh/id_ed25519.pub"}
	g, out, _ := testGuide(t, ctx, guideOpts{}, l, &clienttest.Transport{}, "", true)
	// The address, the default user, Enter for the check, the default
	// name, and yes to the lines.
	g.in = &lastByteCancels{text: "203.0.113.10\n\n\n\ny\n", cancel: cancel}
	if _, err := g.rent(); !errors.Is(err, context.Canceled) {
		t.Errorf("rent = %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "Add them to the end of ~/.ssh/config? [y/N]") || len(l.added) != 0 {
		t.Errorf("added %q\n%s", l.added, out)
	}

	// A Ctrl+C just after the yes, before the lines are written.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	l2 := &guideLocal{key: "~/.ssh/id_ed25519.pub"}
	g2, out2, _ := testGuide(t, ctx2, guideOpts{}, l2, &clienttest.Transport{}, "203.0.113.10\n\n\n\ny\n", true)
	afterAnswer = func(line string) {
		if line == "y\n" {
			cancel2()
		}
	}
	if _, err := g2.rent(); !errors.Is(err, context.Canceled) || len(l2.added) != 0 {
		t.Errorf("rent = %v, added %q\n%s", err, l2.added, out2)
	}
}

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
	err := offerFixes(context.Background(), strings.NewReader("y\n"), io.Discard, sampleFixes[:1], false, func([]string) ([]api.Fix, error) {
		applied = true
		return nil, nil
	})
	if !errors.Is(err, errInterrupted) || applied {
		t.Errorf("offerFixes = %v, applied %v", err, applied)
	}
}

// A signal just after the yes to host destroy deletes nothing.
func TestASignalAfterTheYesDestroysNothing(t *testing.T) {
	withSignal(t)
	m, _ := withFake(t, nil)
	if err := client.SaveHosts([]client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
		t.Fatal(err)
	}
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	_, _ = w.WriteString("y\n")
	_ = w.Close()
	oldIn := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = oldIn }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	afterAnswer = func(string) { cancel() }
	if err := destroyHost(ctx, "box", true, false); !errors.Is(err, errInterrupted) {
		t.Errorf("destroyHost = %v, want a cancel", err)
	}
	for _, c := range m.Calls() {
		if c == "destroy" {
			t.Error("the machine was destroyed after the cancel")
		}
	}
}
