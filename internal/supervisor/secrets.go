package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func (s *Supervisor) secretsBeforeStart(restored bool) (secretEnv, notes []string) {
	if note := s.armHostSubscription(); note != "" {
		notes = append(notes, note)
	}
	want := s.m.Secrets
	s.m.MissingSecrets = nil
	if len(want) == 0 {
		return nil, notes
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

func (s *Supervisor) finishSandboxExit(ex Exit) {
	close(s.doneCh)
	s.closeAllPorts()
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
		s.emitSandboxErrorLocked(st.Reason, &code, ex.Signal)
		s.audit.Record(AuditEntry{Event: "link_error", Session: s.id, ExitCode: &code, Signal: ex.Signal, OOM: oom})
	}
	childCopies := s.finalizeChildrenLocked(stopRequest{kind: stopKindKill, reason: "the sandbox exited unexpectedly"}, time.Now().UTC())
	s.mu.Unlock()
	s.closeAlerts()
	_ = s.save(true)
	for _, cc := range childCopies {
		_ = cc.Save()
	}
	s.closeChildSockets()
	s.closeSocket()
	<-s.acceptDone
}

func (s *Supervisor) finishLinkClosed() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = s.sb.Stop(ctx, 2*time.Second)
	cancel()
	s.finishSandboxExit(s.sb.Wait())
}

// finishStart records the trusted start metadata from StartResp, logs the
// start audit event, and replays any guest ctl events that arrived before
// start completed. All of this happens before the first post-start manifest is
// written, so an early guest exit can never write StateErrored with AgentPID 0.
func (s *Supervisor) finishStart(sr guest.StartResp) {
	now := time.Now()
	nowUTC := now.UTC()
	s.mu.Lock()
	s.m.RunHook = ""
	if u := host.OwnUnit(s.id); u != "" {
		s.m.Run.Unit = u
	}
	s.m.Run.HolderPID = os.Getpid()
	s.m.Run.AgentPID = sr.AgentPID
	s.m.Run.Socket = paths.Socket(s.id)
	s.m.Run.BootID = host.BootID()
	s.m.Run.StartedAt = nowUTC
	if exe, err := os.Executable(); err == nil {
		s.m.HolderBinary = exe
	}
	s.m.HostVersion = version.Version
	if sr.Cols > 0 && sr.Rows > 0 {
		s.m.Term.Cols, s.m.Term.Rows = sr.Cols, sr.Rows
	}
	if !s.ended {
		s.m.Status = host.Status{State: host.StateWorking, Since: nowUTC, Source: "start"}
	}
	s.lastHB = now
	s.starting = false
	wallLvl, wallDrv := "", ""
	if s.m.Wall != nil {
		wallLvl, wallDrv = s.m.Wall.Level, s.m.Wall.Driver
	}
	s.audit.Record(AuditEntry{Event: "start", Session: s.id, Wall: wallLvl, Driver: wallDrv})
	queued := s.earlyEvents
	s.earlyEvents = nil
	for _, ev := range queued {
		s.applyGuestEventLocked(ev, now, nowUTC)
	}
	s.mu.Unlock()
}

// snapshotManifest returns a copy of m safe to marshal or save after releasing
// s.mu. Callers must hold s.mu.
func snapshotManifest(m *host.Manifest) host.Manifest {
	cp := *m
	if m.Usage != nil {
		cp.Usage = m.Usage.Clone()
	}
	if m.Guest != nil {
		g := *m.Guest
		g.Ports = slices.Clone(g.Ports)
		if g.Mem != nil {
			mem := *g.Mem
			g.Mem = &mem
		}
		cp.Guest = &g
	}
	cp.Agent.IDHistory = slices.Clone(m.Agent.IDHistory)
	cp.GrantedPorts = slices.Clone(m.GrantedPorts)
	cp.MissingSecrets = slices.Clone(m.MissingSecrets)
	cp.Pulled = slices.Clone(m.Pulled)
	return cp
}

// save snapshots the latest manifest under saveMu so concurrent writers never
// overwrite newer state with a stale copy.
func (s *Supervisor) save(last bool) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.closed {
		return nil
	}
	s.mu.Lock()
	cp := snapshotManifest(s.m)
	s.mu.Unlock()
	s.closed = last
	return cp.Save()
}
