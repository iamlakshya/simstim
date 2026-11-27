package host

import (
	"net"

	"github.com/iamlakshya/simstim/internal/version"
)

// What the supervisor of a walled session (internal/supervisor) shares with
// the holder of a session without a wall: the same rules, from one place.

// CapTerm caps a client's terminal size as the holder does.
func CapTerm(cols, rows int) (int, int) { return capTerm(cols, rows) }

// WasRestored reports whether restore started the session of m again, after
// a host restart. Such a start waits for no secrets.
func WasRestored(m *Manifest) bool { return wasRestored(m) }

// OwnUnit returns the systemd unit this process runs in, when it is the
// unit of session id, or "".
func OwnUnit(id string) string { return ownUnit(id) }

// PeerCheck reports whether this process runs in the unit of session id,
// and so can tell a session's programs from the user's other programs.
func PeerCheck(id string) bool { return inOwnUnit(id) }

// FullPeer reports whether the peer of conn, on the socket of session id,
// may do everything: always without the peer check, and otherwise when it
// runs outside every session (holder_peer.go).
func FullPeer(conn net.Conn, id string, peerCheck bool) bool {
	return !peerCheck || (&Holder{id: id}).placeOf(conn) == placeOutside
}

// The refusals of the session socket, each with the code forbidden.
var (
	// ErrInside refuses a program inside a session.
	ErrInside = errInside
	// ErrNoHooksHere refuses a hook event on the session socket.
	ErrNoHooksHere = errNoHooksHere
)

// SocketsState is the answer to the op "sockets", which the doctor reads.
func SocketsState(peerCheck bool) any {
	return socketsState{PeerCheck: peerCheck, Version: version.Version}
}

// PersonTyped reports whether input holds anything but the reports that a
// terminal sends by itself.
func PersonTyped(p []byte) bool { return personTyped(p) }

// OOMChildReason is a session's reason after the memory cap stopped a
// process that the agent started.
const OOMChildReason = oomChildReason

// MemoryReason is the reason of a session close to its memory cap, such as
// "close to its memory cap (2.7 of 2.9 GiB)".
func MemoryReason(usage string) string { return (&memWatch{usage: usage}).reason() }

// SrcMemory is the status's source while the memory warning marks it.
const SrcMemory = srcMemory

// UnitWatch watches the memory of a session's unit, for the supervisor of
// a walled session. It follows the rules of the holder's memory warning
// (holder_mem.go) and kill count (holder_oom.go), on the unit's cgroup
// folder. A kill that a tick finds is only noted. The next tick claims it
// for a process that the agent started, unless the agent's end took it
// first (End), so the order of a tick and the end does not matter.
type UnitWatch struct {
	dir   string
	base  int64 // the kill count when the watch began
	seen  int64 // the kills accounted for, by a tick or by End
	noted int64 // the kill count that the last tick read
	w     memWatch
}

// MemTick is what one tick of a UnitWatch found.
type MemTick struct {
	// Kills is how many processes the memory cap stopped, which a tick
	// noted before and which the agent's end did not take.
	Kills int64
	// Warn is true when a warning starts: the session heads for its cap.
	Warn bool
	// Calm is true when an episode ends: the pinned memory fell below 80 %
	// of the cap.
	Calm bool
	// Usage is the pinned memory and the cap, such as "2.7 of 2.9 GiB",
	// while an episode runs.
	Usage string
}

// UnitCgroupDir is the cgroup folder of the unit of session id, when this
// process runs in it, or "".
func UnitCgroupDir(id string) string { return unitCgroupDir(id) }

// NewUnitWatch watches the unit whose cgroup folder is dir. With dir "",
// or a nil UnitWatch, every tick finds nothing.
func NewUnitWatch(dir string) *UnitWatch {
	u := &UnitWatch{dir: dir}
	if dir != "" {
		r := readUnitMemory(dir)
		u.base, u.seen, u.noted, u.w.hits = r.kills, r.kills, r.kills, r.hits
	}
	return u
}

// End reads the kill count now, and returns the kills that no tick has
// claimed yet, which it then accounts for. At the agent's end, with the
// agent's SIGKILL, they are its own kill at the cap. At the sandbox's end,
// they are the kills that the last ticks did not take.
func (u *UnitWatch) End() int64 {
	if u == nil || u.dir == "" {
		return 0
	}
	n := readUnitMemory(u.dir).kills
	if n <= u.seen {
		return 0
	}
	k := n - u.seen
	u.seen = n
	return k
}

// Base is the kill count when the watch began. The supervisor starts its
// watch before the guest starts, so that a kill during the boot counts.
func (u *UnitWatch) Base() int64 {
	if u == nil {
		return 0
	}
	return u.base
}

// Tick reads the unit's memory files once.
func (u *UnitWatch) Tick() MemTick {
	if u == nil || u.dir == "" {
		return MemTick{}
	}
	r := readUnitMemory(u.dir)
	var t MemTick
	w := &u.w
	noted := u.noted
	u.noted = r.kills
	if noted > u.seen {
		// The kill that the last tick noted, and no end took: a process
		// that the agent started. A kill at the cap starts an episode,
		// without a warning: the oom mark and its alert say more.
		t.Kills, u.seen = noted-u.seen, noted
		w.on = true
	}
	rose := r.hits > w.hits
	w.hits = r.hits
	grew := w.growth(r)
	switch {
	case r.limit <= 0:
		w.on = false
	case w.on && share(r.pinned, r.limit) < memLow:
		w.on, t.Calm = false, true
	case !w.on && near(r, rose, grew):
		w.on, t.Warn = true, true
	}
	if w.on && r.limit > 0 {
		t.Usage = memUsage(r.pinned, r.limit)
	}
	return t
}
