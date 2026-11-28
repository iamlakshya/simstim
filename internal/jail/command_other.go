//go:build !linux

package jail

import (
	"context"
	"errors"
	"os/exec"
)

type deps struct {
	fs jailFS
}

func defaultDeps() deps { return deps{fs: defaultJailFS} }

func defaultUserns() bool { return false }

var unsupportedPlatformCommand = func(context.Context, Spec) (*exec.Cmd, error) {
	return nil, errors.New("jails require Linux. Try: run on a Linux host or inside a Lima VM")
}

func commandWithDeps(ctx context.Context, s Spec, d deps) (*exec.Cmd, error) {
	if err := validateSpec(s, d.fs); err != nil {
		return nil, err
	}
	return unsupportedPlatformCommand(ctx, s)
}

// RunExec is a Linux-only internal subcommand stub on other platforms.
func RunExec(_ []string) error {
	return errors.New("jail-exec requires Linux. Try: run on a Linux host or inside a Lima VM")
}
