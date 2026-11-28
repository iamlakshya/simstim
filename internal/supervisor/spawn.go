package supervisor

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type sandboxSession struct {
	id         string
	m          *host.Manifest
	ln         net.Listener
	dirty      bool
	ended      bool
	acceptDone chan struct{}
}

// SpawnPayload aliases host.SpawnSocketRequest so the bridge and supervisor
// share one wire schema.
type SpawnPayload = host.SpawnSocketRequest

func (s *Supervisor) handleSpawn(w *proto.Writer, req proto.Request) {
	reply := func(v any, perr *proto.Error) {
		resp := proto.Response{ID: req.ID, OK: perr == nil, Error: perr}
		if v != nil {
			resp.Result, _ = json.Marshal(v)
		}
		_ = w.WriteJSON(proto.TResponse, resp)
	}
	var payload SpawnPayload
	if err := json.Unmarshal(req.Args, &payload); err != nil {
		reply(nil, &proto.Error{Code: "bad_request", Message: "invalid spawn request", Hint: "Update the host helper."})
		return
	}
	defer clear(payload.SecretEnv)

	cm := payload.Manifest
	if cm.ID == "" || len(cm.Agent.Argv) == 0 {
		reply(nil, &proto.Error{Code: "bad_request", Message: "spawn requires a session id and command", Hint: "Update the host helper."})
		return
	}
	if cm.SandboxOf != "" && cm.SandboxOf != s.id {
		reply(nil, &proto.Error{Code: "bad_request", Message: "sandbox_of does not match this sandbox", Hint: "Use the primary session ID."})
		return
	}
	cm.SandboxOf = s.id

	s.mu.Lock()
	if s.ended || s.stopping {
		s.mu.Unlock()
		reply(nil, &proto.Error{
			Code:    "sandbox_stopped",
			Message: "the sandbox is stopped",
			Hint:    "Wake the sandbox first with resume, then try again.",
		})
		return
	}
	if _, dup := s.sessions[cm.ID]; dup {
		s.mu.Unlock()
		reply(nil, &proto.Error{Code: "bad_request", Message: "session " + cm.ID + " already exists in this sandbox", Hint: "Try again."})
		return
	}
	primaryCwd := s.m.Cwd
	primaryUnit := s.m.Run.Unit
	primaryWall := s.m.Wall
	s.mu.Unlock()

	if cm.Cwd == "" {
		cm.Cwd = primaryCwd
	}
	if primaryWall != nil && cm.Wall == nil {
		wCopy := *primaryWall
		cm.Wall = &wCopy
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Allow the joining agent's egress hosts in the running egress process
	// immediately and persist the extra agent on the primary manifest for wake.
	if cm.Agent.Kind != "" && cm.Agent.Kind != "shell" {
		s.allowAgentEgress(ctx, cm.Agent.Kind)
		s.recordExtraAgent(cm.Agent.Kind)
	}

	spawnReq := guest.SpawnReq{
		Session:   cm.ID,
		Name:      cm.Name,
		Agent:     cm.Agent.Kind,
		Argv:      cm.Agent.Argv,
		Env:       cm.Env,
		Secrets:   payload.SecretEnv,
		Cwd:       cm.Cwd,
		Term:      cm.Term.Term,
		Cols:      cm.Term.Cols,
		Rows:      cm.Term.Rows,
		RunHook:   cm.RunHook,
		Notes:     payload.Notes,
		AgentID:   cm.Agent.AgentSessionID,
		IDSource:  cm.Agent.IDSource,
		Ephemeral: payload.Ephemeral,
	}
	resp, err := s.ctlCall(ctx, "spawn", spawnReq)
	clear(spawnReq.Secrets)
	if err != nil {
		reply(nil, &proto.Error{Code: "sandbox_stopped", Message: "the sandbox did not answer spawn: " + err.Error(), Hint: "Check ls and resume the sandbox if stopped."})
		return
	}
	if !resp.OK {
		reply(nil, cleanGuestError(resp.Error, "spawn_failed", "guest spawn failed", "Check the command and try again."))
		return
	}
	var sr guest.SpawnResp
	_ = json.Unmarshal(resp.Result, &sr)

	sock := paths.Socket(cm.ID)
	for _, dir := range []string{filepath.Dir(filepath.Dir(sock)), filepath.Dir(sock)} {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			reply(nil, &proto.Error{Code: "spawn_failed", Message: err.Error(), Hint: "Check runtime directory permissions."})
			return
		}
	}
	ln, err := host.ListenPrivate(sock, "the spawned session's socket")
	if err != nil {
		_, _ = s.ctlCall(ctx, "kill_session", guest.KillSessionReq{Session: cm.ID, GraceMS: 1000})
		reply(nil, &proto.Error{Code: "spawn_failed", Message: err.Error(), Hint: "Try again."})
		return
	}

	now := time.Now().UTC()
	cm.RunHook = ""
	cm.Run = host.Runtime{
		HolderPID: os.Getpid(),
		AgentPID:  sr.PID,
		Unit:      primaryUnit,
		Socket:    sock,
		BootID:    host.BootID(),
		StartedAt: now,
	}
	if exe, eerr := os.Executable(); eerr == nil {
		cm.HolderBinary = exe
	}
	cm.HostVersion = version.Version
	if sr.Cols > 0 && sr.Rows > 0 {
		cm.Term.Cols, cm.Term.Rows = sr.Cols, sr.Rows
	}
	cm.Status = host.Status{State: host.StateWorking, Since: now, Source: "spawn"}

	child := &sandboxSession{
		id:         cm.ID,
		m:          &cm,
		ln:         ln,
		acceptDone: make(chan struct{}),
	}

	s.mu.Lock()
	if s.ended || s.stopping {
		s.mu.Unlock()
		_ = ln.Close()
		_ = os.Remove(sock)
		reply(nil, &proto.Error{Code: "sandbox_stopped", Message: "the sandbox stopped while starting the session", Hint: "Resume the sandbox and try again."})
		return
	}
	s.sessions[cm.ID] = child
	savedCopy := snapshotManifest(child.m)
	s.mu.Unlock()

	if err := savedCopy.Save(); err != nil {
		s.log.Warn("spawned manifest not saved", "session", cm.ID, "err", err)
	}
	s.audit.Record(AuditEntry{Event: "spawn", Session: cm.ID, Detail: cm.Agent.Kind})
	go s.acceptChildLoop(child)
	reply(savedCopy, nil)
}

func (s *Supervisor) acceptChildLoop(child *sandboxSession) {
	defer close(child.acceptDone)
	for {
		conn, err := child.ln.Accept()
		if err != nil {
			return
		}
		if !s.trackConn(conn) {
			continue
		}
		go func(c net.Conn) {
			defer s.wg.Done()
			defer s.untrackConn(c)
			s.serveSessionConn(child.id, c)
		}(conn)
	}
}

func (s *Supervisor) killChildSession(id string) {
	s.mu.Lock()
	child := s.sessions[id]
	if child == nil || id == s.id {
		s.mu.Unlock()
		return
	}
	child.ended = true
	delete(s.sessions, id)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = s.ctlCall(ctx, "kill_session", guest.KillSessionReq{Session: id, GraceMS: 1000})
	cancel()

	if child.ln != nil {
		_ = child.ln.Close()
		<-child.acceptDone
	}
	_ = os.Remove(paths.Socket(id))

	s.mu.Lock()
	child.m.Status = host.Status{State: host.StateExited, Since: time.Now().UTC(), Reason: "killed by request", Claimed: false}
	cp := snapshotManifest(child.m)
	s.mu.Unlock()
	_ = cp.Save()
	s.audit.Record(AuditEntry{Event: "kill", Session: id, Detail: "child session stopped"})
}

func (s *Supervisor) closeChildSockets() {
	s.mu.Lock()
	var children []*sandboxSession
	for id, sess := range s.sessions {
		if id != s.id {
			children = append(children, sess)
		}
	}
	s.mu.Unlock()
	for _, child := range children {
		if child.ln != nil {
			_ = child.ln.Close()
			if child.acceptDone != nil {
				<-child.acceptDone
			}
		}
		_ = os.Remove(paths.Socket(child.id))
	}
}

// finalizeChildrenLocked marks all child sessions ended and returns copies of
// their manifests to save. Caller holds s.mu.
func (s *Supervisor) finalizeChildrenLocked(reason string, system bool) []*host.Manifest {
	now := time.Now().UTC()
	var out []*host.Manifest
	for id, sess := range s.sessions {
		if id == s.id {
			continue
		}
		sess.ended = true
		switch {
		case system:
			sess.m.Status.Reason, sess.m.Status.Source, sess.m.Status.Claimed = reason, "system", false
		case sess.m.Status.State != host.StateExited && sess.m.Status.State != host.StateErrored:
			sess.m.Status = host.Status{State: host.StateExited, Since: now, Reason: reason, Claimed: false}
		default:
			sess.m.Status.Claimed = false
		}
		cp := snapshotManifest(sess.m)
		out = append(out, &cp)
	}
	return out
}

func (s *Supervisor) allowAgentEgress(ctx context.Context, kind string) {
	if s.egress == nil {
		return
	}
	ag, ok := agents.Find(kind)
	if !ok {
		return
	}
	for _, h := range ag.Egress {
		if h == "" {
			continue
		}
		if err := s.egress.Allow(ctx, h); err != nil {
			// Model gateway hosts and locked hosts intentionally refuse direct Allow.
			s.log.Debug("joining agent egress host not directly allowed", "agent", kind, "host", h, "err", err)
		}
	}
}

func (s *Supervisor) recordExtraAgent(kind string) {
	s.mu.Lock()
	if s.m.Wall == nil || kind == s.m.Agent.Kind || slices.Contains(s.m.Wall.ExtraAgents, kind) {
		s.mu.Unlock()
		return
	}
	wallCopy := *s.m.Wall
	wallCopy.ExtraAgents = append(slices.Clone(s.m.Wall.ExtraAgents), kind)
	s.m.Wall = &wallCopy
	s.dirty = true
	s.mu.Unlock()
	_ = s.save(false)
}
