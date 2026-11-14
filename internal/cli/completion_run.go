package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func completionArgs(args []string) bool {
	return len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd)
}

func completionAtDash(cmd *cobra.Command) bool {
	line := commandLine(cmd)
	if len(line) == 0 {
		return cmd.ArgsLenAtDash() >= 0
	}
	// Cobra temporarily parses an added "--" during completion. pflag
	// keeps that offset when the next parse is empty, so use the actual
	// request and the shared flag lexer instead of that temporary offset.
	values := map[int]bool{}
	for _, word := range words(cmd.Flags(), line) {
		if word.flag != "" {
			values[word.at] = true
		}
	}
	for i, text := range line {
		if text == "--" && !values[i] {
			return true
		}
	}
	return false
}

// Cobra creates its hidden command during Execute, after Root is built.
// Wrap its Run in the persistent hook, once the command exists. Cobra's
// CompErrorln bypasses SetErr, so its process stderr needs a separate sink.
func wireQuietCompletion(root *cobra.Command) {
	previous := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if previous != nil {
			if err := previous(cmd, args); err != nil {
				return err
			}
		}
		if isCompletionRequest(cmd) && cmd.Annotations["quiet-completion"] != "true" {
			if cmd.Annotations == nil {
				cmd.Annotations = map[string]string{}
			}
			cmd.Annotations["quiet-completion"] = "true"
			run := cmd.Run
			cmd.Run = func(c *cobra.Command, words []string) { runQuietCompletion(c, words, run) }
		}
		return nil
	}
}

func runQuietCompletion(cmd *cobra.Command, args []string, run func(*cobra.Command, []string)) {
	root := cmd.Root()
	destination := root.OutOrStdout()
	r, w, err := os.Pipe()
	if err != nil {
		_, _ = fmt.Fprintln(destination, ":4")
		return
	}
	read := make(chan int64, 1)
	go func() {
		n, _ := io.Copy(io.Discard, r)
		_ = r.Close()
		read <- n
	}()
	var output bytes.Buffer
	previousErr, processErr := root.ErrOrStderr(), os.Stderr
	root.SetOut(&output)
	root.SetErr(io.Discard)
	os.Stderr = w
	func() {
		defer func() {
			os.Stderr = processErr
			root.SetOut(destination)
			root.SetErr(previousErr)
			_ = w.Close()
		}()
		run(cmd, args)
	}()
	text := output.String()
	if <-read > 0 {
		// A parse error must never make the shell offer local files.
		text = completionWithoutFiles(text)
	}
	_, _ = io.WriteString(destination, text)
}

func completionWithoutFiles(text string) string {
	trimmed := strings.TrimSuffix(text, "\n")
	start := strings.LastIndex(trimmed, "\n") + 1
	if strings.HasPrefix(trimmed[start:], ":") {
		return trimmed[:start] + ":4\n"
	}
	return ":4\n"
}

// quietCompletionErrors also covers failures before Cobra runs its hidden
// command. Cobra still writes explicit debugging to BASH_COMP_DEBUG_FILE.
func quietCompletionErrors() (restore func(), ok bool) {
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, false
	}
	previous := os.Stderr
	os.Stderr = f
	return func() {
		os.Stderr = previous
		_ = f.Close()
	}, true
}
