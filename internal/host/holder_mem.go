package host

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The memory warning comes before the memory cap stops a process.
//
// A session's unit has a cap (memory.max) and no swap (MemorySwapMax=0).
// At the cap, the kernel reclaims what it can. If that is not enough, the
// out-of-memory killer stops a process (decision 0008). memory.current near
// the cap means little. In normal work, the kernel fills the page cache up
// to the cap, and reclaims it at once when a process needs the room.
//
// So the holder measures what the kernel cannot reclaim: memory.current,
// less the page cache it can drop and the slab it can free (pinned). That
// leaves anonymous memory, tmpfs and shared memory, and the kernel's own
// memory for the session, such as page tables, stacks and socket buffers.
// All of these count against the cap. On Debian 13, /tmp is a tmpfs, and a
// file there counts against the session that wrote it. (Sources:
// "memory.stat" and "Memory Ownership" in the kernel's
// admin-guide/cgroup-v2.rst.)
//
// The holder warns when that memory reaches memHigh of the cap, and one of
// these shows that the session heads for the cap. 90 % is the level
// that decision 0008 named.
//
//   - The kernel works to keep the session under the cap. Either the "max"
//     count in memory.events rose since the last tick: a process asked for
//     memory at the cap, and the kernel had to reclaim at once. Or the
//     session's tasks stalled on memory for memStall of the last 10 s
//     ("some avg10" in memory.pressure, accounting/psi.rst).
//   - The pinned memory still grows: it rose by memGrowth of the cap over
//     the last memGrowTicks ticks.
//
// The holder also warns when that memory reaches memAlone of the cap, with
// no other sign. Decision 0027 adds the growth and memAlone to the kernel's
// signs of decision 0026.
//
// 10 % is the stall at which systemd asks a service to free memory. Its
// default is 200 ms of "some" stall in each 2 s (MemoryPressureThresholdSec=).
//
// The kernel's signs are not enough on their own. On a test host (Debian 13,
// a cap of 256 MiB), a program's anonymous memory grew steadily, with no
// page cache in the unit. The pinned memory stayed at 90 % or more for 12 s
// before the kill. But the kernel had no cache to reclaim, so "max" stayed
// at 0 until the very end, and no stall showed. The memory grew by about
// 0.8 % of the cap each second, so 4 % in 5 s. memGrowth over memGrowTicks
// is 0.2 % a second: it catches a leak four times slower than that one. At
// that rate the last 10 % of the cap lasts 50 s. With page cache in the
// unit, the kernel's signs came 11 s before the kill, as designed.
//
// A full page cache raises "max" and a little stall too. But the cache is
// not pinned, so it does not warn. A large session that does not grow
// raises none of these signs, so it does not warn below memAlone. At 95 %,
// at most 5 % of the cap is left, which one tool call can take.
//
// The warning comes once an episode. The episode ends when the pinned
// memory falls below memLow of the cap. Only then can a new warning come. A
// kill at the cap also starts an episode, without a warning: the oom mark
// and its alert say more (holder_oom.go).
//
// The warning marks the session as needing input, with a reason. It sends
// one memory alert (decision 0026), and the change of state sends no
// needs_input. The agent's own wait comes first (updateStatus): while the
// agent waits, the reason is its question. While the warning shows, the
// agent's own turns still count, for turn_done (updateStatus).
//
// The mark stays until the episode ends, or until a person attaches after
// the warning showed and types. A person who was already attached never
// sees it, so their keys do not clear it. A reconnect after a dropped link
// is the same person on the same screen, so it is no new attach. Text from
// send is no attach either, so it alone does not clear the mark. When the
// cap stops a process, checkOOM clears the mark as it claims the kill.

// Levels of the memory warning: the pinned memory as shares of the cap,
// the stall as a share of the last 10 s, in %, and the growth as a share of
// the cap over memGrowTicks ticks of 1 s.
const (
	memHigh      = 0.90
	memAlone     = 0.95
	memLow       = 0.80
	memStall     = 10.0
	memGrowth    = 0.01
	memGrowTicks = 5
)

// srcMemory is the status's source while the memory warning marks it.
const srcMemory = "memory"

// memWatch is the holder's state for the memory warning. start sets dir
// before the first tick, and nothing changes it after. The caller holds h.mu
// for the rest.
type memWatch struct {
	dir   string    // the unit's cgroup folder, or "" when the holder is not in its unit
	hits  int64     // the "max" count of memory.events at the last tick
	on    bool      // an episode is under way
	mark  bool      // the warning marks the session
	usage string    // the pinned memory and the cap, such as "2.7 of 2.9 GiB"
	shown time.Time // when the warning last took the session's status
	// attached is the last attach that was no reconnect (noteAttach).
	attached time.Time
	// The last attach after the warning showed, and when a person last typed
	// as the holder saw that attach (lastTyped).
	attach time.Time
	typed  time.Time
	// past is the pinned memory of the last memGrowTicks ticks, as a ring:
	// next is the oldest, and seen how many of them there are (growth).
	past [memGrowTicks]int64
	next int
	seen int
}

// memRead is what one tick reads of the unit's memory files. Sizes are in
// bytes.
type memRead struct {
	limit  int64   // memory.max, or 0 for no cap
	pinned int64   // the memory that the kernel cannot reclaim, or memory.current when that is below memLow of the cap
	hits   int64   // the "max" count of memory.events
	kills  int64   // the "oom_kill" count of memory.events
	stall  float64 // "some avg10" of memory.pressure, in %, read only at memHigh
}

// startMemory finds the unit's cgroup folder and its "max" count, when
// the agent starts.
func (h *Holder) startMemory() {
	h.mem.dir = unitCgroupDir(h.id)
	if h.mem.dir != "" {
		h.mem.hits = readKeyed(filepath.Join(h.mem.dir, "memory.events"))["max"]
	}
}

// readMem reads the unit's memory files for one tick, outside h.mu. It
// reads nothing when the holder is not in its unit, or once the agent has
// ended.
func (h *Holder) readMem() memRead {
	h.mu.Lock()
	ended := h.reaped
	h.mu.Unlock()
	if ended || h.mem.dir == "" {
		return memRead{}
	}
	return readUnitMemory(h.mem.dir)
}

// readUnitMemory reads the memory files in a unit's cgroup folder: always
// memory.events and memory.max, and with a cap memory.current. It reads
// memory.stat only when memory.current is at memLow of the cap or more, and
// memory.pressure only when the pinned memory is at memHigh.
func readUnitMemory(dir string) memRead {
	ev := readKeyed(filepath.Join(dir, "memory.events"))
	r := memRead{hits: ev["max"], kills: ev["oom_kill"]}
	// "max" means no cap, and reads as 0.
	if r.limit = readInt(filepath.Join(dir, "memory.max")); r.limit <= 0 {
		return r
	}
	// The pinned memory is part of memory.current.
	if r.pinned = readInt(filepath.Join(dir, "memory.current")); share(r.pinned, r.limit) < memLow {
		return r
	}
	r.pinned = pinned(r.pinned, readKeyed(filepath.Join(dir, "memory.stat")))
	if share(r.pinned, r.limit) >= memHigh {
		r.stall = readPressure(filepath.Join(dir, "memory.pressure")).some.avg10
	}
	return r
}

// pinned is the part of memory.current that the kernel cannot reclaim
// without swap. It is all but the page cache ("file", less the tmpfs and
// shared memory in it, "shmem") and the slab that the kernel can free
// ("slab_reclaimable").
func pinned(current int64, stat map[string]int64) int64 {
	if stat == nil {
		return 0
	}
	return max(current-(stat["file"]-stat["shmem"])-stat["slab_reclaimable"], 0)
}

// share is n as a share of limit.
func share(n, limit int64) float64 { return float64(n) / float64(limit) }

// near reports whether one tick's read starts a warning. rose is whether
// the "max" count rose since the last tick, and grew how much the pinned
// memory rose over the last memGrowTicks ticks, as a share of the cap.
func near(r memRead, rose bool, grew float64) bool {
	s := share(r.pinned, r.limit)
	return s >= memAlone || (s >= memHigh && (rose || r.stall >= memStall || grew >= memGrowth))
}

// growth keeps the pinned memory of this tick, and returns how much it rose
// over the last memGrowTicks ticks, as a share of the cap. It is 0 until
// there are that many ticks, and it starts again without a cap. Below memLow
// of the cap, the pinned memory read is memory.current, which holds it. So
// a growth from there is never more than the real one.
func (w *memWatch) growth(r memRead) float64 {
	if r.limit <= 0 {
		w.seen = 0
		return 0
	}
	old, full := w.past[w.next], w.seen == memGrowTicks
	w.past[w.next] = r.pinned
	w.next = (w.next + 1) % memGrowTicks
	if !full {
		w.seen++
		return 0
	}
	return share(r.pinned-old, r.limit)
}

// checkMemory starts and ends the warning's episodes from one tick's read,
// and sends the memory alert when a warning starts. The caller holds h.mu.
func (h *Holder) checkMemory(r memRead) {
	if h.reaped {
		// The agent has ended, and waitLoop sets the session's end.
		return
	}
	w := &h.mem
	rose := r.hits > w.hits
	w.hits = r.hits
	grew := w.growth(r)
	switch {
	case r.limit <= 0:
		w.on, w.mark = false, false
		return
	case r.kills > h.oomSeen:
		// A kill at the cap that checkOOM has not yet accounted for. It
		// clears the mark as it claims the kill.
		w.on = true
		return
	case w.on && share(r.pinned, r.limit) < memLow:
		w.on, w.mark = false, false
		return
	case !w.on && near(r, rose, grew):
		w.on, w.mark = true, true
		w.shown, w.attach, w.typed = time.Time{}, time.Time{}, time.Time{}
		w.usage = memUsage(r.pinned, r.limit)
		h.log.Info("close to the memory cap", "pinned", r.pinned, "limit", r.limit, "max_rose", rose, "stall", r.stall, "grew", grew)
		h.alertMemory()
	case !w.on:
		return
	}
	// An episode is under way.
	h.clearMark()
	if usage := memUsage(r.pinned, r.limit); usage != w.usage {
		// The numbers in the reason follow the session's memory.
		w.usage = usage
		if h.m.Status.Source == srcMemory {
			h.m.Status.Reason = w.reason()
			h.dirty = true
		}
	}
}

// clearMark clears the mark when a person attached after the session showed
// the warning, and then typed. It looks only while the warning shows: an
// attach or a key while the agent's question hides it does not count. It
// compares times of typing only with each other, from the alert
// dispatcher's clock. So a key in the tick in which the holder sees the
// attach does not count, but the next one does. The caller holds h.mu.
func (h *Holder) clearMark() {
	w := &h.mem
	if !w.mark || h.m.Status.Source != srcMemory {
		return
	}
	if at := w.attached; at.After(w.shown) && !at.Equal(w.attach) {
		w.attach, w.typed = at, h.lastTyped()
		return
	}
	if !w.attach.IsZero() && h.lastTyped().After(w.typed) {
		w.mark = false
	}
}

// noteAttach records a client's attach for the memory warning. A reconnect
// after a dropped link is the same person on the same screen, so it does
// not count. The caller holds h.mu.
func (h *Holder) noteAttach(a proto.Attach) {
	if a.Reconnect {
		h.log.Info("client attached again after a dropped link")
		return
	}
	h.mem.attached = time.Now()
}

// reason is the warning's reason, such as "close to its memory cap (2.7 of
// 2.9 GiB)".
func (w *memWatch) reason() string { return "close to its memory cap (" + w.usage + ")" }

// memUsage is "2.7 of 2.9 GiB", or in MiB below a cap of 1 GiB.
func memUsage(used, limit int64) string {
	if limit < gib {
		return fmt.Sprintf("%d of %d MiB", used/mib, limit/mib)
	}
	g := func(n int64) string {
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/gib, 'f', 1, 64), ".0")
	}
	return g(used) + " of " + g(limit) + " GiB"
}

// lastTyped is when a person last typed into the session. The alert
// dispatcher keeps it for its own quiet rule (noteTyping). The caller holds
// h.mu.
func (h *Holder) lastTyped() time.Time {
	if h.al.d == nil {
		return time.Time{}
	}
	return h.al.d.TypedAt()
}

// alertMemory raises memory, with the session's use of its cap in the
// summary. The caller holds h.mu.
func (h *Holder) alertMemory() {
	if h.al.d != nil {
		ev := h.alertEvent(alerts.Memory)
		ev.Usage = h.mem.usage
		h.al.d.Notify(ev)
	}
}
