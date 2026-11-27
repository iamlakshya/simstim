package host

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A holder in guest mode runs inside a walled session's sandbox (spec 4).
// The guest init starts it with the paths of its two sockets and of its
// own manifest in the environment, and with one end of a socket pair on
// fd 3. In guest mode the holder:
//
//   - listens on the guest's paths, and reads and writes the manifest that
//     the guest init wrote. The trusted manifest stays outside, and only the
//     supervisor writes it.
//   - skips systemd: it has no unit of its own, no peer check, no memory
//     watch and no kill count. The supervisor keeps those facts, outside
//     the wall.
//   - sends no alert itself. It reports its status, alerts, the agent's own
//     session ID and the agent's end to the guest init on fd 3, as TEvent
//     frames. The guest init passes them to the supervisor as claims.
//   - stops when the guest init goes, which closes fd 3.

// The variables that put a holder in guest mode.
var (
	envHolderSock     = brand.Env("HOLDER_SOCK")
	envHolderHook     = brand.Env("HOLDER_HOOK_SOCK")
	envHolderManifest = brand.Env("HOLDER_MANIFEST")
)

// GuestHolder names the files of a holder in guest mode.
type GuestHolder struct {
	Sock     string // its socket, which the guest init connects holder streams to
	Hook     string // its hook socket, which the agent's environment names
	Manifest string // its manifest, which the guest init writes first
}

// Env returns the variables that start a holder in guest mode with these
// files.
func (g GuestHolder) Env() []string {
	return []string{envHolderSock + "=" + g.Sock, envHolderHook + "=" + g.Hook, envHolderManifest + "=" + g.Manifest}
}

// guestHolder reads the files of guest mode from this process's
// environment. ok is false for a holder of a session without a wall.
func guestHolder() (g GuestHolder, ok bool) {
	g = GuestHolder{Sock: os.Getenv(envHolderSock), Hook: os.Getenv(envHolderHook), Manifest: os.Getenv(envHolderManifest)}
	ok = g.Sock != "" && g.Hook != "" && g.Manifest != ""
	return g, ok
}

// holderManifest reads the manifest of session id's holder: the session's
// own, or in guest mode the one that the guest init wrote. guest is set in
// guest mode.
func holderManifest(id string) (m *Manifest, guest *GuestHolder, err error) {
	g, ok := guestHolder()
	if !ok {
		m, err = LoadManifest(id)
		return m, nil, err
	}
	m, err = LoadManifestFile(g.Manifest)
	return m, &g, err
}

// openGuestEvents opens the connection to the guest init in guest mode.
// Without a sandbox it returns nil.
func (h *Holder) openGuestEvents() (*guestEvents, error) {
	if h.guest == nil {
		return nil, nil
	}
	return openGuestEvents()
}

// watchGuest stops the holder when its guest init goes, in guest mode.
// The holder calls it once the agent has started.
func (h *Holder) watchGuest() {
	if h.ev == nil {
		return
	}
	go func() {
		// The guest init sends nothing on it. A read ends when it goes.
		_, _ = io.Copy(io.Discard, bufio.NewReader(h.ev.conn))
		h.stop("the guest init ended", false)
	}()
}

// guestEventsFD is the fd of the socket pair to the guest init.
const guestEventsFD = 3

// guestQueue is how many ordinary events may wait for the guest init. More
// are dropped: they are claims, and the next status says the same again.
// The agent's terminal end has its own reserved slot, so it is never dropped.
const guestQueue = 256

// guestEvents sends a guest holder's events to its guest init. Its writer
// runs apart, so a caller that holds h.mu never waits for the socket.
type guestEvents struct {
	conn      net.Conn
	out       chan proto.CtlEvent
	end       chan proto.CtlEvent
	endOnce   sync.Once
	endQueued atomic.Bool
	endDone   chan struct{}
	mu        sync.Mutex
	last      proto.CtlEvent // the last status sent, so that only a change goes
	endBy     time.Time      // the cumulative deadline for claims before ended
}

// openGuestEvents opens the socket pair on fd 3.
func openGuestEvents() (*guestEvents, error) {
	f := os.NewFile(guestEventsFD, "guest-init")
	if f == nil {
		return nil, UserError("guest_init", "the holder has no connection to its guest init",
			"Start the holder from the guest init, which passes it on fd 3.")
	}
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, UserError("guest_init", "the holder has no connection to its guest init: "+err.Error(),
			"Start the holder from the guest init, which passes it on fd 3.")
	}
	return newGuestEvents(conn), nil
}

func newGuestEvents(conn net.Conn) *guestEvents {
	g := &guestEvents{
		conn:    conn,
		out:     make(chan proto.CtlEvent, guestQueue),
		end:     make(chan proto.CtlEvent, 1),
		endDone: make(chan struct{}),
	}
	go g.loop()
	return g
}

// loop writes ordinary events in order. The terminal event gets a bounded
// drain of prior claims, then its reserved slot goes next.
func (g *guestEvents) loop() {
	defer close(g.endDone)
	for {
		if g.endQueued.Load() {
			g.finishEvent(<-g.end)
			return
		}
		select {
		case ev := <-g.out:
			switch g.writeEvent(ev, true) {
			case guestWritten:
			case guestClosed:
				return
			case guestSkipped:
				if g.endQueued.Load() {
					g.dropClaims()
					g.writeEvent(<-g.end, false)
					return
				}
			}
		case ev := <-g.end:
			g.finishEvent(ev)
			return
		}
	}
}

// flush waits up to timeout for a queued EventEnded to reach the guest init
// before the holder exits its process.
func (g *guestEvents) flush(timeout time.Duration) {
	if g == nil || !g.endQueued.Load() {
		return
	}
	select {
	case <-g.endDone:
	case <-time.After(timeout):
	}
}

// send queues one ordinary event. When the queue is full, the event is
// dropped: the holder runs on, and the guest init's end stops it.
func (g *guestEvents) send(ev proto.CtlEvent) {
	if g == nil || g.endQueued.Load() {
		return
	}
	select {
	case g.out <- ev:
	default:
	}
}

// status sends the session's status when it changed since the last one.
func (g *guestEvents) status(id string, st Status) {
	if g == nil {
		return
	}
	ev := proto.CtlEvent{Event: proto.EventStatus, Session: id, State: st.State, Source: st.Source, Reason: st.Reason, FullScreen: st.FullScreen}
	g.mu.Lock()
	same := ev == g.last
	g.last = ev
	g.mu.Unlock()
	if !same {
		g.send(ev)
	}
}

// alert sends an alert that the holder raised: needs_input or turn_done.
func (g *guestEvents) alert(id string, ev alerts.Event) {
	g.send(proto.CtlEvent{Event: proto.EventAlert, Session: id, Kind: ev.Event, Detail: ev.Detail})
}

// agentID sends the agent's own session ID, after a hook gave it.
func (g *guestEvents) agentID(id, agentID string) {
	g.send(proto.CtlEvent{Event: proto.EventAgentID, Session: id, ID: agentID})
}

// ended sends the agent's end in its reserved slot, so a full queue of
// status or alert claims never drops the sole terminal event.
func (g *guestEvents) ended(id string, e proto.Ended) {
	if g == nil {
		return
	}
	code := e.ExitCode
	ev := proto.CtlEvent{Event: proto.EventEnded, Session: id, ExitCode: &code, Signal: e.Signal}
	g.endOnce.Do(func() {
		g.mu.Lock()
		g.endBy = time.Now().Add(guestClaimsDrain)
		g.mu.Unlock()
		g.endQueued.Store(true)
		g.end <- ev
	})
}

// holderLogPath is the log of the holder of session id: in the session's
// folder, or beside its manifest in guest mode.
func (h *Holder) holderLogPath() string {
	if h.guest != nil {
		return filepath.Join(filepath.Dir(h.guest.Manifest), "holder.log")
	}
	return paths.HolderLog(h.id)
}

// sockets are the holder's session socket and hook socket.
func (h *Holder) sockets() (sock, hook string) {
	if h.guest != nil {
		return h.guest.Sock, h.guest.Hook
	}
	return paths.Socket(h.id), paths.HookSocket(h.id)
}

// notify sends an alert: to the dispatcher, or in guest mode to the guest
// init. The caller holds h.mu.
func (h *Holder) notify(ev alerts.Event) {
	if h.al.d != nil {
		h.al.d.Notify(ev)
	}
	h.ev.alert(h.id, ev)
}
