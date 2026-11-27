//go:build windows

package cli

import "os/exec"

func configureJoinSudo(cmd *exec.Cmd) { cmd.WaitDelay = 0 }
