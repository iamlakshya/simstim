//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestLauncherShellGrantPromptUsesActualTerminal(t *testing.T) {
	t.Setenv(noInputEnv, "")
	terminal := hosttest.NewTerminal(t, 80, 24)
	selected := &shellCommandSelection{in: terminal.TTY, out: terminal.TTY}
	command := launcherShellCommand(t.Context(), selected)
	originalIn, originalOut, originalErr := os.Stdin, os.Stdout, os.Stderr
	question := &client.ConfirmError{Kind: client.ConfirmYesNo, Host: "box", Prompt: "Allow this selected shell?",
		Err: &client.UserError{Msg: "the selected shell needs a terminal answer", Hint: "Answer the selected shell's question.", Code: "grant_confirmation", Kind: client.KindRefused}}
	done := make(chan error, 1)
	calls := 0
	go func() {
		done <- grantQuestions(command, func(context.Context) error {
			calls++
			return question
		})
	}()
	terminal.WaitFor("Allow this selected shell? [y/N] ")
	terminal.Type("n\r")
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatal("declining the actual terminal question retried the grant", calls, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the selected shell ignored its actual terminal: %q", terminal.Output())
	}
	if os.Stdin != originalIn || os.Stdout != originalOut || os.Stderr != originalErr {
		t.Fatal("the grant question swapped process-global terminal files")
	}
}

func TestLauncherShellGrantPromptKeepsInputRestrictions(t *testing.T) {
	t.Setenv(noInputEnv, "")
	for _, mode := range []string{"no-input", "non-terminal", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			terminal := hosttest.NewTerminal(t, 80, 24)
			selected := &shellCommandSelection{in: terminal.TTY, out: terminal.TTY}
			if mode == "non-terminal" {
				input, err := os.CreateTemp(t.TempDir(), "input")
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				if _, err := input.WriteString("yes\n"); err != nil {
					t.Fatal(err)
				}
				if _, err := input.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
				selected.in = input
			}
			command := launcherShellCommand(ctx, selected)
			if mode == "no-input" {
				command.Flags().Bool("no-input", true, "turn input off")
			}
			if mode == "cancelled" {
				cancel()
			}
			question := &client.ConfirmError{Kind: client.ConfirmYesNo, Host: "box", Prompt: "Keep the restriction?",
				Err: &client.UserError{Msg: "the selected shell needs a terminal answer", Hint: "Answer the selected shell's question.", Code: "grant_confirmation", Kind: client.KindRefused}}
			calls := 0
			err := grantQuestions(command, func(context.Context) error { calls++; return question })
			if calls != 1 {
				t.Fatal("a restricted question retried its grant", calls)
			}
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("the actual terminal question lost cancellation", err)
				}
			} else if err != question || len(terminal.Output()) != 0 {
				t.Fatal("carried terminal files enabled a live answer", err, string(terminal.Output()))
			}
			if mode == "non-terminal" {
				if offset, err := selected.in.Seek(0, 1); err != nil || offset != 0 {
					t.Fatal("a non-terminal supplied a grant answer", offset, err)
				}
			}
		})
	}
}

func TestLauncherShellCommandKeepsActualDiagnosticFiles(t *testing.T) {
	input, err := os.OpenFile(filepath.Join(t.TempDir(), "input"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString("selected-terminal-value\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(filepath.Join(t.TempDir(), "output"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	command := launcherShellCommand(t.Context(), &shellCommandSelection{in: input, out: output})
	if command.InOrStdin() != input || command.OutOrStdout() != output || command.ErrOrStderr() != output {
		t.Fatal("the shared command lost the actual restored terminal files")
	}
	settings := client.Settings{Secrets: map[string]client.SecretDefinition{
		"SELECTED": {Run: "read value; printf '%s\\n' \"$value\"; printf 'selected diagnostic\\n' >&2"},
	}}
	values, err := client.FetchSecrets(command.Context(), settings, []string{"SELECTED"}, true)
	if err != nil || string(values["SELECTED"]) != "selected-terminal-value" {
		t.Fatal("the selected command lost its original terminal input", err)
	}
	written, err := os.ReadFile(output.Name())
	if err != nil || string(written) != "selected diagnostic\n" {
		t.Fatal("the diagnostic reached another terminal", string(written), err)
	}
	if secretTerminal(command) || !strings.HasSuffix(command.Use, "<host>/<session>") {
		t.Fatal("private non-terminal files enabled terminal access or replaced the shared command")
	}
	unbound := shellCmd()
	unbound.SetIn(input)
	unbound.SetOut(output)
	unbound.SetErr(output)
	if p := prompts(unbound); p.in != os.Stdin || p.out != os.Stderr || p.ctx != nil {
		t.Fatal("an unbound CLI command adopted selected-shell prompt files")
	}
}
