package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func policyEditCmd() *cobra.Command {
	var layer policyLayerFlags
	cmd := &cobra.Command{Use: "edit", Short: "Edit one policy file, check it and preview its live changes", Args: cobra.NoArgs,
		Example: "  " + brand.Name + " policy edit --profile review", RunE: func(cmd *cobra.Command, _ []string) error {
			if layer.in != "" {
				return policyUsage("a session layer has no laptop file to edit", "Use --all, --host, --repo or --profile.", "layer_key")
			}
			if err := layer.mutation(cmd); err != nil {
				return err
			}
			selected, err := layer.file(cmd.Context())
			if err != nil {
				return err
			}
			path, line, err := client.PolicyEditLocation(selected)
			if err != nil {
				return err
			}
			editor := strings.Fields(os.Getenv("EDITOR"))
			if len(editor) == 0 {
				return &client.UserError{Msg: "policy edit needs an editor", Hint: "Set EDITOR to an editor command, then run policy edit again.", Code: "editor_missing", Kind: client.KindNeedsAnswer}
			}
			if !inputOn(cmd) {
				return &client.UserError{Msg: "policy edit needs a terminal", Hint: "Run policy edit in an interactive terminal.", Code: "needs_answer", Kind: client.KindNeedsAnswer}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err == nil {
				err = file.Close()
			} else if os.IsExist(err) {
				err = nil
			}
			if err != nil {
				return err
			}
			args := append(append([]string{}, editor[1:]...), "+"+strconv.Itoa(line), path)
			command := exec.CommandContext(cmd.Context(), editor[0], args...)
			command.Stdin = cmd.InOrStdin()
			command.Stdout = cmd.OutOrStdout()
			command.Stderr = cmd.ErrOrStderr()
			command.WaitDelay = time.Second
			if err := command.Run(); err != nil {
				return &client.UserError{Msg: "the policy editor did not finish successfully: " + client.CleanLine(err.Error()), Hint: "Check EDITOR and run policy edit again.", Code: "editor_failed", Kind: client.KindFailed}
			}
			report := checkConfig()
			report.write(cmd.OutOrStdout())
			if !report.OK {
				return &quietError{&client.UserError{Msg: "the edited policy has config errors", Hint: "Fix the listed errors before applying the policy.", Code: "config_errors", Kind: client.KindFailed}}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Policy changes are a dry run:")
			return runPolicyApply(cmd, &selected, true, false, false, false)
		}}
	layer.add(cmd)
	return cmd
}
