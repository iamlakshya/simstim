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
// it), and a setup script with no answer, or one that changed after the
// user's yes, is skipped this time, with a note.
func startSession(ctx context.Context, conn *client.Conn, h client.Host, a client.NewArgs) (*client.Session, error) {
	var answers client.SetupAnswers
	if a.Setup == "" {
		answers = client.LoadSetupAnswers(h.Name)
		printWarning(os.Stderr, answers.Warning)
		answers.Apply(&a)
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
		past := answers.Past(q)
		if q.Kept != "" {
			fmt.Fprintf(os.Stderr, "On %s: %s\n", h.Name, client.CleanPath(q.Kept))
		}
		if !interactive {
			fmt.Fprintln(os.Stderr, skipNote(h.Name, q, past))
			a.Setup = api.SetupSkip
			continue
		}
		run := askSetup(os.Stdin, os.Stdout, h.Name, q, past)
		if err := rememberAnswer(os.Stderr, h.Name, q, run); err != nil {
			return nil, err
		}
		// If the script changes again before the next try, the question
		// says that it changed after the user trusted it.
		a, answers = client.AnswerSetup(a, q, run), answers.Answered(q, run)
	}
}

// rememberAnswer keeps the user's answer to q on host. A warning about the
// answers file, such as one that another program broke while the question
// was open, goes to w. A failure to keep the answer is its error.
func rememberAnswer(w io.Writer, host string, q client.SetupQuestion, run bool) error {
	warn, err := client.RememberSetup(host, q.Repo, run, q.Hash)
	printWarning(w, warn)
	return err
}

// printWarning writes a warning about the answers file, if there is one.
func printWarning(w io.Writer, warn *client.UserError) {
	if warn != nil {
		fmt.Fprintf(w, "Warning: %s\nTry: %s\n", warn.Msg, warn.Hint)
	}
}

func setupValue(run bool) string {
	if run {
		return api.SetupRun
	}
	return api.SetupSkip
}

// skipNote says that a setup script is skipped this time, when there is no
// terminal to ask on, and why, from what the user said before. The
// repository comes from the host, so it is shown cleaned, or quoted when it
// holds hidden characters (client.ShowPath).
func skipNote(host string, q client.SetupQuestion, past client.SetupPast) string {
	var why string
	switch past {
	case client.SetupChanged:
		why = "it changed after you trusted it on " + host
	case client.SetupUnchecked:
		why = "you trusted it on " + host + " before this version checked its content"
	case client.SetupNotTrusted:
		why = "you have not said that it may run on " + host
	}
	return fmt.Sprintf("Skipping the setup script of %s: %s. Pass --setup to run it.", client.ShowPath(q.Repo), why)
}

// askSetup asks whether a repository's setup script may run on host. Its
// first line follows what the user said before. The repository and the
// script come from the host, so they are shown cleaned, or quoted when they
// hold hidden characters (client.ShowPath).
func askSetup(in io.Reader, w io.Writer, host string, q client.SetupQuestion, past client.SetupPast) bool {
	repo, script := client.ShowPath(q.Repo), client.ShowPath(q.Script)
	switch past {
	case client.SetupChanged:
		fmt.Fprintf(w, "The setup script of %s on %s changed after you trusted it:\n\n    %s\n\n", repo, host, script)
	case client.SetupUnchecked:
		fmt.Fprintf(w, "You trusted the setup script of %s on %s before this version checked its content:\n\n    %s\n\n", repo, host, script)
	case client.SetupNotTrusted:
		fmt.Fprintf(w, "The repository %s on %s has a setup script:\n\n    %s\n\n", repo, host, script)
	}
	fmt.Fprintf(w, "It runs in the session before the agent, as you, with the session's environment and\nsecrets. Say yes only for a repository you trust. The answer is kept for this\nrepository on %s until the script changes; --setup and --no-setup override it.\n\n", host)
	return ask(bufio.NewReader(in), w, "Run it?")
}

// askMakeFolder asks whether a new session's missing folder may be made on host.
func askMakeFolder(in io.Reader, w io.Writer, host string, q client.FolderQuestion) bool {
	fmt.Fprintf(w, "The folder %s does not exist on %s.\n", client.ShowPath(q.Folder), host)
	return ask(bufio.NewReader(in), w, "Make it?")
}
