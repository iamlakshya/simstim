package host

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/ring"
)

func TestTheSessionSocketTakesNoHookEvents(t *testing.T) {
	for _, check := range []bool{false, true} {
		r := newPeerRig(t, rigPeer{check: check, cgroup: outsideCgroup, alive: func() bool { return true }})
		forbidden(t, "hook on the session socket", r.session().request("hook", notification("on the session socket")))
		r.untouched()
	}
}

func TestWithoutThePeerCheckTheSessionSocketServesAll(t *testing.T) {
	// No systemd user manager: no unit, so no peer check. The holder never
	// asks for the peer, and serves a program whatever its cgroup says.
	r := newPeerRig(t, rigPeer{cgroup: otherCgroup, read: func(net.Conn) (peer, error) {
		t.Error("the holder read the peer without the peer check")
		return peer{}, errors.New("no peer")
	}})
	if resp := r.session().request("send", map[string]any{"text": "abcdef"}); !resp.OK {
		t.Fatalf("send: %+v", resp.Error)
	}
	waitUntil(t, func() bool {
		b, _ := os.ReadFile(r.typed)
		return string(b) == "abcdef"
	})
	// On the hook socket, the holder falls back to the role check without
	// asking for the peer.
	f, _ := r.hookEvent("bridge", notification("as the bridge"))
	if code := errorCode(t, f); code != "forbidden" {
		t.Errorf("role bridge without peer check: code %q", code)
	}
	if _, resp := r.hookEvent(roleHook, notification("from the hook")); resp == nil || !resp.OK {
		t.Errorf("role hook without peer check: %+v", resp)
	}
}

// hookEvent sends a hook event on the hook socket as role. It returns the
// frame that answered the hello, and the answer to the event when the hello
// passed.
func (r *peerRig) hookEvent(role string, ev HookEvent) (proto.Frame, *proto.Response) {
	r.t.Helper()
	rc, f := r.dial(paths.HookSocket(r.h.id), role)
	if f.Type != proto.THello {
		return f, nil
	}
	resp := rc.request("hook", ev)
	return f, &resp
}

// notified reports whether the holder took a notification with msg.
func (r *peerRig) notified(msg string) bool {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	return r.h.needInput && r.h.notifyMsg == msg && r.h.hookSeen
}

func notification(msg string) HookEvent {
	p, _ := json.Marshal(map[string]string{"message": msg})
	return HookEvent{Event: "notification", Payload: p}
}

func sessionStart(id string) HookEvent {
	p, _ := json.Marshal(map[string]string{"session_id": id, "source": "startup"})
	return HookEvent{Event: "session-start", Payload: p}
}

func TestTheHookSocketTakesOnlyHookEvents(t *testing.T) {
	r := newPeerRig(t, rigPeer{cgroup: outsideCgroup})
	// The role must be hook.
	f, _ := r.hookEvent("bridge", notification("as the bridge"))
	if code := errorCode(t, f); code != "forbidden" {
		t.Errorf("role bridge: code %q", code)
	}
	// The op must be hook.
	for _, op := range []string{"send", "kill", "peek", "info", "secrets"} {
		rc, f := r.dial(paths.HookSocket(r.h.id), roleHook)
		if f.Type != proto.THello {
			t.Fatalf("hello: %v %s", f.Type, f.Payload)
		}
		forbidden(t, op+" on the hook socket", rc.request(op, map[string]any{"text": "typed"}))
		if !rc.closedByHolder() {
			t.Errorf("%s: the hook socket stays open after forbidden", op)
		}
	}
	// No other frame either, not even a ping.
	for _, ft := range []proto.Type{proto.TPing, proto.TAttach, proto.TInput} {
		rc, _ := r.dial(paths.HookSocket(r.h.id), roleHook)
		if err := rc.w.Write(ft, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if code := errorCode(t, rc.next()); code != "forbidden" {
			t.Errorf("frame %v on the hook socket: code %q", ft, code)
		}
	}
	r.untouched()
	// A hook event updates the session.
	if _, resp := r.hookEvent(roleHook, notification("from the hook")); resp == nil || !resp.OK {
		t.Fatalf("hook: %+v", resp)
	}
	if !r.notified("from the hook") {
		t.Error("the hook did not reach the session")
	}
}

func TestAHookReachesOnlyItsOwnSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cgroup func(string) string
		alive  func() bool
		ok     bool
	}{
		{"its own session", ownCgroup, nil, true},
		{"below its own unit", func(id string) string { return "0::" + userMgr + brand.Slice + "/" + UnitName(id) + "/tool\n" }, nil, true},
		{"outside every session", outsideCgroup, nil, true},
		{"another session", otherCgroup, nil, false},
		{"another session, in a group named after this one", func(id string) string {
			return "0::" + userMgr + brand.Slice + "/" + UnitName("othersess") + "/" + UnitName(id) + "\n"
		}, nil, false},
		{"a git scope", gitScopeCgroup, nil, false},
		{"a cgroup that cannot be read", noCgroup, nil, false},
		{"a pid that another process took", ownCgroup, func() bool { return false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPeerRig(t, rigPeer{check: true, cgroup: tc.cgroup, alive: tc.alive})
			// The holder keeps an agent's session ID only in the agent's
			// form. So the session counts as Claude Code's here, and the
			// hook sends a UUID.
			const agentUUID = "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"
			r.h.mu.Lock()
			r.h.m.Agent.Kind = "claude"
			r.h.mu.Unlock()
			f, resp := r.hookEvent(roleHook, notification("hook"))
			_, resp2 := r.hookEvent(roleHook, sessionStart(agentUUID))
			switch {
			case tc.ok && (resp == nil || !resp.OK || resp2 == nil || !resp2.OK):
				t.Fatalf("the hook was refused: %v %s", f.Type, f.Payload)
			case !tc.ok && f.Type == proto.THello:
				t.Fatalf("the hook was let in: %+v", resp)
			case !tc.ok:
				if code := errorCode(t, f); code != "forbidden" {
					t.Fatalf("code %q", code)
				}
			}
			if got := r.notified("hook"); got != tc.ok {
				t.Errorf("the session took the hook: %v, want %v", got, tc.ok)
			}
			r.h.mu.Lock()
			agentID := r.h.m.Agent.AgentSessionID
			r.h.mu.Unlock()
			if (agentID == agentUUID) != tc.ok {
				t.Errorf("the agent's ID is %q", agentID)
			}
		})
	}
}

// secretsRig is a holder that waits for its secrets, with the fake peer in
// another session's unit.
func secretsRig(t *testing.T) (*Holder, chan []string) {
	t.Helper()
	m := &Manifest{Schema: 1, ID: NewID(), Name: "s", Agent: AgentInfo{Kind: "custom", Argv: []string{"/bin/true"}},
		Cwd: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}, Term: TermInfo{Cols: 80, Rows: 24}, Secrets: []string{"OPENAI_API_KEY"}}
	h, _ := peerHolder(t, rigPeer{check: true, cgroup: otherCgroup}, m)
	done := make(chan []string, 1)
	go func() { done <- h.awaitSecrets([]string{"OPENAI_API_KEY"}, 5*time.Second) }()
	return h, done
}

// While the holder waits for its secrets, a program inside a session gets
// what it gets later: a pong for a ping, and forbidden for the rest, the
// secrets too. The client's values still arrive after it.
func TestWhileItWaitsForSecretsAProgramInsideIsRefused(t *testing.T) {
	h, done := secretsRig(t)
	sock := paths.Socket(h.id)
	rc, f := dialHolder(t, sock, "bridge")
	if f.Type != proto.THello {
		t.Fatalf("hello: %v %s", f.Type, f.Payload)
	}
	rc.ping()
	if err := rc.w.Write(proto.TAttach, []byte(`{"cols":80,"rows":24,"last_offset":-1}`)); err != nil {
		t.Fatal(err)
	}
	if code := errorCode(t, rc.next()); code != "forbidden" {
		t.Errorf("attach while it waits: code %q", code)
	}
	rc, _ = dialHolder(t, sock, "bridge")
	forbidden(t, "secrets from inside a session", rc.request("secrets", secretsMsg{Env: map[string]api.Secret{"OPENAI_API_KEY": "planted"}}))

	// The client, outside every session, still delivers the values.
	if err := os.WriteFile(fakePeerCgroup(), []byte(outsideCgroup("")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deliverSecrets(h.id, map[string]api.Secret{"OPENAI_API_KEY": secretValue}); err != nil {
		t.Fatal(err)
	}
	if env := <-done; len(env) != 1 || env[0] != "OPENAI_API_KEY="+secretValue {
		t.Fatalf("the holder took %q", env)
	}
}

func TestSecretsFromInsideAreRefused(t *testing.T) {
	h, done := secretsRig(t)
	_, err := HolderRequest(h.id, "secrets", secretsMsg{Env: map[string]api.Secret{"OPENAI_API_KEY": secretValue}}, time.Second)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "forbidden" {
		t.Fatalf("secrets from inside a session: %v", err)
	}
	// A new session that cannot pass its secrets from inside a session says
	// why, with the holder's own error: a retry cannot help.
	err = secretsFailed(deliverSecrets(h.id, map[string]api.Secret{"OPENAI_API_KEY": secretValue}))
	if !errors.As(err, &pe) || pe.Code != "forbidden" || pe.Message != errInside.Message || pe.Hint != errInside.Hint {
		t.Fatalf("new from inside a session: %v", err)
	}
	if err := secretsFailed(errors.New("timed out")); !errors.As(err, &pe) || pe.Code != "secrets_failed" {
		t.Fatalf("new when the secrets did not arrive: %v", err)
	}
	if err := os.WriteFile(fakePeerCgroup(), []byte(outsideCgroup("")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deliverSecrets(h.id, map[string]api.Secret{"OPENAI_API_KEY": secretValue}); err != nil {
		t.Fatal(err)
	}
	if env := <-done; len(env) != 1 {
		t.Fatalf("the holder took %d secrets", len(env))
	}
}

// A new session with secrets, started from inside a session, fails with the
// holder's own refusal, not with "try again": a retry cannot help. Its
// holder runs in this process, and waits for its secrets.
func TestANewSessionFromInsideSaysWhyItsSecretsFailed(t *testing.T) {
	secretHome(t)
	fakePeer(t, otherCgroup(""), nil)
	oldWait, oldStart := stopWait, startHolder
	stopWait = 100 * time.Millisecond
	t.Cleanup(func() { stopWait, startHolder = oldWait, oldStart })
	startHolder = func(m *Manifest) (*os.Process, error) {
		h := &Holder{id: m.ID, m: m, log: slog.New(slog.DiscardHandler), ring: ring.New(1 << 16), clients: map[*hclient]struct{}{}}
		if err := h.listen(); err != nil {
			return nil, err
		}
		t.Cleanup(h.closeSockets)
		h.peerCheck = true
		// The listener stays in this process so fakePeer controls its peer
		// check. The launch still returns an owned child that cleanup must
		// stop and reap before removing the session.
		child := exec.Command("sleep", "60")
		if err := child.Start(); err != nil {
			return nil, err
		}
		oldSignal := signalHolder
		signalHolder = func(p *os.Process, sig os.Signal) error {
			if p == child.Process {
				h.closeSockets()
			}
			return oldSignal(p, sig)
		}
		t.Cleanup(func() { signalHolder = oldSignal })
		t.Cleanup(func() {
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
		})
		go h.awaitSecrets(m.Secrets, 3*time.Second)
		return child.Process, nil
	}
	id := NewID()
	m := &Manifest{Schema: 1, ID: id, Name: "s", Agent: AgentInfo{Kind: "custom", Argv: []string{"/bin/true"}},
		Cwd: t.TempDir(), Secrets: []string{"OPENAI_API_KEY"}}
	_, err := startSession(m, map[string]api.Secret{"OPENAI_API_KEY": secretValue}, false)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "forbidden" || pe.Message != errInside.Message || pe.Hint != errInside.Hint {
		t.Fatalf("new from inside a session: %v", err)
	}
	if _, err := os.Stat(paths.SessionDir(id)); !os.IsNotExist(err) {
		t.Errorf("the failed session was not removed: %v", err)
	}
}
