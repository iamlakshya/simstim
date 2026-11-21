//go:build !linux

package classify

import "os/exec"

func commandSupported() bool                  { return false }
func commandGroup(_ *exec.Cmd) commandProcess { return noCommand{} }

type noCommand struct{}

func (noCommand) waitExited() error { return errCommand }
func (noCommand) stop()             {}
func (noCommand) close()            {}
