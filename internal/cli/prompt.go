package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// Input and prompts (docs/contributing/cli-spec.md 6.1 and 6.2). Every
// question that a command asks goes through this file: it shows the
// default in capitals, reads one line from the terminal, and never asks
// when input is off.

// noInputEnv is the variable that turns input off.
var noInputEnv = brand.Env("NO_INPUT")

// inputOn reports whether cmd may ask: stdin, stdout and stderr are
// terminals, and the user did not turn input off.
func inputOn(cmd *cobra.Command) bool {
	return !inputOff(cmd) && isTerminal(os.Stdin) && isTerminal(os.Stdout) && isTerminal(os.Stderr)
}

// inputOff reports whether the user turned input off: with --no-input, with
// --json (question C-13), or with the variable set to anything but "", "0"
// or "false".
func inputOff(cmd *cobra.Command) bool {
	if envTurnsInputOff(os.Getenv(noInputEnv)) {
		return true
	}
	return cmd != nil && (boolFlag(cmd, "no-input") || jsonOn(cmd))
}

func envTurnsInputOff(v string) bool { return v != "" && v != "0" && v != "false" }

// secretTerminal reports whether a secret's command may use the terminal,
// for a password manager that asks: input is not turned off, and stdin and
// stderr are terminals. stdout may go to a file. The command's stderr then
// reaches the user's.
func secretTerminal(cmd *cobra.Command) bool {
	return !inputOff(cmd) && isTerminal(os.Stdin) && isTerminal(os.Stderr)
}

// boolFlag is the value of a bool flag of cmd, or false when cmd has none.
func boolFlag(cmd *cobra.Command, name string) bool {
	v, err := cmd.Flags().GetBool(name)
	return err == nil && v
}

// question is one row of the prompt table of cli-spec 6.2: a question that
// a command asks, and the flag that answers it. A test checks that each row
// is in the spec's table, and that its flag exists on its command.
type question struct {
	// cmd is the command that asks, such as "host add".
	cmd string
	// flag answers the question, as the user types it, such as "--yes".
	flag string
	// def is the answer that Enter gives.
	def bool
	// keep is true when the default changes nothing that the user did not
	// ask for: with input off, the command takes it and says so. Otherwise
	// the command fails with exit code 4, and names flag.
	keep bool
}

// The questions that commands ask today. A task that adds a prompt adds its
// row here, and to the spec's table.
var (
	qInstallHelper = &question{cmd: "host add", flag: "--yes"}
	qHostKey       = &question{cmd: "host add", flag: "--host-key"}
	qHostRekey     = &question{cmd: "host rekey", flag: "--confirm"}
	qAddAgents     = &question{cmd: "host add", flag: "--yes", keep: true}
	qMakeFolder    = &question{cmd: "new", flag: "--mkdir"}
	qSetupScript   = &question{cmd: "new", flag: "--setup", keep: true}
	qFix           = &question{cmd: "doctor", flag: "-y"}
	qHostFix       = &question{cmd: "host doctor", flag: "-y"}
	qDestroy       = &question{cmd: "host destroy", flag: "-y"}
	qSetup         = &question{cmd: "setup", flag: "--yes"}
)

// questions lists every row, for the tests.
var questions = []*question{qInstallHelper, qHostKey, qHostRekey, qAddAgents, qMakeFolder, qSetupScript, qFix, qHostFix, qDestroy, qSetup}

// prompter asks a command's questions on stderr, and reads the answers from
// stdin.
type prompter struct {
	ctx context.Context
	in  io.Reader
	out io.Writer
	// on is true when input is on.
	on bool
	// line is the command line as the user typed it, and flagsAt where a
	// flag goes in it: before "--". The Try: line of a question that input
	// off leaves without an answer is this line with the flag that answers.
	line    []string
	flagsAt int
	// path is the command's path, for a line that is not known.
	path string
	// log lists what earlier steps changed, for the line of a cancel. Steps
	// add to it as they change something, so a cancel at any point lists
	// all of it.
	log *onboard.Log
}

// prompts is the prompter of cmd.
func prompts(cmd *cobra.Command) *prompter {
	line := commandLine(cmd)
	return &prompter{ctx: cmd.Context(), in: os.Stdin, out: os.Stderr, on: inputOn(cmd),
		line: line, flagsAt: flagsAt(cmd, line), path: cmd.CommandPath()}
}

// withFlags is p's command line with flags added before any "--", as a
// command that runs as written.
func (p *prompter) withFlags(flags ...string) string {
	if len(p.line) == 0 {
		return strings.Join(append([]string{p.path}, flags...), " ")
	}
	at := min(p.flagsAt, len(p.line))
	// The user's words are quoted where a shell needs it. The flags are
	// ours, placeholders such as <host> included, and stay as they are.
	parts := []string{brand.Name}
	if at > 0 {
		parts = append(parts, joinWords(p.line[:at]))
	}
	parts = append(parts, flags...)
	if at < len(p.line) {
		parts = append(parts, joinWords(p.line[at:]))
	}
	return strings.Join(parts, " ")
}

// flagsAt is where a flag goes in line, as the flags of cmd read it: at
// the "--" that ends the flags, or at the end.
func flagsAt(cmd *cobra.Command, line []string) int {
	values := map[int]bool{}
	for _, w := range words(cmd.Flags(), line) {
		if w.flag != "" {
			values[w.at] = true
		}
	}
	for i, w := range line {
		if w == "--" && !values[i] {
			return i
		}
	}
	return len(line)
}

// changes is p's log of changes, for a step that changes something.
func (p *prompter) changes() *onboard.Log {
	if p.log == nil {
		p.log = &onboard.Log{}
	}
	return p.log
}

// cancel is the cancel at one of p's questions: it says what changed.
func (p *prompter) cancel() error { return cancelled(p.changes().Lines()...) }

// confirm asks a yes-or-no question for q. need says what the answer
// allows, for the error when input is off: "a yes to install the helper on
// box". With input off, a question that keeps its default returns it, and
// any other fails with exit code 4.
func (p *prompter) confirm(q *question, text, need string) (bool, error) {
	if !p.on {
		if q.keep {
			return q.def, nil
		}
		return false, p.needs(q, need)
	}
	return p.yesNo(text, q.def)
}

// asks reports whether p asks q, for a question that is more than a yes or
// a no, such as the checklist of agents. With input off, the command takes
// q's default.
func (p *prompter) asks(*question) bool { return p.on }

// needs is the error of a question that input off left without an answer.
// Its Try: line is the command line with the flags that answer it: q's
// flag, or the flags given.
func (p *prompter) needs(q *question, need string, flags ...string) error {
	if len(flags) == 0 {
		flags = []string{q.flag}
	}
	return &client.UserError{
		Msg:  fmt.Sprintf("%s needs %s, and input is off", q.cmd, need),
		Hint: p.withFlags(flags...),
		Code: "needs_answer",
		Kind: client.KindNeedsAnswer,
	}
}

// yesNo asks text with its default in capitals, and reads the answer. Enter
// takes def. y, yes, n and no work in any case. Any other answer asks
// again. The third such answer cancels.
func (p *prompter) yesNo(text string, def bool) (bool, error) {
	opts := " [y/N] "
	if def {
		opts = " [Y/n] "
	}
	for try := 1; ; try++ {
		a, err := p.ask(text + opts)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(a) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		if try == 3 {
			return false, p.cancel()
		}
		fmt.Fprintln(p.out, "Type y or n, or press Enter for the answer in capitals.")
	}
}

// typedName asks for the name of the thing that a severe action deletes
// (G9). Only name itself confirms, exactly: a space before or after it is
// other text. Enter keeps the thing, and returns false. Any other text
// cancels, spaces alone too. text holds the question, such as "Type box to
// delete the machine, or press Enter to keep it:".
func (p *prompter) typedName(text, name string) (bool, error) {
	a, err := p.askLine(text + " ")
	if err != nil {
		return false, err
	}
	switch a {
	case name:
		return true, nil
	case "":
		return false, nil
	}
	return false, p.cancel()
}

// ask writes q and reads one line, through answerOn, without the spaces at
// its ends. A cancel that comes after the line, before the caller acts on
// it, wins too. A cancel says what changed before it.
func (p *prompter) ask(q string) (string, error) {
	a, err := p.askLine(q)
	return strings.TrimSpace(a), err
}

// askLine is ask with the line as it was typed, except for its line
// ending.
func (p *prompter) askLine(q string) (string, error) {
	a, err := readLine(p.ctx, p.in, p.out, q)
	if err == nil && stopped(p.ctx) {
		err = errInterrupted
	}
	if isCancel(err) && len(p.changes().Lines()) > 0 {
		return "", p.cancel()
	}
	return a, err
}

// cancelOf is err, a cancel, as p says it: with what changed before it.
// A signal at a question, with nothing changed, keeps its own words.
func (p *prompter) cancelOf(err error) error {
	if errors.Is(err, errInterrupted) && len(p.changes().Lines()) == 0 {
		return err
	}
	return p.cancel()
}

// cancelError is a cancel at a prompt: esc, ctrl+c, the end of input,
// three answers that are not one, or a typed name that does not match. It
// prints its one line, without "Error:", and exits with 130. It counts as
// context.Canceled, so a command that stops on a cancel stops on it too.
// A cancel during the fixes has data too: the fixes, as they ended
// (fixesCancelled).
type cancelError struct {
	msg  string
	data json.RawMessage
}

func (e *cancelError) Error() string { return e.msg }

// Is makes a cancel at a prompt a context.Canceled.
func (e *cancelError) Is(target error) bool { return target == context.Canceled }

// cancelled is the cancel at a prompt. changed says what earlier steps
// changed, such as "saved the host box", if anything (cli-spec 6.2, rule 6).
func cancelled(changed ...string) error {
	return &cancelError{msg: cancelLine("Cancelled.", changed)}
}

// cancelLine is the line of a cancel: head, then what changed before it.
func cancelLine(head string, changed []string) string {
	switch len(changed) {
	case 0:
		return head + " Nothing changed."
	case 1:
		return head + " Done before that: " + changed[0] + "."
	}
	last := len(changed) - 1
	return head + " Done before that: " + strings.Join(changed[:last], ", ") + ", and " + changed[last] + "."
}

// isCancel reports whether err is a cancel at a prompt: a key or the end
// of input (cancelError), or a signal or the end of the context
// (errInterrupted).
func isCancel(err error) bool {
	var c *cancelError
	return errors.As(err, &c) || errors.Is(err, errInterrupted)
}
