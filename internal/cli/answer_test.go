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
	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
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
	if got, err := answer(context.Background(), bufio.NewReader(strings.NewReader("y\n"))); got != "y\n" || err != nil {
		t.Errorf("answer of a buffered reader = %q, %v", got, err)
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
	within(t, "answer after a signal, with the input blocked,", func() {
		if _, err := answer(context.Background(), bufio.NewReader(in)); !errors.Is(err, errInterrupted) {
			t.Errorf("answer = %v", err)
		}
	})

	// A signal before the question: no read starts, and the input waits.
	buffered := bufio.NewReader(strings.NewReader("yes\n"))
	p := &prompter{ctx: context.Background(), in: buffered, out: io.Discard, on: true}
	if _, err := p.yesNo("Fix it?", false); !errors.Is(err, errInterrupted) || buffered.Buffered() != 0 {
		t.Errorf("a question after a signal = %v", err)
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
	if err := client.SaveHosts(context.Background(), []client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(api.SetupQuestion{Repo: "/home/me/app", Script: "/home/me/app/.x/setup"})
	tr.Fail("new", &proto.Error{Code: api.CodeSetupTrust, Message: "the repository has a setup script", Hint: "Pass --setup-script.", Data: q})
	h, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(context.Background(), h, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The question asks only with input on: stdin, stdout and stderr are
	// the terminal.
	t.Setenv(noInputEnv, "")
	term := hosttest.NewTerminal(t, 100, 30)
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = term.TTY, term.TTY, term.TTY
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()

	term.Type("y\n")
	beforeChoice = func() { close(interrupted) }
	if _, err := startSession(sessionCmd(context.Background()), conn, h, client.NewArgs{}); !errors.Is(err, errInterrupted) {
		t.Fatalf("a yes with a signal = %v, want a cancel", err)
	}
	withSignal(t)
	term.Type("y\n")
	ctx, cancel := context.WithCancel(context.Background())
	beforeChoice = cancel
	if _, err := startSession(sessionCmd(ctx), conn, h, client.NewArgs{}); !errors.Is(err, errInterrupted) {
		t.Fatalf("a yes as the context ends = %v, want a cancel", err)
	}
	withSignal(t)
	term.Type("y\n")
	ctx, cancel = context.WithCancel(context.Background())
	afterAnswer = func(string) { cancel() }
	if _, err := startSession(sessionCmd(ctx), conn, h, client.NewArgs{}); !errors.Is(err, errInterrupted) {
		t.Fatalf("a cancel after the yes = %v, want a cancel", err)
	}
	// The context ends while the read of the terminal waits. A question
	// reads the terminal through keyread: on a new terminal, keyread.Open
	// gives it a reader whose read says, from inside Read, that it has
	// started, and waits there. Only then does the context end, so the
	// cancel meets a blocked read.
	withSignal(t)
	blocked := hosttest.NewTerminal(t, 100, 30)
	os.Stdin, os.Stdout, os.Stderr = blocked.TTY, blocked.TTY, blocked.TTY
	src := &heldRead{entered: make(chan struct{}, 1), stop: make(chan struct{})}
	open := keyread.Open
	keyread.Open = func(io.Reader) (cancelreader.CancelReader, error) { return src, nil }
	defer func() { keyread.Open = open }()
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		<-src.entered
		cancel()
	}()
	within(t, "the question after the context ended, with the input blocked,", func() {
		//nolint:contextcheck // startSession takes ctx from its command
		if _, err := startSession(sessionCmd(ctx), conn, h, client.NewArgs{}); !errors.Is(err, errInterrupted) {
			t.Errorf("a cancel with no answer = %v, want a cancel", err)
		}
	})
	if !src.waited() {
		t.Error("the read of the terminal ended before the context did")
	}
	if kept := keptAnswers("box"); len(kept) != 0 {
		t.Errorf("the client kept %v", kept)
	}
}

// heldRead is a terminal's reader whose read says, from inside Read, that
// it has started, and waits there until its Cancel. keyread never cancels a
// read: the terminal's cleanup does (keyread.Release).
type heldRead struct {
	entered chan struct{}
	stop    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	inRead  bool
	// held stays true once a read was still in Read after it said so.
	held bool
}

func (h *heldRead) Read([]byte) (int, error) {
	h.mu.Lock()
	h.inRead = true
	h.mu.Unlock()
	h.entered <- struct{}{}
	<-h.stop
	h.mu.Lock()
	h.inRead = false
	h.mu.Unlock()
	return 0, cancelreader.ErrCanceled
}

func (h *heldRead) Cancel() bool {
	h.once.Do(func() { close(h.stop) })
	return true
}

func (*heldRead) Close() error { return nil }

// waited reports whether the read is still held in Read: the question
// ended with its read blocked.
func (h *heldRead) waited() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held = h.held || h.inRead
	return h.held
}

// sessionCmd is a command for startSession, with ctx as its context.
func sessionCmd(ctx context.Context) *cobra.Command {
	c := &cobra.Command{Use: "new"}
	c.SetContext(withLine(ctx, []string{"new", "box"}))
	return c
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
// changes nothing, says so, and exits with 130.
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
		// The stop is the cancel of the prompts (cli-spec 6.2, rule 6).
		if !isCancel(err) || err.Error() != "Cancelled. Nothing changed." || printError(io.Discard, err, nil, false, false) != 130 {
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
