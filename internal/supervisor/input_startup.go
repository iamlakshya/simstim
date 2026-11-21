package supervisor

import (
	"bufio"
	"context"
	"errors"
	"net"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type startupInput struct {
	completion       *inputCompletion
	clock            *inputPhaseClock
	carrier          *link.Session
	scope            inputtransfer.Scope
	summary          api.InputSummary
	copyMax, seedMax int64
	runGeneration    string
}

var errInputAccepted = errors.New("input continuation accepted")

// awaitInputStartup is the same prestart Unix reader, extended to retain the
// dedicated input continuation while it receives the complete secret envelope.
// The ordinary accept loop begins only after startup completes.
func (s *Supervisor) awaitInputStartup(ctx context.Context) map[string]string {
	s.mu.Lock()
	want := slices.Clone(s.m.Secrets)
	needValues := len(want) > 0 || s.m.Run.StartEnvelope
	s.mu.Unlock()
	for _, member := range s.savedMembers {
		needValues = needValues || len(member.Secrets) > 0
	}
	listener, ok := s.ln.(*net.UnixListener)
	if !ok {
		s.startupErr = inputFailure("the input phase has no owned prestart listener")
		return nil
	}
	deadline := now().Add(secretsWait)
	_ = listener.SetDeadline(deadline)
	defer listener.SetDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { _ = listener.SetDeadline(now()) })
	defer stop()
	var values map[string]string
	if s.originalStart != nil {
		needValues = false
		values = s.originalStart.values
		s.originalStart.values = nil
	}
	for needValues || s.startupInput == nil {
		conn, err := listener.Accept()
		if err != nil {
			s.startupErr = inputFailure("the complete declared startup input did not arrive")
			return nil
		}
		stopConn := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(now()) })
		env, err := s.readPrestartSecrets(ctx, conn, want, deadline)
		stopConn()
		if errors.Is(err, errInputAccepted) {
			continue
		}
		_ = conn.Close()
		if err == nil {
			values, needValues = env, false
		}
		if ctx.Err() != nil {
			s.startupErr = ctx.Err()
			return nil
		}
	}
	return values
}

func (s *Supervisor) acceptStartupInputs(ctx context.Context, c *client, reader *bufio.Reader, req proto.Request) error {
	begin, err := decodeInputBegin(req.Args)
	if err != nil || begin.Summary != nil || begin.FinishOnly || begin.RunGeneration != "" {
		return inputFailure("initial inputs must use the protected start declaration")
	}
	scope := begin.Scope
	s.mu.Lock()
	if s.startupInput != nil || s.m.Inputs == nil || scope.Session != s.id || scope.Nonce != s.m.Inputs.Nonce || scope.Digest != s.m.Inputs.Digest || ctx.Err() != nil {
		s.mu.Unlock()
		return inputFailure("the input continuation differs from its protected start declaration")
	}
	copyMax, seedMax, limitsErr := host.SessionInputLimits(s.m)
	if limitsErr != nil {
		s.mu.Unlock()
		return limitsErr
	}
	if err := s.m.Inputs.Validate(copyMax, seedMax); err != nil {
		s.mu.Unlock()
		return err
	}
	summary := *s.m.Inputs
	summary.Repositories = api.CloneInputRepositories(summary.Repositories)
	s.mu.Unlock()
	clock, err := newInputPhaseClock(s.lifetimeContext(), summary)
	if err != nil {
		return err
	}
	c.reply(req.ID, inputtransfer.BeginResult{Version: 1, RunGeneration: s.generation}, nil)
	if c.writeErr != nil {
		clock.close()
		return c.writeErr
	}
	_ = c.conn.SetDeadline(time.Time{})
	s.startupInput = &startupInput{clock: clock, carrier: link.ContinueInputs(c.conn, reader, link.Supervisor, len(summary.Repositories) > 0), scope: scope, summary: summary, copyMax: copyMax, seedMax: seedMax, runGeneration: s.generation}
	return errInputAccepted
}

func inputFailure(message string) *proto.Error {
	return &proto.Error{Code: "input_protocol", Message: message, Hint: "Prepare the local inputs again with matching client and helper versions."}
}

func (s *Supervisor) closeStartupInput() {
	if s.startupInput != nil {
		if s.startupInput.clock != nil {
			s.startupInput.clock.close()
		}
		_ = s.startupInput.carrier.Close()
	}
}

// readSecrets keeps the legacy test/caller surface. The actual composed input
// path supplies its active prestart context directly to the same decoder.
func (s *Supervisor) readSecrets(conn net.Conn, want []string, deadline time.Time) (map[string]string, error) {
	ctx, cancel := context.WithDeadline(s.lifetimeContext(), deadline)
	defer cancel()
	return s.readPrestartSecrets(ctx, conn, want, deadline)
}

func (s *Supervisor) markCopiesPending(ctx context.Context, pending bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	var snapshot host.Manifest
	return s.writeManifestChange(&snapshot, false, nil, nil, func(m *host.Manifest) error {
		m.CopiesPending = pending
		return nil
	}, func(m *host.Manifest) { s.m.CopiesPending = m.CopiesPending })
}
