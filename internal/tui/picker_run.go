package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keyread"
)

// PickOptions are the picker's rows and its terminal.
type PickOptions struct {
	// Title names the command that asks, such as "attach".
	Title string
	// Hosts are the hosts that Refresh asks, for the line while it looks.
	Hosts []string
	// Rows are the sessions to show first, from the session cache.
	Rows []PickRow
	// Refresh, if set, asks the hosts for their sessions. Its rows replace
	// Rows. Its error, when it has no rows, is why no host answered.
	Refresh func(context.Context) ([]PickRow, error)
	// In and Out are the terminal.
	In, Out *os.File
}

// pickSignals close the picker. In raw mode ctrl+c is a key, so these come
// from outside: kill, a closed terminal, or a quit.
var pickSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}

// pickStage is called at each stage of Pick. Tests send a signal at a
// stage of their choice.
var pickStage = func(string) {}

// Pick opens the picker, and returns the row picked. Esc, ctrl+c or a
// signal give ErrPickCancelled. Hosts that answer with no session give
// ErrNoSessions, and hosts that do not answer, with no session in the
// cache, give their error.
//
// The picker sends the terminal no query, so no reply can reach the
// command that goes on. Bubble Tea runs with no input of its own: then it
// sends none of its queries. The picker reads the keys itself, a key at a
// time, and reads no further than the key that closes it, so what the
// user types after the pick stays in the terminal. Only the last block of
// a paste can hold keys typed within moments of it (keyread). It reads
// through the terminal's one reader (keyread.For), which the questions
// share, and the terminal goes back to the state that keyread saved before
// any reader touched it. The signal handler starts before the raw mode,
// and stops after the terminal is restored. A signal at any point gives
// ErrPickCancelled.
func Pick(ctx context.Context, o PickOptions) (PickRow, error) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, pickSignals...)
	defer signal.Stop(sigs)
	pickStage("start")
	keys, err := keyread.For(o.In)
	if err != nil {
		return PickRow{}, pickerTerminalReadError(err)
	}
	restore, err := keys.Raw()
	if err != nil {
		return PickRow{}, pickerTerminalReadError(err)
	}
	// A panic restores the terminal too, before the handler stops. The
	// restore runs once: the call below keeps the order of a normal end.
	defer func() { _ = restore() }()
	pickStage("raw")
	var signalled atomic.Bool
	row, err := pick(ctx, o, keys, sigs, &signalled)
	pickStage("picked")
	_ = restore()
	pickStage("restored")
	// When Stop returns, any signal that came before it is in sigs.
	signal.Stop(sigs)
	select {
	case <-sigs:
		signalled.Store(true)
	default:
	}
	// A signal or the end of ctx at any point, also after a pick and after
	// the restore, is a cancel: never a pick.
	if signalled.Load() || ctx.Err() != nil {
		return PickRow{}, ErrPickCancelled
	}
	return row, err
}

type pickerTerminalError struct {
	*client.UserError
	cause error
}

func (e pickerTerminalError) Unwrap() []error { return []error{e.UserError, e.cause} }

func pickerTerminalReadError(err error) error {
	ue := &client.UserError{
		Msg:  "cannot read the terminal safely: " + err.Error(),
		Hint: "Open a new terminal, or name the session on the command line.",
		Code: "terminal_failed",
	}
	if errors.Is(err, keyread.ErrStuck) {
		ue.Msg = "the terminal's reader from an earlier session is still running"
		ue.Code = "terminal_busy"
	}
	return pickerTerminalError{UserError: ue, cause: err}
}

// pickProfile is the picker's colour profile on out. NO_COLOR with any
// value but the empty one, and TERM=dumb, turn colour off, as everywhere in
// the client (cli-spec 7.5); --no-color sets NO_COLOR. Text styles, such as
// reverse, stay.
func pickProfile(out *os.File, getenv func(string) string) colorprofile.Profile {
	if getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return colorprofile.Ascii
	}
	return colorprofile.Detect(out, os.Environ())
}

// pick runs the picker on a terminal in raw mode, with its keys.
func pick(ctx context.Context, o PickOptions, keys *keyread.Reader, sigs <-chan os.Signal, signalled *atomic.Bool) (PickRow, error) {
	p := newPicker(o.Title, o.Rows, time.Now)
	p.hosts, p.loading = o.Hosts, o.Refresh != nil
	p.handled = make(chan bool, 1)
	// With no input, Bubble Tea sends no query: not the mode reports for
	// synchronized output and unicode core, not the keyboard enhancements,
	// and not the colour, cursor, clipboard and version requests. The colour
	// profile follows the colour rules, then the environment and terminfo.
	// Its signal handler is off: Pick has its own, which outlives the raw
	// mode.
	prog := tea.NewProgram(p,
		tea.WithInput(nil),
		tea.WithOutput(newlines{o.Out}),
		tea.WithColorProfile(pickProfile(o.Out, os.Getenv)),
		tea.WithoutSignalHandler(),
	)

	rctx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	if o.Refresh != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := o.Refresh(rctx)
			if rctx.Err() == nil {
				prog.Send(pickedRows{rows: rows, err: err})
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		select {
		case <-sigs:
			signalled.Store(true)
			prog.Send(pickCancelled{})
		case <-rctx.Done():
			prog.Send(pickCancelled{})
		}
	}()
	go func() {
		defer wg.Done()
		feedKeys(rctx, keys, prog, p.handled)
	}()

	_, err := prog.Run()
	stop()
	wg.Wait()

	switch {
	case p.chosen != nil:
		return *p.chosen, nil
	case p.failed != nil:
		return PickRow{}, p.failed
	case err == nil, ctx.Err() != nil, errors.Is(err, tea.ErrInterrupted), errors.Is(err, tea.ErrProgramKilled):
		return PickRow{}, ErrPickCancelled
	}
	return PickRow{}, fmt.Errorf("run the picker: %w", err)
}

// feedKeys gives the picker its keys, one at a time. After each key it
// waits for the picker to take it, and it stops once the picker is done,
// so it never reads a key past the one that closed the picker. A terminal
// that cannot be read closes the picker.
func feedKeys(ctx context.Context, keys *keyread.Reader, prog *tea.Program, handled <-chan bool) {
	for {
		k, err := keys.ReadKey(ctx)
		if err != nil {
			prog.Send(pickCancelled{})
			return
		}
		msg, ok := teaKey(k)
		if !ok {
			continue
		}
		prog.Send(msg)
		select {
		case done := <-handled:
			if done {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// teaKey is the picker's message for k, and false for a key that the
// picker does not use. A paste is one message, so a line break in it never
// picks a row.
func teaKey(k keyread.Key) (tea.Msg, bool) {
	switch k.Kind {
	case keyread.Rune:
		return tea.KeyPressMsg{Code: k.Rune, Text: string(k.Rune)}, true
	case keyread.Enter:
		return tea.KeyPressMsg{Code: tea.KeyEnter}, true
	case keyread.Esc:
		return tea.KeyPressMsg{Code: tea.KeyEscape}, true
	case keyread.CtrlC:
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, true
	case keyread.Backspace:
		return tea.KeyPressMsg{Code: tea.KeyBackspace}, true
	case keyread.CtrlU:
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, true
	case keyread.Up, keyread.CtrlP:
		return tea.KeyPressMsg{Code: tea.KeyUp}, true
	case keyread.Down, keyread.CtrlN:
		return tea.KeyPressMsg{Code: tea.KeyDown}, true
	case keyread.Paste:
		return tea.PasteMsg{Content: k.Text}, true
	case keyread.Other, keyread.CtrlD, keyread.CtrlW:
	}
	return nil, false
}

// newlines is the picker's output. Bubble Tea with no input takes it that
// the terminal turns a new line into a carriage return and a new line, as
// it does out of raw mode, except on Windows. The picker's raw mode turns
// that off, so newlines does it instead.
type newlines struct{ f *os.File }

func (n newlines) Write(b []byte) (int, error) {
	if runtime.GOOS == "windows" || bytes.IndexByte(b, '\n') < 0 {
		return n.f.Write(b) //nolint:wrapcheck // Bubble Tea says what it was writing
	}
	if _, err := n.f.Write(bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err //nolint:wrapcheck // as above
	}
	return len(b), nil
}

func (n newlines) Read(b []byte) (int, error) { return n.f.Read(b) } //nolint:wrapcheck // not read
func (n newlines) Close() error               { return nil }
func (n newlines) Fd() uintptr                { return n.f.Fd() }
