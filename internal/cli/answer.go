package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keys"
)

// The one reader of what a person types at a question
// (docs/contributing/cli-spec.md 5.1, rule 7). Every read of the terminal
// in this package goes through answer, or through inputOf and
// keys.ReadUntil as the key tester's does: a test fails on a direct read of
// os.Stdin anywhere else (TestNoDirectReadOfStdin).

// errInterrupted is the error of a question that a signal ended. It is a
// cancel, never an answer: a caller stops at it, before it keeps or acts
// on anything.
var errInterrupted = &client.UserError{Msg: "a signal ended the command at a question, so the question has no answer",
	Hint: "Run the command again.", Code: "cancelled", Kind: client.KindCancelled}

// beforeChoice runs in answer when the line has come, before answer looks
// for a cancel once more. Tests set it to make a cancel come with the line.
var beforeChoice = func() {}

// afterAnswer runs in answer when it gives a line back. Tests set it to
// make a cancel come after the answer, before the caller acts on it: a
// caller that writes something checks once more.
var afterAnswer = func(line string) {}

// answer reads one line of an answer from in, with its newline, a byte at
// a time, so that it takes nothing after the newline: an attach that
// follows gets every byte. A closed input gives what came before it, or "".
// It gives up, with errInterrupted, when ctx ends or a signal comes: before
// the read, while it waits, and when the line comes with them, since a
// cancel that it has seen wins. Its read runs in its own goroutine, so a
// read that cannot be cancelled, as on some Windows consoles, never keeps
// it.
func answer(ctx context.Context, in io.Reader) (string, error) {
	if stopped(ctx) {
		return "", errInterrupted
	}
	r, done := inputOf(in)
	defer done()
	sctx, cancel := stopping(ctx)
	defer cancel()
	var line []byte
	err := keys.ReadUntil(sctx, oneByte{r}, func(b []byte) bool {
		line = append(line, b...)
		return line[len(line)-1] != '\n'
	})
	beforeChoice()
	if err != nil || stopped(ctx) {
		return "", errInterrupted
	}
	afterAnswer(string(line))
	return string(line), nil
}

// question asks a yes-or-no question on w and reads the answer from in:
// anything but yes is no. A cancel is neither, and returns errInterrupted.
func question(ctx context.Context, in io.Reader, w io.Writer, q string) (bool, error) {
	_, _ = io.WriteString(w, q+" [y/N] ")
	line, err := answer(ctx, in)
	if err != nil {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

// confirm asks a yes-or-no question on the terminal.
func confirm(ctx context.Context, q string) (bool, error) {
	return question(ctx, os.Stdin, os.Stdout, q)
}

// readAnswer reads one line of an answer from r, as answer does, until a
// signal.
func readAnswer(r *bufio.Reader) (string, error) {
	return answer(context.Background(), r)
}

// ask asks a yes-or-no question, as question does, until a signal.
func ask(r *bufio.Reader, w io.Writer, q string) (bool, error) {
	return question(context.Background(), r, w, q)
}

// newKeyReader opens a terminal for answer and the key tester, so that a
// cancel can stop a read that waits. Tests replace it.
var newKeyReader = func(f *os.File) (cancelreader.CancelReader, error) { return cancelreader.NewReader(f) }

// inputOf is in as a reader whose waiting read a cancel may stop: one of
// newKeyReader on a terminal, else in as it is, which no cancel stops. It
// also returns what closes it. keys.ReadUntil waits for a read that a
// cancel stopped, so the reader is closed only after its last read. A read
// that a cancel could not stop may still run, so then the reader stays
// open.
func inputOf(in io.Reader) (keys.CancelReader, func()) {
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		if cr, err := newKeyReader(f); err == nil {
			s := &stoppable{CancelReader: cr}
			return s, func() {
				if !s.stuck.Load() {
					_ = cr.Close()
				}
			}
		}
	}
	return plainInput{in}, func() {}
}

// stoppable is a terminal's reader that remembers a cancel that failed:
// its read may then still run, and it must not be closed.
type stoppable struct {
	cancelreader.CancelReader
	stuck atomic.Bool
}

func (s *stoppable) Cancel() bool {
	ok := s.CancelReader.Cancel()
	if !ok {
		s.stuck.Store(true)
	}
	return ok
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

// stopping is ctx, which also ends at a signal.
func stopping(ctx context.Context) (context.Context, context.CancelFunc) {
	sctx, cancel := context.WithCancel(ctx)
	signal := interrupted
	go func() {
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
