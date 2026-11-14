package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

func (c *Conn) startError(ctx context.Context, err error) error {
	waitErr := c.waitStartup(ctx)
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if c.agentOnly {
		if failure := agentProcessExitFailure(exitCode(waitErr)); failure != nil {
			return failure
		}
	}
	if source, ok := c.proc.(interface{ agentFailure() error }); ok {
		if failure := source.agentFailure(); failure != nil {
			return failure
		}
	}
	if native, ok := c.proc.(interface {
		nativeSSHDiag() (nativeSSHDiagnosis, int, bool)
	}); ok {
		diagnosis, code, complete := native.nativeSSHDiag()
		if complete && (errors.Is(waitErr, ErrUnreachable) || code < 0) {
			if code == 255 && diagnosis.stopped {
				if refusal := hostKeyStopError(c.Host, diagnosis.shownType, diagnosis.shown); refusal != nil {
					return refusal
				}
			}
			why := sshFailure("", waitErr)
			if code == 255 && diagnosis.failReason != "" {
				why = diagnosis.failReason
			}
			return &UserError{Msg: fmt.Sprintf("cannot reach %s: %s", c.Host.Name, why), Hint: c.hint, Code: "unreachable"}
		}
	}
	if d, ok := c.proc.(interface{ sshDiag() (string, int) }); ok {
		sshLog, code := d.sshDiag()
		if errors.Is(waitErr, ErrUnreachable) || code < 0 {
			if ke := hostKeyError(c.Host, sshLog, code); ke != nil {
				return ke
			}
			why := sshFailure("", waitErr)
			if code == 255 {
				why = sshFailReason(sshLog, c.proc.Stderr(), waitErr)
			}
			return &UserError{Msg: fmt.Sprintf("cannot reach %s: %s", c.Host.Name, why), Hint: c.hint, Code: "unreachable"}
		}
	} else {
		if code := exitCode(waitErr); code < 0 && waitErr != nil && !strings.HasPrefix(waitErr.Error(), "exit status ") && !errors.Is(waitErr, ErrUnreachable) {
			return err
		}
		if errors.Is(waitErr, ErrUnreachable) {
			msg := strings.TrimSpace(c.proc.Stderr())
			if msg == "" {
				return err
			}
			return &UserError{Msg: fmt.Sprintf("cannot reach %s: %s", c.Host.Name, sshFailure(msg, waitErr)), Hint: c.hint, Code: "unreachable"}
		}
	}
	msg := strings.TrimSpace(c.proc.Stderr())
	if strings.Contains(msg, "No such file") || strings.Contains(msg, "not found") {
		return ErrNotInstalled
	}
	msg = tailStderr(msg)
	if msg == "" {
		return err
	}
	return &UserError{Msg: fmt.Sprintf("the helper on %s did not start: %s", c.Host.Name, msg), Hint: brand.Name + " host doctor " + c.Host.Name, Code: "helper_failed"}
}
