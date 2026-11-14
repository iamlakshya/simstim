package client

import (
	"context"
	"fmt"
	"io"
	"sync"
)

type sshOriginKey struct{}

func (s *SSH) entryHost() Host {
	if s.origin != nil {
		return *s.origin
	}
	return s.Host
}

func (s *SSH) policyContext(ctx context.Context) context.Context {
	if s.origin != nil {
		return context.WithValue(ctx, sshOriginKey{}, s.origin)
	}
	return ctx
}

// beginSSHStart keeps saved host and tier writers behind the actual process
// start. Its returned context must reach argument assembly so a caller that
// already holds the host lock shares it. The release is safe to call twice.
func (s *SSH) beginSSHStart(ctx context.Context, kind string) (context.Context, func(), error) {
	ctx = s.policyContext(ctx)
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	st, err := stageForContext(ctx, s.Host.Name)
	if err != nil {
		return ctx, nil, err
	}
	if st != nil {
		// An owned, unpublished stage always uses a restricted wrapper.
		return ctx, func() {}, nil
	}
	expected := s.entryHost()
	ctx, unlock, err := LockHost(ctx, expected.Name, kind)
	if err != nil {
		return ctx, nil, err
	}
	release := sync.OnceFunc(unlock)
	saved, err := SavedEntry(expected.Name)
	if err != nil {
		release()
		return ctx, nil, err
	}
	if s.Host.Name != expected.Name || saved != nil && !SameEntry(*saved, expected) {
		release()
		return ctx, nil, (HostChange{Kind: kind, Was: &expected, Now: &expected}).changed(saved)
	}
	// The canonical pin path rejects an absent entry without an owned stage.
	// It never turns a removed host's orphan pin into a connection.
	return ctx, release, nil
}

// RunShell gives a login shell the supplied terminal streams. It checks the
// current saved host and environment when the shell actually starts, and
// releases the host lock before waiting for the shell to end.
func (s *SSH) RunShell(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	ctx, release, err := s.beginSSHStart(ctx, "shell")
	if err != nil {
		return err
	}
	defer release()
	cmd, err := s.Shell(ctx)
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.WaitDelay = sshWaitDelay
	runErr := cmd.Start()
	release()
	if runErr == nil {
		runErr = cmd.Wait()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr != nil {
		return fmt.Errorf("run the SSH shell: %w", runErr)
	}
	return nil
}
