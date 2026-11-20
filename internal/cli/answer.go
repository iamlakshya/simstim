package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keyread"
	"github.com/iamlakshya/simstim/internal/keys"
)

// The one reader of what a person types at a question
// (docs/contributing/cli-spec.md 5.1, rule 7, and 6.2). Every question
// reads through answer: the prompter's questions (readLine), and so the
// guided setup's. At a terminal, answer reads in raw mode, through the one
// reader of the terminal (keyread.For), which every question and the
// picker share: a new reader on Windows drops what waits in the console.
// The key tester and the terminal check read raw bytes, through inputOf
// and keys.ReadUntil. A test fails on a direct read of os.Stdin anywhere
// else (TestNoDirectReadOfStdin).

// interruptError is the error of a question that a signal or the end of
// its context ended. It is a cancel, never an answer: a caller stops at
// it, before it keeps or acts on anything. It counts as context.Canceled,
// as every cancel at a question does.
type interruptError struct{ *client.UserError }

func (e interruptError) Unwrap() error { return e.UserError }

func (interruptError) Is(target error) bool { return target == context.Canceled }

// errInterrupted is the error of a question that a signal or the end of
// its context ended.
var errInterrupted error = interruptError{&client.UserError{Msg: "a signal ended the command at a question, so the question has no answer",
	Hint: "Run the command again.", Code: "cancelled", Kind: client.KindCancelled}}

// beforeChoice runs in answer when the line has come, before answer looks
// for a cancel once more. Tests set it to make a cancel come with the line.
var beforeChoice = func() {}

// afterAnswer runs in answer when it gives a line back. Tests set it to
// make a cancel come after the answer, before the caller acts on it: a
// caller that writes something checks once more.
var afterAnswer = func(line string) {}

// answer reads one line of an answer from in, with its newline, as
// answerOn does, with no question and no echo.
func answer(ctx context.Context, in io.Reader) (string, error) {
	return answerOn(ctx, in, io.Discard, "")
}

// answerOn writes the question q on echo, and reads one line of an
// answer from in, with its newline, a byte at a time, so that it takes
// nothing after the newline: the next question or an attach that follows
// gets every byte. Only a paste is read in blocks (keyread): its last
// block can hold keys typed just after it, also past the end of the line.
// Those stay in the terminal's reader for the next question, and an attach
// does not get them. A closed input gives what came before it, or "". At a
// terminal it reads the line in raw mode, and writes q once the terminal
// is raw, so that a key typed as soon as q shows is read as a key. It
// echoes the line on echo: esc and ctrl+c cancel at once (cancelled), as
// does ctrl+d on an empty line. It gives up, with errInterrupted, when ctx
// ends or a signal comes: before the read, while it waits, and when the
// line comes with them, since a cancel that it has seen wins. Its read
// runs in its own goroutine, so a read that cannot be cancelled, as from a
// pipe or on some Windows consoles, never keeps it.
func answerOn(ctx context.Context, in io.Reader, echo io.Writer, q string) (string, error) {
	if stopped(ctx) {
		return "", errInterrupted
	}
	sctx, cancel := stopping(ctx)
	defer cancel()
	line, err := readLineOf(sctx, in, echo, q)
	beforeChoice()
	if stopped(ctx) || errors.Is(err, errPromptSignal) || err != nil && sctx.Err() != nil {
		return "", errInterrupted
	}
	if err != nil {
		return "", err
	}
	afterAnswer(line)
	return line, nil
}

// readLineOf writes q, and reads one line from in, with its newline: from
// a terminal in raw mode, or else a byte at a time from in as it is.
func readLineOf(ctx context.Context, in io.Reader, echo io.Writer, q string) (string, error) {
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		return readTerminalLine(ctx, f, echo, q)
	}
	_, _ = io.WriteString(echo, q)
	var line []byte
	err := keys.ReadUntil(ctx, oneByte{plainInput{in}}, func(b []byte) bool {
		line = append(line, b...)
		return line[len(line)-1] != '\n'
	})
	return string(line), err
}

// keyEsc is the esc key, which cancels a line read from a pipe.
const keyEsc = 0x1b

// readLine asks q on echo, and reads the line of its answer from in,
// through answerOn, as it was typed, except for its line ending. The
// prompter trims the spaces at its ends, except at a question whose answer
// must be exact (typedName). Esc in a line from a pipe cancels, as does
// the end of input before any text.
func readLine(ctx context.Context, in io.Reader, echo io.Writer, q string) (string, error) {
	line, err := answerOn(ctx, in, echo, q)
	switch {
	case err != nil:
		return "", err
	case line == "", strings.ContainsRune(line, keyEsc):
		return "", cancelled()
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

// promptSignals end a question in raw mode. In raw mode ctrl+c is a key,
// so these come from outside: kill, a closed terminal, or a quit.
var promptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}

// errPromptSignal is the error of a line that a signal ended: one of
// promptSignals, which answerOn turns into errInterrupted.
var errPromptSignal = errors.New("a signal came at the question")

// pasteOn and pasteOff turn the terminal's bracketed paste on and off.
// While it is on, a paste comes as one key (keyread.Paste), so a paste of
// many lines is text in the line, and never ends it. A console on Windows
// may show the sequences as text, and so may a terminal with TERM=dumb, as
// the colour rule has it: a question sends none there.
const (
	pasteOn  = "\x1b[?2004h"
	pasteOff = "\x1b[?2004l"
)

// pasteModes is whether a question turns bracketed paste on.
var pasteModes = runtime.GOOS != "windows"

// promptStage is called at stages of readTerminalLine's end: "restored",
// once the terminal is restored. Tests send a signal at a stage of their
// choice.
var promptStage = func(string) {}

// terminalReadError turns a failure to open or configure a terminal reader
// into a UserError rather than falling back to unsafe cooked line reads.
func terminalReadError(err error) error {
	if errors.Is(err, keyread.ErrStuck) {
		return &client.UserError{
			Msg:  "the terminal's reader from an earlier session is still running",
			Hint: "Run the command again in a new command.",
			Code: "terminal_busy",
		}
	}
	return &client.UserError{
		Msg:  "cannot read the terminal safely: " + err.Error(),
		Hint: "Open a new terminal, or pass --yes or --confirm to answer on the command line.",
		Code: "terminal_failed",
	}
}

// readTerminalLine puts the terminal in raw mode, writes q on echo, and
// reads one line, through the terminal's one key reader, and echoes it on
// echo. It returns the line with a newline. Backspace, ctrl+w and ctrl+u
// edit the line. esc and ctrl+c cancel, and so does ctrl+d on an empty
// line. Keys that send a sequence, such as the arrows, do nothing.
//
// It ends in the order that the picker ends in: its handler of signals
// starts before the raw mode, bracketed paste goes off, the terminal is
// restored, the handler stops, and a signal that came before the stop is
// a cancel (errPromptSignal), never an answer.
func readTerminalLine(ctx context.Context, f *os.File, echo io.Writer, q string) (string, error) {
	keys, err := keyread.For(f)
	if err != nil {
		return "", terminalReadError(err)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, promptSignals...)
	defer signal.Stop(sigs)
	restore, err := keys.Raw()
	if err != nil {
		return "", terminalReadError(err)
	}
	// A panic restores the terminal too, before the handler stops. The
	// restore runs once: the call below keeps the order of a normal end.
	defer func() { _ = restore() }()
	// Bracketed paste is on while the line is read (pasteOn). It goes off
	// before the restore, once, on a panic too.
	pasteEnd := func() {}
	if pasteModes && os.Getenv("TERM") != "dumb" {
		_, _ = io.WriteString(echo, pasteOn)
		pasteEnd = sync.OnceFunc(func() { _, _ = io.WriteString(echo, pasteOff) })
		defer pasteEnd()
	}
	// Raw mode turns off the terminal's own carriage return at a new line.
	_, _ = io.WriteString(echo, strings.ReplaceAll(q, "\n", "\r\n"))
	line, err := editLine(ctx, keys, sigs, echo)
	pasteEnd()
	_ = restore()
	promptStage("restored")
	// When Stop returns, any signal that came before it is in sigs.
	signal.Stop(sigs)
	select {
	case <-sigs:
		err = errPromptSignal
	default:
	}
	if errors.Is(err, errPromptSignal) {
		// answerOn makes it errInterrupted, a cancel that exits with 130.
		// interrupted stays as it is: listen closes it, and only after it
		// has ended the command's context (endAtSignal).
		return "", errPromptSignal
	}
	return line, err
}

// editLine reads keys from a terminal in raw mode until the line is done
// or cancelled, and echoes it on echo. A signal in sigs while it reads ends
// it with errPromptSignal. It returns once it has stopped looking at sigs,
// so that a signal after it stays in sigs for its caller.
func editLine(ctx context.Context, keys *keyread.Reader, sigs <-chan os.Signal, echo io.Writer) (string, error) {
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	var got atomic.Bool
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-sigs:
			got.Store(true)
			stop()
		case <-rctx.Done():
		}
	}()
	line, err := editKeys(rctx, keys, echo)
	stop()
	<-watched
	if got.Load() {
		return "", errPromptSignal
	}
	return line, err
}

// editKeys reads keys until the line is done or cancelled.
func editKeys(ctx context.Context, keys *keyread.Reader, echo io.Writer) (string, error) {
	var e lineEditor
	for {
		k, err := keys.ReadKey(ctx)
		if err != nil {
			fmt.Fprint(echo, "\r\n")
			if ctx.Err() != nil {
				// answerOn sees the cancel or the signal, and says so.
				return "", ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return "", cancelled()
			}
			return "", fmt.Errorf("read the answer: %w", err)
		}
		out, done, cancel := e.key(k)
		fmt.Fprint(echo, out)
		switch {
		case cancel:
			fmt.Fprint(echo, "\r\n")
			return "", cancelled()
		case done:
			fmt.Fprint(echo, "\r\n")
			return string(e.line) + "\n", nil
		}
	}
}

// lineEditor holds the line that a question reads, and edits it with the
// keys.
type lineEditor struct {
	line []rune
}

// key takes one key. It returns what to echo, and whether the line is done
// or cancelled.
func (e *lineEditor) key(k keyread.Key) (echo string, done, cancel bool) {
	switch k.Kind {
	case keyread.Enter:
		return "", true, false
	case keyread.Esc, keyread.CtrlC:
		return "", false, true
	case keyread.CtrlD:
		return "", false, len(e.line) == 0
	case keyread.Backspace:
		return e.erase(1), false, false
	case keyread.CtrlU:
		return e.erase(len(e.line)), false, false
	case keyread.CtrlW:
		return e.erase(e.lastWord()), false, false
	case keyread.Rune:
		e.line = append(e.line, k.Rune)
		return string(k.Rune), false, false
	case keyread.Paste:
		// A paste is text: a line break in it never ends the line.
		text := keyread.OneLine(k.Text)
		e.line = append(e.line, []rune(text)...)
		return text, false, false
	case keyread.Other, keyread.CtrlP, keyread.CtrlN, keyread.Up, keyread.Down:
		// Another key, such as an arrow, does nothing.
	}
	return "", false, false
}

// erase removes the last n runes of the line, and returns what erases them
// on the screen.
func (e *lineEditor) erase(n int) string {
	var b strings.Builder
	for ; n > 0 && len(e.line) > 0; n-- {
		w := max(1, ansi.StringWidth(string(e.line[len(e.line)-1])))
		e.line = e.line[:len(e.line)-1]
		b.WriteString(strings.Repeat("\b", w) + strings.Repeat(" ", w) + strings.Repeat("\b", w))
	}
	return b.String()
}

// lastWord is the number of runes that ctrl+w erases: the spaces at the end
// of the line, and the word before them.
func (e *lineEditor) lastWord() int {
	i := len(e.line)
	for i > 0 && e.line[i-1] == ' ' {
		i--
	}
	for i > 0 && e.line[i-1] != ' ' {
		i--
	}
	return len(e.line) - i
}

// newKeyReader opens a terminal for the key tester and the terminal check,
// so that a cancel can stop a read that waits. Tests replace it. A
// question never uses it: it reads through keyread.For.
var newKeyReader = func(f *os.File) (cancelreader.CancelReader, error) { return cancelreader.NewReader(f) }

// inputOf is in as a reader whose waiting read a cancel may stop: one of
// newKeyReader on a terminal, else in as it is, which no cancel stops. It
// also returns what finishes its ownership. A read that a cancel could not
// stop, or whose cancel reported success without ending it, stays open and
// is registered until it actually ends.
func inputOf(in io.Reader) (keys.CancelReader, func(), error) {
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		if err := keyread.CheckStuck(f); err != nil {
			return nil, nil, terminalReadError(err)
		}
		cr, err := newKeyReader(f)
		if err != nil {
			return nil, nil, terminalReadError(err)
		}
		s := &stoppable{cr: cr}
		return s, func() { s.finish(f) }, nil
	}
	return plainInput{in}, func() {}, nil
}

// stoppable is a terminal's reader that tracks in-flight reads and registers
// HoldStuck when a read cannot be cancelled, so it is never closed under an
// active read and no later reader starts on the same terminal.
type stoppable struct {
	cr         cancelreader.CancelReader
	mu         sync.Mutex
	reading    bool
	stopping   bool
	readDone   chan struct{}
	cancelOnce sync.Once
	cancelOK   bool
	finishOnce sync.Once
	closeOnce  sync.Once
}

func (s *stoppable) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return 0, cancelreader.ErrCanceled
	}
	done := make(chan struct{})
	s.reading, s.readDone = true, done
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.reading = false
		close(done)
		s.mu.Unlock()
	}()
	return s.cr.Read(p) //nolint:wrapcheck // a reader passes its reader's errors on as they are
}

func (s *stoppable) Cancel() bool {
	s.cancelOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		s.mu.Unlock()
		s.cancelOK = s.cr.Cancel()
	})
	return s.cancelOK
}

func (s *stoppable) finish(f *os.File) {
	s.finishOnce.Do(func() {
		cancelled := s.Cancel()
		s.mu.Lock()
		reading, done := s.reading, s.readDone
		s.mu.Unlock()
		if !reading {
			_ = s.close()
			return
		}
		cause := keyread.ErrCannotCancel
		if cancelled {
			cause = keyread.ErrReadGoesOn
		}
		keyread.HoldStuck(f, s.close, done, cause)
	})
}

func (s *stoppable) close() error {
	s.closeOnce.Do(func() { _ = s.cr.Close() })
	return nil
}

// plainInput is a reader that no cancel stops.
type plainInput struct{ io.Reader }

func (plainInput) Cancel() bool { return false }

// oneByte reads at most one byte at a time.
type oneByte struct{ keys.CancelReader }

func (o oneByte) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return o.CancelReader.Read(p) //nolint:wrapcheck // a reader passes its reader's errors on as they are
}

// stoppingWatch is called as stopping's watcher of signals starts. Tests
// hold it there, so that only stopping's own look at a signal that has
// come can end its context.
var stoppingWatch = func() {}

// stopping is ctx, which also ends at a signal.
func stopping(ctx context.Context) (context.Context, context.CancelFunc) {
	sctx, cancel := context.WithCancel(ctx)
	if signalled() {
		// A signal that came before: sctx has ended already, at once.
		cancel()
		return sctx, cancel
	}
	// The caller reads the hook, so a later test's hook never races with
	// a watcher that an earlier test started.
	signal, watch := interrupted, stoppingWatch
	go func() {
		watch()
		select {
		case <-signal:
			cancel()
		case <-sctx.Done():
		}
	}()
	return sctx, cancel
}

// stopped reports whether ctx has ended or a signal has come.
func stopped(ctx context.Context) bool {
	return ctx.Err() != nil || signalled()
}

// signalled reports whether a signal has come.
func signalled() bool {
	select {
	case <-interrupted:
		return true
	default:
		return false
	}
}
