package host

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Exported host primitives reused by internal/supervisor so walled and
// unwalled sessions share one implementation of unit membership, peer checks,
// memory warnings, OOM tracking, log rotation and input classification.

// OwnUnit returns the systemd unit this process runs in when it matches
// UnitName(id), or "".
func OwnUnit(id string) string { return ownUnit(id) }

// InOwnUnit reports whether this process runs in the systemd unit of session id.
func InOwnUnit(id string) bool { return inOwnUnit(id) }

// UnitCgroupDir returns the cgroup folder of session id's unit, or "".
func UnitCgroupDir(id string) string { return unitCgroupDir(id) }

// ReadOOMKills reads the oom_kill counter of session id's systemd unit.
func ReadOOMKills(id string) int64 { return readOOMKills(id) }

// PeerOutside reports whether conn's peer runs outside every session unit and
// git scope under the sessions slice.
func PeerOutside(id string, conn net.Conn) bool {
	p, err := readPeer(conn)
	if err != nil {
		return false
	}
	if p.done != nil {
		defer p.done()
	}
	if p.pid <= 0 || p.uid != os.Getuid() {
		return false
	}
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(p.pid), "cgroup"))
	if err != nil {
		return false
	}
	place := placeIn(b, UnitName(id))
	if p.alive != nil && !p.alive() {
		return false
	}
	return place == placeOutside
}

// ErrInside is the forbidden error sent to a peer running inside a session.
func ErrInside() *proto.Error { return errInside }

// ErrNoHooksHere is the forbidden error sent when a hook request arrives on
// the session socket.
func ErrNoHooksHere() *proto.Error { return errNoHooksHere }

// ListenPrivate opens a mode-0600 Unix socket listener at sock.
func ListenPrivate(sock, what string) (net.Listener, error) { return listenPrivate(sock, what) }

// OpenRotatingLog opens path as a rotating session log capped at 1 MiB.
func OpenRotatingLog(path string) (io.WriteCloser, error) { return openHolderLog(path, holderLogMax) }

// NewSessionLogger returns a key=value text slog.Logger writing to w.
func NewSessionLogger(w io.Writer) *slog.Logger { return newHolderLog(w, nil) }

// PersonTyped reports whether p holds keys typed by a person rather than only
// terminal reports.
func PersonTyped(p []byte) bool { return personTyped(p) }

// SafeName cleans an upload filename so it cannot escape its folder.
func SafeName(n string) string { return safeName(n) }

// WasRestored reports whether restore started the session after a host restart.
func WasRestored(m *Manifest) bool { return wasRestored(m) }

// WasResumed reports whether resume started the session again.
func WasResumed(m *Manifest) bool { return wasResumed(m) }

// MissingSecretsNote returns the terminal notice shown when a session starts
// without its secrets.
func MissingSecretsNote(names []string, restored bool) string {
	return missingSecretsNote(names, restored)
}

// SecretsPayload is the JSON payload of the "secrets" request on a session socket.
type SecretsPayload = secretsMsg

// SocketsState is the reply to the "sockets" op that host doctor sends.
type SocketsState = socketsState

// SrcMemory is the status source while the memory warning marks a session.
const SrcMemory = srcMemory

// MemSample is one tick's reading of a session unit's cgroup memory files.
type MemSample struct {
	Limit  int64
	Pinned int64
	Hits   int64
	Kills  int64
	Stall  float64
}

// ReadUnitMemSample reads the cgroup memory files in dir.
func ReadUnitMemSample(dir string) MemSample {
	if dir == "" {
		return MemSample{}
	}
	r := readUnitMemory(dir)
	return MemSample{Limit: r.limit, Pinned: r.pinned, Hits: r.hits, Kills: r.kills, Stall: r.stall}
}

// MemTracker tracks memory warnings and child OOM kills for a supervised
// session's unit, sharing the exact thresholds of holder_mem.go and holder_oom.go.
type MemTracker struct {
	dir      string
	w        memWatch
	oomSeen  int64
	oomNoted int64
	reaped   bool
}

// Init finds the session unit's cgroup folder and baseline counters.
func (t *MemTracker) Init(id string) {
	t.dir = unitCgroupDir(id)
	t.oomSeen = readOOMKills(id)
	t.oomNoted = t.oomSeen
	if t.dir != "" {
		t.w.hits = readKeyed(filepath.Join(t.dir, "memory.events"))["max"]
	}
}

// Dir returns the unit's cgroup folder, or "".
func (t *MemTracker) Dir() string { return t.dir }

// NoteAttach records a non-reconnect attach for clearing a memory warning.
func (t *MemTracker) NoteAttach(reconnect bool) {
	if !reconnect {
		t.w.attached = time.Now()
	}
}

// MemTickResult reports transitions from one memory tick.
type MemTickResult struct {
	Warned   bool
	Usage    string
	Mark     bool
	Reason   string
	ChildOOM bool
}

// Step updates the memory warning and child OOM state from one tick's sample.
func (t *MemTracker) Step(s MemSample, showingMem bool, lastTyped time.Time) MemTickResult {
	var res MemTickResult
	if t.reaped {
		return res
	}
	r := memRead{limit: s.Limit, pinned: s.Pinned, hits: s.Hits, kills: s.Kills, stall: s.Stall}
	rose := r.hits > t.w.hits
	t.w.hits = r.hits
	grew := t.w.growth(r)
	switch {
	case r.limit <= 0:
		t.w.on, t.w.mark = false, false
	case r.kills > t.oomSeen:
		t.w.on = true
	case t.w.on && share(r.pinned, r.limit) < memLow:
		t.w.on, t.w.mark = false, false
	case !t.w.on && near(r, rose, grew):
		t.w.on, t.w.mark = true, true
		t.w.shown, t.w.attach, t.w.typed = time.Time{}, time.Time{}, time.Time{}
		t.w.usage = memUsage(r.pinned, r.limit)
		res.Warned = true
	}
	if t.w.on {
		if t.w.mark && showingMem {
			if t.w.shown.IsZero() {
				t.w.shown = time.Now()
			}
			if at := t.w.attached; at.After(t.w.shown) && !at.Equal(t.w.attach) {
				t.w.attach, t.w.typed = at, lastTyped
			} else if !t.w.attach.IsZero() && lastTyped.After(t.w.typed) {
				t.w.mark = false
			}
		}
		t.w.usage = memUsage(r.pinned, r.limit)
	}
	noted := t.oomNoted
	t.oomNoted = r.kills
	if noted > t.oomSeen {
		t.oomSeen = noted
		t.w.mark = false
		res.ChildOOM = true
	}
	res.Mark = t.w.mark
	res.Usage = t.w.usage
	if t.w.mark {
		res.Reason = t.w.reason()
	}
	return res
}

// Reap records that the session ended and reports whether new OOM kills
// occurred since the last claimed child kill.
func (t *MemTracker) Reap(id string) bool {
	t.reaped = true
	n := readOOMKills(id)
	if n <= t.oomSeen {
		return false
	}
	t.oomSeen = n
	return true
}
