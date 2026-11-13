//go:build !windows

package cli

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

// Both terminal commands must restore raw mode after a cancellation request
// reports success but leaves its read waiting. The waiting reader stays owned
// until completion, and the shared registry refuses a competing terminal read.
func TestTerminalCommandsRestoreModesAfterClaimedCancellation(t *testing.T) {
	for _, command := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"keys", keyTester},
		{"terminal", terminalProbe},
	} {
		t.Run(command.name, func(t *testing.T) {
			newTestHost(t)
			withSignal(t)
			terminal := hosttest.NewTerminal(t, 90, 30)
			before, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			oldIn, oldOut, oldReader := os.Stdin, os.Stdout, newKeyReader
			os.Stdin, os.Stdout = terminal.TTY, terminal.TTY
			r := newHeldKeyReader(true)
			opens := 0
			newKeyReader = func(*os.File) (cancelreader.CancelReader, error) {
				opens++
				return r, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			ended := make(chan struct{})
			go func() {
				defer close(ended)
				result <- command.run(ctx)
			}()
			t.Cleanup(func() {
				r.letGo()
				<-ended
				select {
				case <-r.started:
					<-r.readEnded
				default:
				}
				_ = keyread.Release(terminal.TTY)
				os.Stdin, os.Stdout, newKeyReader = oldIn, oldOut, oldReader
			})
			select {
			case <-r.started:
			case <-time.After(5 * time.Second):
				t.Fatal("the command never began its terminal read")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("terminal command = %v, want cancellation", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("terminal command stayed in raw mode after its release deadline")
			}
			<-ended
			after, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Errorf("terminal modes did not restore: before=%+v after=%+v err=%v", before, after, err)
			}
			if command.name == "keys" {
				terminal.WaitFor("\x1b[<u\x1b[?2004l")
			}
			if r.closes.Load() != 0 || r.closedDuringRead.Load() || r.cancels.Load() != 1 {
				t.Errorf("blocked reader: closes=%d under-read=%v cancels=%d", r.closes.Load(), r.closedDuringRead.Load(), r.cancels.Load())
			}
			if err := keyread.CheckStuck(terminal.TTY); !errors.Is(err, keyread.ErrReadGoesOn) {
				t.Errorf("CheckStuck = %v, want ErrReadGoesOn", err)
			}
			reader, closeFn, err := inputOf(terminal.TTY)
			var ue *client.UserError
			if reader != nil || closeFn != nil || !errors.As(err, &ue) || ue.Code != "terminal_busy" || opens != 1 {
				t.Errorf("a competing reader opened: reader=%v err=%v opens=%d", reader, err, opens)
			}
			r.letGo()
			<-r.readEnded
			hosttest.WaitFor(t, 5*time.Second, "the terminal read to finish", func() bool {
				return keyread.CheckStuck(terminal.TTY) == nil
			})
			if r.closes.Load() != 1 || r.closedDuringRead.Load() {
				t.Errorf("released reader: closes=%d under-read=%v", r.closes.Load(), r.closedDuringRead.Load())
			}
		})
	}
}
