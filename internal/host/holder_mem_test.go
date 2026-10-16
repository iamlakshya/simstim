package host

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/proto"
)

// gb is a size of x GiB, in bytes.
func gb(x float64) int64 { return int64(x * gib) }

// memCap is the cap of the memory rig's unit: 2.9 GiB.
var memCap = gb(2.9)

// memRig is an oomRig whose unit has a memory cap. The test sets what the
// unit's memory files say.
type memRig struct {
	*oomRig
	dir  string
	hits int // the "max" count of memory.events
	oom  int // the "oom_kill" count of memory.events
}

func newMemRig(t *testing.T) *memRig {
	t.Helper()
	r := &memRig{oomRig: newOOMRig(t)}
	r.dir = filepath.Dir(r.events)
	r.file("memory.max", strconv.FormatInt(memCap, 10))
	r.use(0.5, 0, 0.5)
	r.stall(0)
	return r
}

func (r *memRig) file(name, body string) {
	writeTestFile(r.t, filepath.Join(r.dir, name), body+"\n")
}

// use sets the unit's anonymous memory, its tmpfs and shared memory, and
// its other page cache, in GiB.
func (r *memRig) use(anon, shmem, cache float64) { r.useKernel(anon, shmem, cache, 0, 0) }

// useKernel also sets the kernel's memory for the unit, and the part of it
// that the kernel can free, in GiB.
func (r *memRig) useKernel(anon, shmem, cache, kernel, slabFree float64) {
	a, s, c, k, f := gb(anon), gb(shmem), gb(cache), gb(kernel), gb(slabFree)
	r.file("memory.current", strconv.FormatInt(a+s+c+k, 10))
	// "file" counts shmem too, and "kernel" counts the slab.
	r.file("memory.stat", fmt.Sprintf("anon %d\nfile %d\nkernel %d\nshmem %d\nslab_reclaimable %d\nslab_unreclaimable %d",
		a, c+s, k, s, f, k-f))
}

// hit counts one more charge at the cap in memory.events.
func (r *memRig) hit() {
	r.hits++
	r.writeEvents()
}

// oomKill counts a kill at the cap, as the kernel does.
func (r *memRig) oomKill() {
	r.hits++
	r.oom++
	r.writeEvents()
}

func (r *memRig) writeEvents() {
	writeTestFile(r.t, r.events, fmt.Sprintf("low 0\nhigh 0\nmax %d\noom %d\noom_kill %d\n", r.hits, r.oom, r.oom))
}

// stall sets the "some avg10" of memory.pressure, in %.
func (r *memRig) stall(avg10 float64) {
	r.file("memory.pressure", fmt.Sprintf("some avg10=%.2f avg60=0.00 avg300=0.00 total=1000\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0", avg10))
}

// near puts 2.7 of the session's 2.9 GiB in memory that the kernel cannot
// reclaim, with the page cache in the rest, and counts a charge at the cap.
func (r *memRig) near() {
	r.use(2.5, 0.2, 0.2)
	r.hit()
}

// hook applies an agent's hook event, such as its question.
func (r *memRig) hook(event, payload string) {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	r.h.applyHook(HookEvent{Event: event, Payload: json.RawMessage(payload)})
}

// typed notes a person's key, as the holder does for each input, and for
// the text of a send.
func (r *memRig) typed() { noteTyping(r.h.al.d, []byte("y")) }

// attach notes a client's attach, as the holder does. reconnect is true
// when the client resumes after a dropped link.
func (r *memRig) attach(reconnect bool) {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	r.h.m.LastAttachedAt = time.Now().UTC()
	r.h.noteAttach(proto.Attach{Reconnect: reconnect})
}

const nearReason = "close to its memory cap (2.7 of 2.9 GiB)"

func (r *memRig) wantWarned() {
	r.t.Helper()
	if st := r.status(); st.State != StateNeedsInput || st.Source != srcMemory || st.Reason != nearReason {
		r.t.Fatalf("near the cap: %+v", st)
	}
}

func (r *memRig) wantClear() {
	r.t.Helper()
	if st := r.status(); st.State == StateNeedsInput || st.Source == srcMemory {
		r.t.Fatalf("the warning did not clear: %+v", st)
	}
}

// wantWarning checks that one alert went out: memory, with the session's
// use of its cap in the summary.
func (r *memRig) wantWarning() {
	r.t.Helper()
	got := r.sentEvents()
	if len(got) != 1 || got[0].Event != alerts.Memory || !strings.HasSuffix(got[0].Summary, " is close to its memory cap (2.7 of 2.9 GiB)") {
		r.t.Fatalf("alerts %+v, want one memory alert", got)
	}
}

// The kernel fills the page cache up to the cap in normal work, and
// reclaims it at each charge there. This is no reason to warn, even when the
// session's tasks stall a little on the reclaim.
func TestMemoryCacheAloneDoesNotWarn(t *testing.T) {
	r := newMemRig(t)
	r.use(0.5, 0.1, 2.3)
	r.stall(25)
	for range 3 {
		r.hit()
		r.ticks(1)
	}
	r.wantClear()
	r.wantSent()
}

// Memory that cannot be reclaimed near the cap, and a charge at the cap:
// one warning, and one memory alert for the whole episode, with no
// needs_input. The reason follows the session's memory.
func TestMemoryWarnsOnce(t *testing.T) {
	r := newMemRig(t)
	r.near()
	r.ticks(1)
	r.wantWarned()
	r.wantWarning()
	for range 3 {
		r.hit()
		r.ticks(1)
	}
	r.wantWarned()
	r.use(2.6, 0.2, 0.1)
	r.ticks(1)
	if st := r.status(); st.State != StateNeedsInput || st.Reason != "close to its memory cap (2.8 of 2.9 GiB)" {
		t.Fatalf("the reason did not follow the memory: %+v", st)
	}
	r.wantSent()
}

// The kernel's own memory for the session counts against its cap too, and
// what it can free of its slab does not.
func TestMemoryCountsKernelMemory(t *testing.T) {
	r := newMemRig(t)
	r.useKernel(2.3, 0, 0.25, 0.35, 0) // 79 % anonymous, 91 % in all
	r.hit()
	r.ticks(1)
	if st := r.status(); st.Source != srcMemory || st.Reason != "close to its memory cap (2.6 of 2.9 GiB)" {
		t.Fatalf("with page tables and stacks near the cap: %+v", st)
	}
	r.wantSent(alerts.Memory)

	r = newMemRig(t)
	r.useKernel(2.0, 0, 0.2, 0.7, 0.6) // the kernel can free most of its slab
	r.hit()
	r.ticks(1)
	r.wantClear()
	r.wantSent()
}

// A session near the cap that does not grow is no reason to warn. A stall
// on memory is, as a charge at the cap is.
func TestMemoryWarnsOnStall(t *testing.T) {
	r := newMemRig(t)
	r.use(2.5, 0.2, 0.2)
	r.ticks(3)
	r.wantClear()
	r.stall(9)
	r.ticks(1)
	r.wantClear()
	r.stall(12)
	r.ticks(1)
	r.wantWarned()
	r.wantWarning()
}

// The mark stays above the lower level, and clears below it. The next
// episode warns again.
func TestMemoryEpisodeEnds(t *testing.T) {
	r := newMemRig(t)
	r.near()
	r.ticks(1)
	r.wantWarning()
	r.use(2.2, 0.2, 0.5) // 83 %
	r.ticks(1)
	if st := r.status(); st.State != StateNeedsInput || st.Reason != "close to its memory cap (2.4 of 2.9 GiB)" {
		t.Fatalf("between the levels: %+v", st)
	}
	r.use(2.0, 0.1, 0.8) // 72 %, with a full cache
	r.ticks(1)
	r.wantClear()
	r.wantSent()

	r.near()
	r.ticks(1)
	r.wantWarned()
	r.wantWarning()
}

// A person who is attached and types never sees the warning, so the keys
// do not clear it, and the alert still goes out. Nor does a send. A
// reconnect after a dropped link is the same person on the same screen. A
// person who attaches after the warning showed, and types, clears it. The
// episode goes on, and does not warn again.
func TestMemoryMarkClearsAfterANewAttach(t *testing.T) {
	r := newMemRig(t)
	r.attach(false)
	r.typed()
	r.near()
	r.ticks(1)
	r.typed()
	r.ticks(2)
	r.wantWarned()
	r.wantWarning()

	r.attach(true)
	r.ticks(1)
	r.typed()
	r.ticks(2)
	r.wantWarned()

	r.attach(false)
	r.ticks(1)
	r.wantWarned()
	r.typed()
	r.ticks(1)
	r.wantClear()
	for range 3 {
		r.hit()
		r.ticks(1)
	}
	r.wantClear()
	r.wantSent()
}

// An attach while the agent's question hides the warning does not count as
// having seen the warning, nor do keys then. An attach after the warning
// shows again does.
func TestMemoryAttachWhileHidden(t *testing.T) {
	r := newMemRig(t)
	r.near()
	r.ticks(1)
	r.wantWarned()
	r.hook("notification", `{"message":"Allow?"}`)
	r.ticks(1)
	r.attach(false)
	r.ticks(1)
	r.typed()
	r.ticks(1)
	r.hook("stop", "")
	r.ticks(1)
	r.typed()
	r.ticks(2)
	r.wantWarned()

	r.attach(false)
	r.ticks(1)
	r.typed()
	r.ticks(1)
	r.wantClear()
}

// While the agent waits, the reason is its question, and the warning waits
// behind it. Each has its own alert.
func TestMemoryAgentQuestionFirst(t *testing.T) {
	const q = "Claude needs your permission to use Bash"
	r := newMemRig(t)
	r.hook("notification", `{"message":"`+q+`"}`)
	r.ticks(1)
	r.wantSent(alerts.NeedsInput)

	r.near()
	r.ticks(1)
	if st := r.status(); st.State != StateNeedsInput || st.Source != "notify" || st.Reason != q {
		t.Fatalf("the warning hid the agent's question: %+v", st)
	}
	r.wantWarning()
	r.hook("stop", "")
	r.ticks(1)
	r.wantWarned()
	r.wantSent()

	r.hook("notification", `{"message":"`+q+`"}`)
	r.ticks(1)
	if st := r.status(); st.State != StateNeedsInput || st.Source != "notify" || st.Reason != q {
		t.Fatalf("the agent asked during the warning: %+v", st)
	}
	r.wantSent(alerts.NeedsInput)
	r.hook("stop", "")
	r.ticks(1)
	r.wantWarned()
	r.wantSent()
}

// The agent works on while the warning shows, and its turn still counts:
// turn_done goes out when it ends, with the whole turn.
func TestMemoryWarningKeepsTurns(t *testing.T) {
	r := newMemRig(t)
	r.hook("prompt", "")
	r.ticks(1)
	r.h.mu.Lock()
	r.h.al.workSince = time.Now().Add(-30 * time.Second)
	r.h.mu.Unlock()
	r.near()
	r.ticks(1)
	r.wantWarned()
	r.wantWarning()
	r.h.mu.Lock()
	r.h.al.workSince = r.h.al.workSince.Add(-5 * time.Minute) // the turn goes on
	r.h.mu.Unlock()
	r.hook("stop", "")
	r.ticks(1)
	r.wantWarned()
	got := r.sentEvents()
	if len(got) != 1 || got[0].Event != alerts.TurnDone || !strings.Contains(got[0].Summary, "finished a turn (5m30s)") {
		t.Fatalf("alerts %+v, want turn_done for the whole turn", got)
	}
}

// The cap stops a process that the agent started, after the warning. The
// oom mark and its alert take over, and the episode goes on without a new
// warning until the memory falls.
func TestMemoryWarningThenChildKill(t *testing.T) {
	r := newMemRig(t)
	r.near()
	r.ticks(1)
	r.wantWarning()
	r.oomKill()
	r.ticks(1) // notes the kill: the warning shows until checkOOM claims it
	r.wantWarned()
	r.ticks(1)
	st := r.status()
	if !st.OOM || st.Reason != oomChildReason || st.State == StateNeedsInput {
		t.Fatalf("a child's kill after the warning: %+v", st)
	}
	r.wantSent(alerts.OOM)
	for range 3 {
		r.hit()
		r.ticks(1)
	}
	r.wantSent()

	r.use(1.0, 0, 1.9)
	r.ticks(1)
	r.near()
	r.ticks(1)
	r.wantWarned()
	r.wantWarning()
}

// The same while the agent waits for an answer: the warning goes out
// before the oom alert, and nothing after it.
func TestMemoryWarningWhileWaitingThenChildKill(t *testing.T) {
	r := newMemRig(t)
	r.hook("notification", `{"message":"Allow?"}`)
	r.ticks(1)
	r.wantSent(alerts.NeedsInput)
	r.near()
	r.ticks(1)
	r.oomKill()
	r.ticks(3)
	if st := r.status(); !st.OOM || st.Reason != "Allow?" {
		t.Fatalf("a child's kill while the agent waits: %+v", st)
	}
	r.wantSent(alerts.Memory, alerts.OOM)
}

// A kill at the cap with no warning before it does not warn after it.
func TestMemoryKillWithoutWarning(t *testing.T) {
	r := newMemRig(t)
	r.use(2.5, 0.2, 0.2)
	r.oomKill()
	r.ticks(3)
	if st := r.status(); !st.OOM || st.State == StateNeedsInput {
		t.Fatalf("a kill at the cap: %+v", st)
	}
	r.wantSent(alerts.OOM)
}

// The cap stops the agent itself after the warning. Between the reap and
// the end, a tick keeps the session's state, so no other alert comes
// between the warning and the oom alert.
func TestMemoryWarningThenAgentKill(t *testing.T) {
	r := newMemRig(t)
	r.near()
	r.ticks(1)
	r.oomKill()
	r.killWhile(func() {
		r.ticks(1)
		r.wantWarned()
	})
	if st := r.status(); !st.OOM || st.State != StateErrored {
		t.Fatalf("the agent's own kill: %+v", st)
	}
	r.wantSent(alerts.Memory, alerts.OOM)
}

// Without a cap there is nothing to warn about.
func TestMemoryNoCap(t *testing.T) {
	r := newMemRig(t)
	r.file("memory.max", "max")
	r.use(2.8, 0.2, 0)
	r.stall(50)
	for range 3 {
		r.hit()
		r.ticks(1)
	}
	r.wantClear()
	r.wantSent()
}

// A holder outside its own unit, as without systemd or on cgroup v1, reads
// nothing and never warns.
func TestMemoryOutsideItsUnit(t *testing.T) {
	id := NewID()
	fakeProc(t, map[string]string{"self/cgroup": "4:memory:/user.slice/" + UnitName(id) + "\n0::/system.slice/ci-runner.service\n"})
	h := &Holder{id: id, m: &Manifest{}}
	h.startMemory()
	if r := h.readMem(); r != (memRead{}) {
		t.Fatalf("read %+v outside the unit", r)
	}
	h.checkMemory(memRead{})
	if h.mem.mark || h.mem.on {
		t.Fatal("a warning outside the unit")
	}
}

func TestMemUsage(t *testing.T) {
	for _, c := range []struct {
		used, limit int64
		want        string
	}{
		{gb(2.7), gb(2.9), "2.7 of 2.9 GiB"},
		{gb(7.3), gb(8), "7.3 of 8 GiB"},
		{700 << 20, 900 << 20, "700 of 900 MiB"},
	} {
		if got := memUsage(c.used, c.limit); got != c.want {
			t.Errorf("memUsage(%d, %d) = %q, want %q", c.used, c.limit, got, c.want)
		}
	}
}

func TestReadPressure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.pressure")
	writeTestFile(t, path, "some avg10=12.50 avg60=3.00 avg300=0.40 total=123456\nfull avg10=4.25 avg60=1.00 avg300=0.10 total=6789\n")
	p := readPressure(path)
	if p.some != (stall{avg10: 12.5, total: 123456}) || p.full != (stall{avg10: 4.25, total: 6789}) || p.some.ns() != 123456000 {
		t.Fatalf("readPressure = %+v", p)
	}
	if p := readPressure(filepath.Join(t.TempDir(), "none")); p != (pressure{}) {
		t.Fatalf("a missing file reads as %+v", p)
	}
}
