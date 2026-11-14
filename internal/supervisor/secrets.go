package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// Secrets reach a walled session as they reach a holder (secrets.go in
// internal/host): the host hands their values to the supervisor over the
// session's socket, before the sandbox starts. The supervisor passes them
// on in start, and writes them nowhere.

// secretsWait is how long the supervisor waits for the session's secrets
// before it starts the sandbox without them.
const secretsWait = 30 * time.Second

// awaitSecrets waits on the session's socket for the values of the
// session's secrets, and returns them. It returns nil when the session has
// none, when restore started it, after the wait, and once ctx ends.
func (s *Supervisor) awaitSecrets(ctx context.Context) map[string]string {
	if s.m.Inputs != nil && !host.WasRestored(s.m) {
		return s.awaitInputStartup(ctx)
	}
	s.mu.Lock()
	want := append([]string(nil), s.m.Secrets...)
	restored := host.WasRestored(s.m)
	s.mu.Unlock()
	hasMembers := false
	for _, m := range s.savedMembers {
		if len(m.Secrets) > 0 {
			hasMembers = true
		}
	}
	if (len(want) == 0 && !hasMembers && !s.m.Run.StartEnvelope) || restored {
		return nil
	}
	ul, ok := s.ln.(*net.UnixListener)
	if !ok {
		return nil
	}
	deadline := time.Now().Add(secretsWait)
	_ = ul.SetDeadline(deadline)
	defer func() { _ = ul.SetDeadline(time.Time{}) }()
	stop := context.AfterFunc(ctx, func() { _ = ul.SetDeadline(time.Now()) })
	defer stop()
	for {
		conn, err := ul.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.log.Info("the wait for the secrets ended: a stop")
				return nil
			}
			s.log.Warn("secrets did not arrive, starting without them", "waited", secretsWait)
			return nil
		}
		stopConn := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
		env, err := s.readSecrets(conn, want, deadline)
		stopConn()
		conn.Close()
		if err == nil {
			s.log.Info("secrets received", "count", len(env))
			return env
		}
	}
}

// readSecrets serves one connection while the supervisor waits for the
// secrets. Any other request is told that the session is starting. A
// program inside a session gets a pong for a ping, and forbidden for
// anything else, the secrets too.
func (s *Supervisor) readPrestartSecrets(ctx context.Context, conn net.Conn, want []string, deadline time.Time) (map[string]string, error) {
	_ = conn.SetDeadline(minTime(deadline, time.Now().Add(3*time.Second)))
	r := bufio.NewReader(conn)
	c := &client{conn: conn, w: proto.NewWriter(conn)}
	if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
		return nil, errors.New("no hello")
	}
	b, _ := json.Marshal(proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "supervisor", Features: Features})
	if err := c.write(proto.THello, b); err != nil {
		return nil, err
	}
	if !host.FullPeer(conn, s.id, s.peerCheck) {
		for {
			f, err := proto.Read(r)
			if err != nil || !s.serveLimited(c, f) {
				return nil, errors.New("a program inside a session")
			}
		}
	}
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TRequest {
		return nil, errors.New("no request")
	}
	var req proto.Request
	if json.Unmarshal(f.Payload, &req) != nil {
		return nil, errors.New("a request that is not valid")
	}
	fail := func(code, msg string) error {
		c.reply(req.ID, nil, &proto.Error{Code: code, Message: msg, Hint: "Try again in a moment."})
		return errors.New(msg)
	}
	if req.Op == "inputs.begin" {
		err := s.acceptStartupInputs(ctx, c, r, req)
		if err != errInputAccepted {
			c.reply(req.ID, nil, toProtoErr(err))
		}
		return nil, err
	}
	if req.Op != "secrets" {
		return nil, fail("starting", "the session is starting")
	}
	env, err := s.validateStartupSecrets(ctx, req.Args, want)
	req.Args = nil
	if err != nil {
		c.reply(req.ID, nil, toProtoErr(err))
		return nil, err
	}
	c.reply(req.ID, map[string]bool{"ok": true}, nil)
	return env, nil
}

// minTime is the earlier of a and b.
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
