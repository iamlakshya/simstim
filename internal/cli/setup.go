package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// startSession starts a session, and asks on this terminal what the host
// needs to know first: whether a missing folder may be made, and whether a
// repository's setup script may run on h. Each answer starts it again. It
// asks only when stdin and stdout are both terminals, so that a person sees
// the question. Otherwise a missing folder stays an error (--mkdir answers
// it), and a setup script with no answer is skipped this time, with a note.
func startSession(ctx context.Context, conn *client.Conn, h client.Host, a client.NewArgs) (*client.Session, error) {
	if a.Setup == "" {
		a.SetupTrust = client.SetupTrust(h.Name)
	}
	interactive := isTerminal(os.Stdin) && isTerminal(os.Stdout)
	for {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		s, err := conn.NewSession(cctx, a)
		cancel()
		if q, ok := client.FolderQuestionOf(err); ok && !a.MakeFolder {
			if !interactive || !askMakeFolder(os.Stdin, os.Stdout, h.Name, q) {
				return nil, err
			}
			a.MakeFolder = true
			continue
		}
		q, ok := client.SetupQuestionOf(err)
		if !ok || a.Setup != "" {
			return s, err
		}
		if interactive {
			run := askSetup(os.Stdin, os.Stdout, h.Name, q)
			if err := client.RememberSetup(h.Name, q.Repo, run); err != nil {
				return nil, err
			}
			a.Setup = setupValue(run)
		} else {
			fmt.Fprintf(os.Stderr, "Skipping the setup script of %s: you have not said that it may run on %s. Pass --setup to run it.\n", q.Repo, h.Name)
			a.Setup = api.SetupSkip
		}
	}
}

func setupValue(run bool) string {
	if run {
		return api.SetupRun
	}
	return api.SetupSkip
}

// askSetup asks whether a repository's setup script may run on host.
func askSetup(in io.Reader, w io.Writer, host string, q client.SetupQuestion) bool {
	fmt.Fprintf(w, "The repository %s on %s has a setup script:\n\n    %s\n\n", q.Repo, host, q.Script)
	fmt.Fprintf(w, "It runs in the session before the agent, as you, with the session's environment and\nsecrets. Say yes only for a repository you trust. The answer is kept for this\nrepository on %s; --setup and --no-setup override it.\n\n", host)
	return ask(bufio.NewReader(in), w, "Run it?")
}

// askMakeFolder asks whether a new session's missing folder may be made on host.
func askMakeFolder(in io.Reader, w io.Writer, host string, q client.FolderQuestion) bool {
	fmt.Fprintf(w, "The folder %s does not exist on %s.\n", q.Folder, host)
	return ask(bufio.NewReader(in), w, "Make it?")
}
