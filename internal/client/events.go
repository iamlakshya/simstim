package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/link"
)

// Prune lists or removes stopped sessions on this host that have no commits
// waiting to be pulled (isolation spec 17.6).
func (c *Conn) Prune(ctx context.Context, a api.PruneArgs) (api.PruneResult, error) {
	if !c.HasFeature(api.FeatureEvents) {
		return api.PruneResult{}, &UserError{
			Msg:  "the helper on " + c.Host.Name + " is too old for prune",
			Hint: brand.Name + " host upgrade " + ShellQuote(c.Host.Name),
			Code: "wall_helper_old",
		}
	}
	var out api.PruneResult
	err := c.Call(ctx, "prune", a, &out)
	return out, err
}

// FinishSession finishes a throwaway (`new --rm`) session once its program has
// exited, removing it unless commits have not been pulled (spec 17.6).
func (c *Conn) FinishSession(ctx context.Context, id string) (api.SessionFinishResult, error) {
	if !c.HasFeature(api.FeatureEvents) {
		return api.SessionFinishResult{}, &UserError{
			Msg:  "the helper on " + c.Host.Name + " is too old for --rm",
			Hint: brand.Name + " host upgrade " + ShellQuote(c.Host.Name),
			Code: "wall_helper_old",
		}
	}
	var out api.SessionFinishResult
	err := c.Call(ctx, "session.finish", api.SessionFinishArgs{ID: id}, &out)
	if err == nil && out.Action == "removed" {
		ForgetCopyIn(c.Host.Name, id)
	}
	return out, err
}

// WatchEvents opens the streaming `events.watch` bridge op on host h and calls
// emit for each session or sandbox event until the stream ends or ctx is
// cancelled (spec 10.5, 17.6).
func WatchEvents(ctx context.Context, h Host, args api.EventsWatchArgs, emit func(api.WatchEvent) error) error {
	t, err := HostTransport(ctx, h, false)
	if err != nil {
		return err
	}
	st, closer, err := openBridgeStream(ctx, t, h, "events.watch", args, link.KindEvents)
	if err != nil {
		return err
	}
	defer closer.Close()

	stop := context.AfterFunc(ctx, func() {
		_ = closer.Close()
	})
	defer stop()

	sc := bufio.NewScanner(st)
	sc.Buffer(make([]byte, 8<<10), 256<<10)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev api.WatchEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		ev.Host = h.Name
		if err := emit(ev); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) && ctx.Err() == nil {
		return fmt.Errorf("read events from %s: %w", h.Name, err)
	}
	return nil
}

// RunSessionExec runs one non-interactive command inside session s on host h
// through the session's exec backend (the supervisor's link stream for walled
// sessions, or `<helper> ssh-exec` for unwalled sessions), streaming stdout,
// stderr and optional stdin, and returning the command's exit code (spec 17.6).
func RunSessionExec(ctx context.Context, h Host, s *Session, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if s == nil {
		return 1, errors.New("missing session")
	}
	t, err := HostTransport(ctx, h, true)
	if err != nil {
		return 1, err
	}
	backend := &SessionSSHBackend{
		Host:      h,
		SessionID: s.ID,
		Transport: t,
		Walled:    api.IsWalled(s.Wall),
	}
	es, err := backend.StartExec(ctx, api.SSHExecArgs{
		Command: command,
		PTY:     false,
	})
	if err != nil {
		return 1, err
	}
	defer es.Close()

	stop := context.AfterFunc(ctx, func() {
		_ = es.Signal("TERM")
		_ = es.Close()
	})
	defer stop()

	if stdin != nil {
		go func() {
			_, _ = io.Copy(es.Stdin(), stdin)
			_ = es.Stdin().Close()
		}()
	} else {
		_ = es.Stdin().Close()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if stdout != nil {
			_, _ = io.Copy(stdout, es.Stdout())
		} else {
			_, _ = io.Copy(io.Discard, es.Stdout())
		}
	}()
	go func() {
		defer wg.Done()
		if stderr != nil {
			_, _ = io.Copy(stderr, es.Stderr())
		} else {
			_, _ = io.Copy(io.Discard, es.Stderr())
		}
	}()

	code, err := es.Wait()
	wg.Wait()
	return code, err
}
