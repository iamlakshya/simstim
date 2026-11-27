package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/tui"
)

// runHome opens the launcher.
func runHome(cmd *cobra.Command) error {
	if err := checkLauncher(cmd, "the launcher", brand.Name+" ls (lists the sessions without input)"); err != nil {
		return err
	}
	return tui.RunWith(cmd.Context(), tui.Options{Local: onboard.System{Providers: providers}, SessionShell: runLauncherSessionShell, PolicyLockdown: runLauncherPolicyLockdown})
}

// checkLauncher enforces the input and terminal rules of cli-spec 6.1 before
// an interactive launcher screen opens.
func checkLauncher(cmd *cobra.Command, what, offHint string) error {
	if inputOff(cmd) {
		return &client.UserError{
			Msg:  what + " cannot open with input off",
			Hint: offHint,
			Code: "no_input",
			Kind: client.KindNeedsAnswer,
		}
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) || !isTerminal(os.Stderr) {
		return &client.UserError{
			Msg:  what + " needs a terminal",
			Hint: offHint,
			Code: "launcher_no_terminal",
			Kind: client.KindFailed,
		}
	}
	return nil
}
