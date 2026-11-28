package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func (s *Supervisor) secretsBeforeStart(restored bool) (secretEnv, notes []string) {
	want := s.m.Secrets
	s.m.MissingSecrets = nil
	if len(want) == 0 {
		return nil, nil
	}
	var received map[string]api.Secret
	if !restored {
		received = s.awaitSecrets(want, s.opt.SecretsWait)
	}
	if received == nil {
		s.m.MissingSecrets = append([]string(nil), want...)
		notes = append(notes, host.MissingSecretsNote(want, restored))
		return nil, notes
	}
	exposedEnv, err := s.splitAndArmSecrets(want, received)
	if err != nil {
		s.log.Error("arm bound secrets failed", "err", err)
		boundMap := s.boundSecretMap()
		for _, k := range want {
			if _, ok := boundMap[k]; ok {
				s.m.MissingSecrets = append(s.m.MissingSecrets, k)
			}
		}
		if len(s.m.MissingSecrets) > 0 {
			notes = append(notes, host.MissingSecretsNote(s.m.MissingSecrets, restored))
		}
	}
	return exposedEnv, notes
}

func (s *Supervisor) awaitSecrets(want []string, timeout time.Duration) map[string]api.Secret {
	ul, ok := s.ln.(*net.UnixListener)
	if !ok {
		return nil
	}
	deadline := time.Now().Add(timeout)
	_ = ul.SetDeadline(deadline)
	defer func() { _ = ul.SetDeadline(time.Time{}) }()
	for {
		conn, err := ul.Accept()
		if err != nil {
			s.log.Warn("secrets did not arrive, starting without them", "waited", timeout)
			return nil
		}
		vals, err := s.readSecrets(conn, want, deadline)
		_ = conn.Close()
		if err == nil {
			s.log.Info("secrets received", "count", len(vals))
			return vals
		}
	}
}

func (s *Supervisor) readSecrets(conn net.Conn, want []string, deadline time.Time) (map[string]api.Secret, error) {
	dl := time.Now().Add(3 * time.Second)
	if deadline.Before(dl) {
		dl = deadline
	}
	_ = conn.SetDeadline(dl)
	r := bufio.NewReader(conn)
	w := proto.NewWriter(conn)
	if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
		return nil, errors.New("no hello")
	}
	if err := w.WriteJSON(proto.THello, proto.Hello{
		Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "supervisor",
	}); err != nil {
		return nil, err
	}
	if !s.fullPeer(conn) {
		s.serveLimited(r, w)
		return nil, errors.New(host.ErrInside().Message)
	}
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TRequest {
		return nil, errors.New("no request")
	}
	var req proto.Request
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return nil, fmt.Errorf("read secrets request: %w", err)
	}
	fail := func(code, msg string) error {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: &proto.Error{Code: code, Message: msg, Hint: "Try again in a moment."}})
		return errors.New(msg)
	}
	if req.Op != "secrets" {
		return nil, fail("starting", "the session is starting")
	}
	var msg host.SecretsPayload
	if err := json.Unmarshal(req.Args, &msg); err != nil {
		return nil, fail("bad_request", "the secrets are not valid JSON")
	}
	vals := make(map[string]api.Secret, len(want))
	for _, k := range want {
		v, ok := msg.Env[k]
		if !ok {
			return nil, fail("bad_request", "the secret "+k+" is missing")
		}
		vals[k] = v
	}
	_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{"ok":true}`)})
	return vals, nil
}

func (s *Supervisor) requestStop(reason string, system bool) {
	s.stopOnce.Do(func() {
		s.stopCh <- stopRequest{reason: reason, system: system}
	})
}

func (s *Supervisor) finishStop(reason string, system bool) {
	close(s.doneCh)
	s.closeAlerts()
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()

	deadline := time.Now().Add(s.opt.StopGrace)
	ctlCtx, ctlCancel := context.WithDeadline(context.Background(), deadline)
	_, ctlErr := s.ctlCall(ctlCtx, "stop", guest.StopReq{GraceMS: int(s.opt.StopGrace / time.Millisecond)})
	ctlCancel()

	// If ctl "stop" was acknowledged, wall-init is already running its own
	// shutdown with StopGrace; give it that window before escalating with Stop.
	if ctlErr == nil {
		waitCh := make(chan struct{})
		go func() {
			_ = s.sb.Wait()
			close(waitCh)
		}()
		rem := time.Until(deadline)
		if rem > 0 {
			t := time.NewTimer(rem)
			select {
			case <-waitCh:
				t.Stop()
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = s.sb.Stop(ctx, 2*time.Second)
				cancel()
				<-waitCh
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.sb.Stop(ctx, 2*time.Second)
			cancel()
			<-waitCh
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), s.opt.StopGrace)
		_ = s.sb.Stop(ctx, s.opt.StopGrace)
		cancel()
	}

	s.closeAllPorts()
	s.closeSocket()
	<-s.acceptDone
	s.closeAllConns()
	_ = s.link.Close()
	s.wg.Wait()
	_ = s.sb.Destroy()

	s.mu.Lock()
	_ = s.mem.Reap(s.id)
	s.m.GrantedPorts = nil
	s.ended = true
	switch {
	case system:
		s.m.Status.Reason, s.m.Status.Source, s.m.Status.Claimed = reason, "system", false
	case s.m.Status.State != host.StateExited && s.m.Status.State != host.StateErrored:
		s.m.Status = host.Status{State: host.StateExited, Since: time.Now().UTC(), Reason: reason, Claimed: false}
	default:
		s.m.Status.Claimed = false
	}
	mCopy := *s.m
	s.mu.Unlock()
	_ = s.save(&mCopy, true)
	s.audit.Record(AuditEntry{Event: "kill", Session: s.id, Detail: reason})
}

func (s *Supervisor) finishSandboxExit(ex Exit) {
	close(s.doneCh)
	s.closeAllPorts()
	s.closeSocket()
	<-s.acceptDone
	s.closeAllConns()
	_ = s.link.Close()
	s.wg.Wait()
	_ = s.sb.Destroy()

	s.mu.Lock()
	s.m.GrantedPorts = nil
	if !s.ended && !s.stopping {
		s.ended = true
		newKills := s.mem.Reap(s.id)
		oom := newKills && ex.Signal == syscall.SIGKILL.String()
		code := ex.Code
		if code == 0 && ex.Err != nil {
			code = 1
		}
		st := host.Status{
			State:    host.StateErrored,
			Since:    time.Now().UTC(),
			ExitCode: &code,
			Signal:   ex.Signal,
			OOM:      oom,
			Source:   "sandbox",
			Reason:   "the sandbox exited unexpectedly",
			Claimed:  false,
		}
		if oom {
			st.Reason = "oom"
		}
		s.m.Status = st
		s.alertEnded(code, ex.Signal, oom)
		s.audit.Record(AuditEntry{Event: "link_error", Session: s.id, ExitCode: &code, Signal: ex.Signal, OOM: oom})
	}
	mCopy := *s.m
	s.mu.Unlock()
	s.closeAlerts()
	_ = s.save(&mCopy, true)
}

func (s *Supervisor) finishLinkClosed() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = s.sb.Stop(ctx, 2*time.Second)
	cancel()
	s.finishSandboxExit(s.sb.Wait())
}

func (s *Supervisor) save(m *host.Manifest, last bool) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = last
	return m.Save()
}
