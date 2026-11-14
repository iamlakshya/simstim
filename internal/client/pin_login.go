package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"slices"
	"strings"
)

// CheckLogin checks that `ssh target true` works without a question, for a
// target that is not a host yet, as the guided setup asks before it adds
// one. Like every connection, it logs in only with a pin: it stages, under
// a name of its own, the key that the user's own settings trust for the
// target, and logs in with that pin alone. A key that the settings do not
// name is ErrUnchecked: the user checks it once by hand, with ssh. It
// leaves no pin and removes only the temporary configs this operation
// created. Replaced files and current saved references stay.
func CheckLogin(ctx context.Context, target string) error {
	h := loginCheckHost(target)
	ctx, abort, err := StagePinOwned(ctx, h.Name)
	if err != nil {
		return err
	}
	defer abort()
	wrappers, err := beginLoginWrapperScope(ctx, h)
	if err != nil {
		return err
	}
	defer wrappers.cleanup(ctx)
	_, err = pinHostKey(WithoutInput(ctx), h, "", FirstUseRefuse, true)
	if err == nil {
		err = loginOnce(ctx, h)
	}
	var ue *UserError
	if errors.As(err, &ue) {
		// The check's own name means nothing to the user: say the target.
		c := *ue
		c.Msg = strings.ReplaceAll(c.Msg, h.Name, target)
		c.Hint = strings.ReplaceAll(c.Hint, h.Name, target)
		return &c
	}
	return err
}

func loginCheckHost(target string) Host {
	sum := sha256.Sum256([]byte(target))
	return Host{Name: "setup-check-" + hex.EncodeToString(sum[:6]), SSH: target}
}

// loginOnce runs true on h, with a connection of its own, which leaves no
// shared connection behind.
func loginOnce(ctx context.Context, h Host) error {
	logFile, err := newSSHLog()
	if err != nil {
		return err
	}
	s := &SSH{Host: h}
	args, err := s.connArgs(ctx, false)
	if err != nil {
		logFile.finish()
		return err
	}
	args = slices.Concat(args, NoPortForwards(), []string{"-T", "-e", "none", "-E", logFile.path, "--", h.SSH, "true"})
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = sshWaitDelay
	var errb bytes.Buffer
	cmd.Stderr = &errb
	runErr := cmd.Run()
	sshLog := logFile.finish()
	if runErr != nil {
		return sshError(ctx, h, runErr, sshLog, errb.String())
	}
	return nil
}
