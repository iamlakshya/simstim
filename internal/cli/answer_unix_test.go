//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A cancel at the question of a setup script keeps no answer: one that
// comes with the yes, and one that comes while the input is blocked.
func TestAnAnswerThatMeetsASignalKeepsNothing(t *testing.T) {
	hosttest.New(t)
	withSignal(t)
	_, tr := withFake(t, nil)
	if err := client.SaveHostsContext(context.Background(), []client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
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
