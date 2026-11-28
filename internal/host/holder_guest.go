package host

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// GuestStartConfig is delivered on stdin to a holder started in guest mode by
// guest init, so secret values stay in memory only and are never written to
// disk (decision 0032).
type GuestStartConfig struct {
	Manifest    Manifest `json:"manifest"`
	SecretEnv   []string `json:"secret_env,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	AgentUID    int      `json:"agent_uid,omitempty"`
	AgentGID    int      `json:"agent_gid,omitempty"`
	AgentGroups []uint32 `json:"agent_groups,omitempty"`
	HolderUID   int      `json:"holder_uid,omitempty"`
	HolderGID   int      `json:"holder_gid,omitempty"`
	HookGID     int      `json:"hook_gid,omitempty"`
	AgentCgroup string   `json:"agent_cgroup,omitempty"`
}

// GuestEvent is one event written by a guest-mode holder on fd 3 for guest
// init to forward on the link's ctl stream.
type GuestEvent struct {
	Type       string `json:"type"` // "started", "start_failed", "status", "alert", "agent_id", "ended", "resize", "activity"
	Session    string `json:"session,omitempty"`
	HolderPID  int    `json:"holder_pid,omitempty"`
	AgentPID   int    `json:"agent_pid,omitempty"`
	State      string `json:"state,omitempty"`
	Source     string `json:"source,omitempty"`
	Reason     string `json:"reason,omitempty"`
	FullScreen bool   `json:"full_screen,omitempty"`
	Kind       string `json:"kind,omitempty"`      // alert kind: "needs_input" or "turn_done"
	Detail     string `json:"detail,omitempty"`    // alert detail
	TurnNS     int64  `json:"turn_ns,omitempty"`   // working duration for turn_done
	AgentID    string `json:"id,omitempty"`        // agent_id event
	IDSource   string `json:"id_source,omitempty"` // hook source
	ExitCode   int    `json:"exit_code,omitempty"`
	Signal     string `json:"signal,omitempty"`
	Cols       int    `json:"cols,omitempty"`
	Rows       int    `json:"rows,omitempty"`
}

// guestConfig holds the guest-mode paths and event pipe when RunHolder runs
// inside a wall under guest init.
type guestConfig struct {
	socket       string
	hookSocket   string
	manifestPath string
	logPath      string
	secretEnv    []string
	notes        []string
	agentUID     int
	agentGID     int
	agentGroups  []uint32
	holderUID    int
	holderGID    int
	hookGID      int
	agentCgroup  string

	evMu     sync.Mutex
	events   *os.File
	lastAct  time.Time
	lastSent Status
}

// loadHolderManifest loads the manifest for RunHolder. When GUEST_SOCKET is
// set in the environment, the holder runs in guest mode: it reads its startup
// manifest and in-memory secrets from stdin, and uses the guest paths named in
// the environment instead of the host's paths.
func loadHolderManifest(id string) (*guestConfig, *Manifest, error) {
	sock := os.Getenv(brand.Env("GUEST_SOCKET"))
	if sock == "" {
		m, err := LoadManifest(id)
		if err != nil {
			return nil, nil, fmt.Errorf("load manifest: %w", err)
		}
		return nil, m, nil
	}
	g := &guestConfig{
		socket:       sock,
		hookSocket:   os.Getenv(brand.Env("GUEST_HOOK_SOCKET")),
		manifestPath: os.Getenv(brand.Env("GUEST_MANIFEST")),
		logPath:      os.Getenv(brand.Env("GUEST_LOG")),
	}
	if g.hookSocket == "" {
		g.hookSocket = filepath.Join(filepath.Dir(sock), "hook.sock")
	}
	if g.manifestPath == "" {
		g.manifestPath = filepath.Join(filepath.Dir(sock), "manifest-"+id+".json")
	}
	if g.logPath == "" {
		g.logPath = filepath.Join(filepath.Dir(sock), "holder-"+id+".log")
	}
	if os.Getenv(brand.Env("GUEST_EVENTS_FD")) == "3" {
		g.events = os.NewFile(3, "guest-events")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("read guest start config: %w", err)
	}
	var cfg GuestStartConfig
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, nil, fmt.Errorf("decode guest start config: %w", err)
		}
		g.secretEnv = cfg.SecretEnv
		g.notes = cfg.Notes
		g.agentUID = cfg.AgentUID
		g.agentGID = cfg.AgentGID
		g.agentGroups = cfg.AgentGroups
		g.holderUID = cfg.HolderUID
		g.holderGID = cfg.HolderGID
		g.hookGID = cfg.HookGID
		g.agentCgroup = cfg.AgentCgroup
		m := cfg.Manifest
		if m.ID == "" {
			m.ID = id
		}
		return g, &m, nil
	}
	m, err := LoadManifestPath(g.manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load guest manifest: %w", err)
	}
	return g, m, nil
}

func holderLogPath(id string, g *guestConfig) string {
	if g != nil {
		return g.logPath
	}
	return paths.HolderLog(id)
}

func (h *Holder) isGuest() bool { return h.guest != nil }

func (h *Holder) socketPath() string {
	if h.guest != nil {
		return h.guest.socket
	}
	return paths.Socket(h.id)
}

func (h *Holder) hookSocketPath() string {
	if h.guest != nil {
		return h.guest.hookSocket
	}
	return paths.HookSocket(h.id)
}

func (h *Holder) emitGuest(ev GuestEvent) {
	if h.guest == nil {
		return
	}
	if ev.Session == "" {
		ev.Session = h.id
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b = append(b, '\n')
	h.guest.evMu.Lock()
	if h.guest.events != nil {
		_, _ = h.guest.events.Write(b)
	}
	h.guest.evMu.Unlock()
}

func (h *Holder) emitGuestStart(startErr error) {
	if !h.isGuest() {
		return
	}
	if startErr != nil {
		h.emitGuest(GuestEvent{Type: "start_failed", Reason: startErr.Error()})
		return
	}
	h.mu.Lock()
	ev := GuestEvent{
		Type:      "started",
		HolderPID: os.Getpid(),
		AgentPID:  h.m.Run.AgentPID,
		Cols:      h.m.Term.Cols,
		Rows:      h.m.Term.Rows,
	}
	h.mu.Unlock()
	h.emitGuest(ev)
}

// emitGuestStatus reports a status change to guest init. The caller holds h.mu.
func (h *Holder) emitGuestStatus() {
	if !h.isGuest() || h.exited {
		return
	}
	st := h.m.Status
	if st.State == h.guest.lastSent.State &&
		st.Source == h.guest.lastSent.Source &&
		st.Reason == h.guest.lastSent.Reason &&
		st.FullScreen == h.guest.lastSent.FullScreen {
		return
	}
	h.guest.lastSent = st
	h.emitGuest(GuestEvent{
		Type:       "status",
		State:      st.State,
		Source:     st.Source,
		Reason:     st.Reason,
		FullScreen: st.FullScreen,
	})
}

// emitGuestAlert reports an agent-side alert to guest init. The caller holds h.mu.
func (h *Holder) emitGuestAlert(kind, detail string, turn time.Duration) {
	if !h.isGuest() {
		return
	}
	h.emitGuest(GuestEvent{
		Type:   "alert",
		Kind:   kind,
		Detail: detail,
		TurnNS: int64(turn),
	})
}

// emitGuestAgentID reports a verified agent session ID change. The caller holds h.mu.
func (h *Holder) emitGuestAgentID(id, source string) {
	if !h.isGuest() {
		return
	}
	h.emitGuest(GuestEvent{Type: "agent_id", AgentID: id, IDSource: source})
}

func (h *Holder) emitGuestEnded(ended proto.Ended) {
	if !h.isGuest() {
		return
	}
	h.emitGuest(GuestEvent{Type: "ended", ExitCode: ended.ExitCode, Signal: ended.Signal})
}

func (h *Holder) emitGuestResize(cols, rows int) {
	if !h.isGuest() {
		return
	}
	h.emitGuest(GuestEvent{Type: "resize", Cols: cols, Rows: rows})
}

// guestActivity throttles activity pings to guest init to at most once per second.
func (h *Holder) guestActivity() {
	if !h.isGuest() {
		return
	}
	now := time.Now()
	h.guest.evMu.Lock()
	if now.Sub(h.guest.lastAct) < time.Second {
		h.guest.evMu.Unlock()
		return
	}
	h.guest.lastAct = now
	h.guest.evMu.Unlock()
	h.emitGuest(GuestEvent{Type: "activity"})
}

func (h *Holder) closeGuestEvents() {
	if h.guest == nil {
		return
	}
	h.guest.evMu.Lock()
	if h.guest.events != nil {
		_ = h.guest.events.Close()
		h.guest.events = nil
	}
	h.guest.evMu.Unlock()
}
