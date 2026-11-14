package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
)

// RunForward runs a port forward with cleaned terminal diagnostics. It calls
// onStart only after checking the pin and SSH configuration and preparing a
// private destination log, before ssh starts writing to stdout or stderr.
func (s *SSH) RunForward(ctx context.Context, pairs []ForwardPair, stdin io.Reader, stdout, stderr io.Writer, onStart func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	logFile, err := newForwardLog()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &UserError{Msg: "forward cannot make a private SSH log: " + CleanLine(err.Error()),
			Hint: "Check that the temporary folder is writable, then try again.", Code: "ssh_failed", Kind: KindFailed}
	}
	defer func() {
		if logFile != nil {
			logFile.finish()
		}
	}()
	cmd, err := s.forwardCommand(ctx, pairs, []string{"-E", logFile.path})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	var outputMu sync.Mutex
	stream := &cleanWriter{w: forwardWriter{mu: &outputMu, w: stderr}}
	defer stream.Flush()
	var errOut lockedWriter
	cmd.Stdin = stdin
	cmd.Stdout = forwardWriter{mu: &outputMu, w: stdout}
	cmd.Stderr = io.MultiWriter(stream, &errOut)
	cmd.WaitDelay = sshWaitDelay
	if err := ctx.Err(); err != nil {
		return err
	}
	if onStart != nil {
		onStart()
	}
	runErr := cmd.Run()
	log := logFile.finish()
	logFile = nil
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr == nil {
		return nil
	}
	if ke := hostKeyError(s.Host, log, exitCode(runErr)); ke != nil {
		return ke
	}
	diagnostic := tailStderr(log)
	if diagnostic == "" {
		diagnostic = tailStderr(errOut.String())
	}
	if diagnostic != "" {
		return fmt.Errorf("run the SSH forward: %w: %s", runErr, diagnostic)
	}
	return fmt.Errorf("run the SSH forward: %w", runErr)
}

func newForwardLog() (*sshLogFile, error) {
	log, err := newSSHLog()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(log.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		log.finish()
		return nil, fmt.Errorf("create the SSH forward log: %w", err)
	}
	if err := f.Close(); err != nil {
		log.finish()
		return nil, fmt.Errorf("close the SSH forward log: %w", err)
	}
	return log, nil
}

// forwardWriter permits callers to use the same writer for both streams.
type forwardWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w forwardWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p) //nolint:wrapcheck // the caller's writer error, passed on as a writer does
}
