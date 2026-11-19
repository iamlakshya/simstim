package host

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// HookEvent is what an agent's hook reports.
type HookEvent struct {
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// currentHelper is the stable path of the newest installed helper, so hooks in
// long-running sessions keep working after an upgrade.
func currentHelper() string {
	return filepath.Join(paths.BinDir(), "current", brand.HostBinary)
}

// hookArgs fills the {hook} and {hook_bin} placeholders of an agent's hook arguments.
func hookArgs(args []string) []string {
	bin := currentHelper()
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "{hook}", shellQuote(bin)+" hook")
		a = strings.ReplaceAll(a, "{hook_bin}", bin)
		out[i] = a
	}
	return out
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"\\$`!*?[]{}()<>|&;") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RunHook is the `hook <event>` command that agents call. It forwards the event
// to the session's holder and always exits quickly and cleanly, so a hook can
// never slow down or break the agent.
func RunHook(event string, args []string) {
	sock := hookSocket()
	if sock == "" {
		return
	}
	var payload []byte
	if len(args) > 0 && strings.HasPrefix(strings.TrimSpace(args[len(args)-1]), "{") {
		payload = []byte(args[len(args)-1]) // Codex passes JSON as the last argument
	} else if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
		done := make(chan []byte, 1)
		go func() {
			b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			done <- b
		}()
		select {
		case payload = <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}
	if len(payload) > 0 && !json.Valid(payload) {
		payload = nil
	}
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	conn, err := d.DialContext(context.Background(), "unix", sock)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	w := proto.NewWriter(conn)
	r := bufio.NewReader(conn)
	if w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: roleHook}) != nil {
		return
	}
	if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
		return
	}
	ev, _ := json.Marshal(HookEvent{Event: event, Payload: payload})
	_ = w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "hook", Args: ev})
	_, _ = proto.Read(r)
}

// hookSocket is the socket that a hook sends its event to: the hook socket
// that the agent's environment names. A session that an older helper
// started names only its session socket. Its holder is that older helper
// too, and takes hook events there. A new holder never does: a resume or a
// restore gives the session the hook socket first (withHookSocket).
func hookSocket() string {
	if sock := os.Getenv(brand.Env("HOOK_SOCK")); sock != "" {
		return sock
	}
	return os.Getenv(brand.Env("SOCK"))
}

// withHookSocket returns env, a session's environment, naming only the hook
// socket of session id. An older helper named the session socket instead.
// A restore or a resume starts a new holder, which serves hooks only on the
// hook socket.
func withHookSocket(env []string, id string) []string {
	sock, hook := brand.Env("SOCK")+"=", brand.Env("HOOK_SOCK")+"="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, sock) && !strings.HasPrefix(kv, hook) {
			out = append(out, kv)
		}
	}
	return append(out, hook+paths.HookSocket(id))
}

// applyHook updates the holder's view of the session from a hook event.
// The caller holds h.mu.
func (h *Holder) applyHook(ev HookEvent) {
	var p struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
		Message   string `json:"message"`
		Type      string `json:"type"`
		ThreadID  string `json:"thread-id"`
	}
	_ = json.Unmarshal(ev.Payload, &p)
	now := time.Now().UTC()
	switch ev.Event {
	case "session-start":
		if p.SessionID != "" && p.SessionID != h.m.Agent.AgentSessionID {
			h.m.Agent.AgentSessionID = p.SessionID
			h.m.Agent.IDSource = "hook"
			h.m.Agent.IDHistory = append(h.m.Agent.IDHistory, IDEvent{ID: p.SessionID, Source: p.Source, At: now})
		}
	case "prompt":
		h.hookBusy, h.needInput, h.notifyMsg = true, false, ""
	case "stop":
		h.hookBusy, h.needInput = false, false
		h.lastTurnEnd = now
	case "notification":
		h.needInput = true
		h.notifyMsg = p.Message
	case "codex-notify":
		if p.Type == "agent-turn-complete" {
			h.hookBusy = false
			h.lastTurnEnd = now
		}
	}
	h.hookSeen = true
	h.dirty = true
}
