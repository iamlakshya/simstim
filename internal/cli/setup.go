package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// startSession asks through the command's input policy while preserving the
// content-bound setup answers. A changed script asks again. With input off,
// a missing folder needs --mkdir and an untrusted script is skipped.
func startSession(cmd *cobra.Command, conn *client.Conn, h client.Host, a client.NewArgs) (*client.Session, error) {
	session, err, _ := startSessionOwnedV1(cmd, conn, h, a, nil)
	return session, err
}

func startSessionOnAttemptV1(cmd *cobra.Command, attempt *client.HostAttemptV1, a client.NewArgs) (*client.Session, error, bool) {
	return startSessionOwnedV1(cmd, attempt.Conn(), attempt.Host(), a, attempt)
}

func startSessionOwnedV1(cmd *cobra.Command, conn *client.Conn, h client.Host, a client.NewArgs, attempt *client.HostAttemptV1) (*client.Session, error, bool) {
	var answers client.SetupAnswers
	warnings := stderrFor(jsonOn(cmd), "setup_answers")
	if a.Setup == "" {
		answers = client.LoadSetupAnswers(h.Name)
		printWarning(warnings, answers.Warning)
		answers.Apply(&a)
	}
	p := prompts(cmd)
	for {
		cctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		recordNewStartBudget(cmd.Context(), cctx)
		var s *client.Session
		var err error
		if attempt == nil {
			s, err = conn.NewSession(cctx, a)
		} else {
			s, err = attempt.NewSession(cctx, a)
		}
		cancel()
		if q, ok := client.FolderQuestionOf(err); ok && !a.MakeFolder {
			yes, err := askMakeFolder(p, h.Name, q)
			if err != nil {
				return nil, err, false
			}
			if !yes {
				return nil, p.cancel(), false
			}
			if stopped(p.ctx) {
				return nil, errInterrupted, false
			}
			a.MakeFolder = true
			continue
		}
		q, ok := client.SetupQuestionOf(err)
		if !ok || a.Setup != "" {
			return s, err, true
		}
		past := answers.Past(q)
		if q.Kept != "" {
			note(cmd, "worktree_kept", fmt.Sprintf("On %s: %s", h.Name, client.CleanPath(q.Kept)))
		}
		if !p.on {
			note(cmd, "setup_skipped", skipNote(h.Name, q, past))
			a.Setup = api.SetupSkip
			continue
		}
		run, err := askSetup(p, h.Name, q, past)
		if err != nil {
			return nil, err, false
		}
		if stopped(p.ctx) {
			return nil, errInterrupted, false
		}
		if err := rememberAnswer(p.ctx, warnings, h.Name, q, run); err != nil {
			return nil, err, false
		}
		a, answers = client.AnswerSetup(a, q, run), answers.Answered(q, run)
	}
}

// rememberAnswer keeps the hash that the user answered for, and reports a
// recovered answers file through the caller's human or JSON warning writer.
// Its owned context also ends at a signal, including one during a lock wait.
func rememberAnswer(ctx context.Context, w io.Writer, host string, q client.SetupQuestion, run bool) error {
	sctx, cancel := stopping(ctx)
	defer cancel()
	warn, err := client.RememberSetupContext(sctx, host, q.Repo, run, q.Hash)
	printWarning(w, warn)
	return err
}

func printWarning(w io.Writer, warn *client.UserError) {
	if warn != nil {
		fmt.Fprintf(w, "Warning: %s\nTry: %s\n", client.CleanLine(warn.Msg), client.CleanLine(warn.Hint))
	}
}

func setupValue(run bool) string {
	if run {
		return api.SetupRun
	}
	return api.SetupSkip
}

func skipNote(host string, q client.SetupQuestion, past client.SetupPast) string {
	host = shownShort(host)
	var why string
	switch past {
	case client.SetupChanged:
		why = "it changed after you trusted it on " + host
	case client.SetupUnchecked:
		why = "you trusted it on " + host + " before this version checked its content"
	case client.SetupNotTrusted:
		why = "you have not said that it may run on " + host
	}
	return fmt.Sprintf("Skipping the setup script of %s: %s. Pass --setup-script to run it.", client.ShowPath(q.Repo), why)
}

func askSetup(p *prompter, host string, q client.SetupQuestion, past client.SetupPast) (bool, error) {
	repo, script := client.ShowPath(q.Repo), client.ShowPath(q.Script)
	host = shownShort(host)
	if p.on {
		switch past {
		case client.SetupChanged:
			fmt.Fprintf(p.out, "The setup script of %s on %s changed after you trusted it:\n\n    %s\n\n", repo, host, script)
		case client.SetupUnchecked:
			fmt.Fprintf(p.out, "You trusted the setup script of %s on %s before this version checked its content:\n\n    %s\n\n", repo, host, script)
		case client.SetupNotTrusted:
			fmt.Fprintf(p.out, "The repository %s on %s has a setup script:\n\n    %s\n\n", repo, host, script)
		}
		fmt.Fprintf(p.out, "It runs in the session before the agent, as you, with the session's environment and\nsecrets. Say yes only for a repository you trust. The answer is kept for this\nrepository on %s until the script changes; --setup-script and --no-setup-script override it.\n\n", host)
	}
	return p.confirm(qSetupScript, "Run it?", "an answer for the setup script of "+repo)
}

func askMakeFolder(p *prompter, host string, q client.FolderQuestion) (bool, error) {
	folder := client.ShowPath(q.Folder)
	host = shownShort(host)
	if p.on {
		fmt.Fprintf(p.out, "The folder %s does not exist on %s.\n", folder, host)
	}
	return p.confirm(qMakeFolder, "Make it?", fmt.Sprintf("a yes to make the folder %s, which does not exist on %s", folder, host))
}
