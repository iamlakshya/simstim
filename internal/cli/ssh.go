package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

var qEditorSSHInclude = &question{cmd: "ssh-config", flag: "None", keep: true}

func init() { questions = append(questions, qEditorSSHInclude) }

func sshConfigCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "ssh-config",
		Short: "Prepare the local SSH configuration for sandbox tools",
		Long: "Checks the system SSH parser before writing local configuration and a pinned laptop host key.\n" +
			"Asks before adding the generated Include to your SSH config. The SSH server runs in the\n" +
			"client on standard input and output. It opens no listening port.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (returned error) {
			if remove {
				if err := client.RemoveEditorSSHConfig(cmd.Context()); err != nil {
					return err
				}
				say(cmd, "Removed the generated editor SSH configuration and its Include.\n")
				return nil
			}
			setup, err := client.PrepareEditorSSHConfig(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { returned = errors.Join(returned, setup.Close()) }()
			if err := client.ShowEditorSSHTrustNotice(cmd.Context(), func(text string) error {
				_, err := fmt.Fprintln(cmd.ErrOrStderr(), client.CleanLine(text))
				return err
			}); err != nil {
				return err
			}
			line, err := setup.IncludeLine()
			if err != nil {
				return err
			}
			say(cmd, "Prepared %s and its pinned known-hosts file.\n", client.CleanLine(setup.ConfigPath()))
			say(cmd, "The SSH Include is: %s\n", client.CleanLine(strings.TrimSpace(line)))
			include, err := prompts(cmd).confirm(qEditorSSHInclude, "Add this Include to the top of your SSH config?", "a yes to add the generated SSH Include")
			if err != nil {
				return err
			}
			if !include {
				note(cmd, "ssh_include_unchanged", "The generated files are ready. Your SSH config was left unchanged.")
				return nil
			}
			changed, err := setup.InstallInclude(cmd.Context())
			if err != nil {
				return err
			}
			if changed {
				say(cmd, "Added the generated SSH Include. Try: ssh <session>.<host>.%s\n", brand.Name)
			} else {
				say(cmd, "The generated SSH Include is already present.\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the generated SSH config and its exact Include; keep the laptop host key")
	return cmd
}
